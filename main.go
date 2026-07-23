package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultAddress = ":8758"
	updateInterval = 5 * time.Second
	requestTimeout = 5 * time.Second
)

type config struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type systemResponse struct {
	MemoryFree    *metricNumber `json:"memfree"`
	MemoryTotal   *metricNumber `json:"memtotal"`
	MemoryCache   *metricNumber `json:"memcache"`
	MemoryBuffers *metricNumber `json:"membuffers"`
	CPULoad       *metricNumber `json:"cpuload"`
	Uptime        *metricNumber `json:"uptime"`
	ConnFree      *metricNumber `json:"connfree"`
	ConnTotal     *metricNumber `json:"conntotal"`
}

type metricNumber float64

func (number *metricNumber) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "null" {
		return nil
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return fmt.Errorf("invalid quoted number: %w", err)
		}
		value = unquoted
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("invalid number %q", value)
	}
	*number = metricNumber(parsed)
	return nil
}

type interfaceResponse struct {
	InterfaceName string                     `json:"interface-name"`
	ID            any                        `json:"id"`
	Ports         map[string]json.RawMessage `json:"port"`
}

type port struct {
	ID, Label   string
	Link, Speed float64
}

type snapshot struct {
	Config    config
	System    systemResponse
	Interface string
	Ports     map[string]port
}

type exporter struct {
	client *http.Client
	mu     sync.RWMutex
	data   map[string]snapshot
}

func main() {
	configs, err := loadConfig(envOrDefault("CONFIG_PATH", "config.json"))
	if err != nil {
		log.Fatalf("load configuration: %v", err)
	}
	address := envOrDefault("LISTEN_ADDRESS", defaultAddress)
	exp := newExporter(configs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exp.start(ctx, configs)

	server := &http.Server{
		Addr:              address,
		Handler:           exp.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown: %v", err)
		}
	}()

	log.Printf("serving metrics on %s/metrics", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server: %v", err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func loadConfig(path string) ([]config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var configs []config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configs); err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return nil, errors.New("at least one device is required")
	}
	seen := make(map[string]struct{}, len(configs))
	for i, cfg := range configs {
		if cfg.Name == "" || cfg.IP == "" || cfg.Username == "" || cfg.Password == "" {
			return nil, fmt.Errorf("device %d: name, ip, username and password are required", i+1)
		}
		parsed, err := url.ParseRequestURI(cfg.IP)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("device %q: invalid HTTP(S) URL %q", cfg.Name, cfg.IP)
		}
		if _, exists := seen[cfg.IP]; exists {
			return nil, fmt.Errorf("duplicate device URL %q", cfg.IP)
		}
		seen[cfg.IP] = struct{}{}
	}
	return configs, nil
}

func newExporter(configs []config) *exporter {
	data := make(map[string]snapshot, len(configs))
	for _, cfg := range configs {
		data[cfg.IP] = snapshot{Config: cfg, System: emptySystem(), Ports: map[string]port{}}
	}
	return &exporter{client: &http.Client{Timeout: requestTimeout}, data: data}
}

func emptySystem() systemResponse {
	nan := metricNumber(math.NaN())
	return systemResponse{
		MemoryFree: &nan, MemoryTotal: &nan, MemoryCache: &nan, MemoryBuffers: &nan,
		CPULoad: &nan, Uptime: &nan, ConnFree: &nan, ConnTotal: &nan,
	}
}

func (e *exporter) start(ctx context.Context, configs []config) {
	for _, cfg := range configs {
		go e.updateLoop(ctx, cfg)
	}
}

func (e *exporter) updateLoop(ctx context.Context, cfg config) {
	e.update(ctx, cfg)
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.update(ctx, cfg)
		}
	}
}

func (e *exporter) update(ctx context.Context, cfg config) {
	var system systemResponse
	if err := e.fetch(ctx, cfg.IP, cfg, &system); err != nil {
		log.Printf("%s: update system metrics: %v", cfg.Name, err)
		system = emptySystem()
	} else {
		fillMissingWithNaN(&system)
	}

	var iface interfaceResponse
	ports := map[string]port{}
	if err := e.fetch(ctx, interfaceURL(cfg.IP), cfg, &iface); err != nil {
		log.Printf("%s: update interface metrics: %v", cfg.Name, err)
	} else {
		ports = parsePorts(iface.Ports)
	}
	interfaceName := iface.InterfaceName
	if interfaceName == "" {
		interfaceName = stringValue(iface.ID)
	}
	if interfaceName == "" && len(ports) > 0 {
		interfaceName = "GigabitEthernet0"
	}

	e.mu.Lock()
	e.data[cfg.IP] = snapshot{Config: cfg, System: system, Interface: interfaceName, Ports: ports}
	e.mu.Unlock()
}

func (e *exporter) fetch(ctx context.Context, endpoint string, cfg config, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(cfg.Username, cfg.Password)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

func interfaceURL(systemURL string) string {
	parsed, err := url.Parse(systemURL)
	if err != nil {
		return systemURL
	}
	parsed.Path = "/rci/show/interface/GigabitEthernet0"
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	return parsed.String()
}

func fillMissingWithNaN(data *systemResponse) {
	fields := []**metricNumber{
		&data.MemoryFree, &data.MemoryTotal, &data.MemoryCache, &data.MemoryBuffers,
		&data.CPULoad, &data.Uptime, &data.ConnFree, &data.ConnTotal,
	}
	for _, field := range fields {
		if *field == nil {
			nan := metricNumber(math.NaN())
			*field = &nan
		}
	}
}

func parsePorts(rawPorts map[string]json.RawMessage) map[string]port {
	ports := make(map[string]port, len(rawPorts))
	for number, raw := range rawPorts {
		var values map[string]any
		if err := json.Unmarshal(raw, &values); err != nil {
			continue
		}
		label := stringValue(values["label"])
		if label == "" {
			label = number
		}
		link := 0.0
		if strings.EqualFold(stringValue(values["link"]), "up") {
			link = 1
		}
		ports[number] = port{
			ID: stringValue(values["id"]), Label: label,
			Link: link, Speed: numberValue(values["speed"]),
		}
	}
	return ports
}

func stringValue(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func numberValue(value any) float64 {
	number, err := strconv.ParseFloat(stringValue(value), 64)
	if err != nil {
		return math.NaN()
	}
	return number
}

func (e *exporter) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", e.metrics)
	return mux
}

func (e *exporter) metrics(w http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	snapshots := make([]snapshot, 0, len(e.data))
	for _, data := range e.data {
		snapshots = append(snapshots, data)
	}
	e.mu.RUnlock()
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Config.IP < snapshots[j].Config.IP })

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writeMetricHeaders(w)
	for _, data := range snapshots {
		deviceLabels := labels(map[string]string{
			"device_name": data.Config.Name, "device_ip": data.Config.IP,
		})
		writeSample(w, "keenetic_memory_free_kb", deviceLabels, float64(*data.System.MemoryFree))
		writeSample(w, "keenetic_memory_total_kb", deviceLabels, float64(*data.System.MemoryTotal))
		writeSample(w, "keenetic_memory_cache_kb", deviceLabels, float64(*data.System.MemoryCache))
		writeSample(w, "keenetic_memory_buffers_kb", deviceLabels, float64(*data.System.MemoryBuffers))
		writeSample(w, "keenetic_cpu_load", deviceLabels, float64(*data.System.CPULoad))
		writeSample(w, "keenetic_uptime_seconds", deviceLabels, float64(*data.System.Uptime))
		writeSample(w, "keenetic_connections_free", deviceLabels, float64(*data.System.ConnFree))
		writeSample(w, "keenetic_connections_total", deviceLabels, float64(*data.System.ConnTotal))

		portNumbers := make([]string, 0, len(data.Ports))
		for number := range data.Ports {
			portNumbers = append(portNumbers, number)
		}
		sort.Strings(portNumbers)
		for _, number := range portNumbers {
			port := data.Ports[number]
			portLabels := labels(map[string]string{
				"device_name": data.Config.Name, "device_ip": data.Config.IP,
				"interface": data.Interface, "port": number, "port_id": port.ID, "label": port.Label,
			})
			writeSample(w, "keenetic_port_link_up", portLabels, port.Link)
			writeSample(w, "keenetic_port_speed_mbps", portLabels, port.Speed)
		}
	}
}

func writeMetricHeaders(w http.ResponseWriter) {
	metrics := [][3]string{
		{"keenetic_memory_free_kb", "Free memory in KB", "gauge"},
		{"keenetic_memory_total_kb", "Total memory in KB", "gauge"},
		{"keenetic_memory_cache_kb", "Memory cache in KB", "gauge"},
		{"keenetic_memory_buffers_kb", "Memory buffers in KB", "gauge"},
		{"keenetic_cpu_load", "CPU load", "gauge"},
		{"keenetic_uptime_seconds", "System uptime in seconds", "gauge"},
		{"keenetic_connections_free", "Free connections", "gauge"},
		{"keenetic_connections_total", "Total connections", "gauge"},
		{"keenetic_port_link_up", "Whether the Ethernet port link is up", "gauge"},
		{"keenetic_port_speed_mbps", "Negotiated Ethernet port speed in Mbps", "gauge"},
	}
	for _, metric := range metrics {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", metric[0], metric[1], metric[0], metric[2])
	}
}

func labels(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result strings.Builder
	result.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			result.WriteByte(',')
		}
		fmt.Fprintf(&result, `%s="%s"`, key, escapeLabel(values[key]))
	}
	result.WriteByte('}')
	return result.String()
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func writeSample(w http.ResponseWriter, name, metricLabels string, value float64) {
	fmt.Fprintf(w, "%s%s %s\n", name, metricLabels, strconv.FormatFloat(value, 'g', -1, 64))
}
