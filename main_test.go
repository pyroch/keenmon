package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestInterfaceURL(t *testing.T) {
	got := interfaceURL("https://router.example:8443/rci/show/system?unused=1")
	want := "https://router.example:8443/rci/show/interface/GigabitEthernet0"
	if got != want {
		t.Fatalf("interfaceURL() = %q, want %q", got, want)
	}
}

func TestLoadConfig(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`[{"name":"router","ip":"https://router/rci/show/system","username":"user","password":"pass"}]`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	configs, err := loadConfig(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Name != "router" {
		t.Fatalf("unexpected config: %#v", configs)
	}
}

func TestUpdateAndMetrics(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "pass" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/rci/show/system" {
			_, _ = w.Write([]byte(`{"memfree":10,"memtotal":20,"memcache":3,"membuffers":4,"cpuload":5,"uptime":6,"connfree":7,"conntotal":8}`))
			return
		}
		_, _ = w.Write([]byte(`{"interface-name":"GigabitEthernet0","port":{"1":{"id":"1","label":"LAN 1","link":"up","speed":"1000"},"2":{"link":"down"}}}`))
	}))
	defer server.Close()

	cfg := config{Name: `router "one"`, IP: server.URL + "/rci/show/system", Username: "user", Password: "pass"}
	exp := newExporter([]config{cfg})
	exp.update(context.Background(), cfg)

	recorder := httptest.NewRecorder()
	exp.metrics(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		`keenetic_memory_free_kb{device_ip="` + cfg.IP + `",device_name="router \"one\""} 10`,
		`keenetic_port_link_up{device_ip="` + cfg.IP + `",device_name="router \"one\"",interface="GigabitEthernet0",label="LAN 1",port="1",port_id="1"} 1`,
		`keenetic_port_speed_mbps{device_ip="` + cfg.IP + `",device_name="router \"one\"",interface="GigabitEthernet0",label="2",port="2",port_id=""} NaN`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics output does not contain %q:\n%s", expected, body)
		}
	}
}

func TestParsePorts(t *testing.T) {
	raw := map[string]json.RawMessage{
		"0": json.RawMessage(`{"id":0,"link":"UP","speed":100}`),
	}
	port := parsePorts(raw)["0"]
	if port.ID != "0" || port.Link != 1 || port.Speed != 100 {
		t.Fatalf("unexpected port: %#v", port)
	}
	if !math.IsNaN(numberValue(nil)) {
		t.Fatal("missing number must be NaN")
	}
}

func TestSystemResponseAcceptsNumbersAndNumericStrings(t *testing.T) {
	var response systemResponse
	err := json.Unmarshal([]byte(`{
		"memfree": 10,
		"memtotal": "20",
		"memcache": null,
		"membuffers": "4.5",
		"cpuload": "5",
		"uptime": "123456",
		"connfree": 7,
		"conntotal": "8"
	}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	fillMissingWithNaN(&response)

	if float64(*response.Uptime) != 123456 {
		t.Fatalf("uptime = %v, want 123456", *response.Uptime)
	}
	if float64(*response.MemoryTotal) != 20 {
		t.Fatalf("memory total = %v, want 20", *response.MemoryTotal)
	}
	if !math.IsNaN(float64(*response.MemoryCache)) {
		t.Fatalf("memory cache = %v, want NaN", *response.MemoryCache)
	}
}
