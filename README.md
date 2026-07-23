# Keenmon

Prometheus exporter for Keenetic routers via RCI, written in Go.

## Run with Docker Compose

1. Download `docker-compose.yml` and `config.json`.
2. Add one or more routers to `config.json`:

```json
[
  {
    "name": "Keenetic1",
    "ip": "https://192.168.1.1/rci/show/system",
    "username": "monitoring",
    "password": "change-me"
  }
]
```

3. Start the exporter:

```shell
docker compose up -d
```

4. Open <http://localhost:8758/metrics>.

## Run locally

Go 1.23 or newer is required.

```shell
go run .
```

Optional environment variables:

- `CONFIG_PATH` — configuration file path (default: `config.json`);
- `LISTEN_ADDRESS` — HTTP listen address (default: `:8758`).

The exporter uses only the Go standard library.

## Metrics

- `keenetic_memory_free_kb`
- `keenetic_memory_total_kb`
- `keenetic_memory_cache_kb`
- `keenetic_memory_buffers_kb`
- `keenetic_cpu_load`
- `keenetic_uptime_seconds`
- `keenetic_connections_free`
- `keenetic_connections_total`
- `keenetic_port_link_up`
- `keenetic_port_speed_mbps`
