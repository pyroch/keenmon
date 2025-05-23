import json
import requests
from threading import Thread
from wsgiref.simple_server import make_server
from prometheus_client import Gauge, generate_latest, CONTENT_TYPE_LATEST, CollectorRegistry, REGISTRY
from prometheus_client.core import GaugeMetricFamily
import time
import signal

# Настройки
EXPORTER_PORT = 8758

# Загрузка конфига
with open('config.json', "r", encoding="utf-8") as json_file:
    DEVICE_CONFIGS = json.load(json_file)

# Убираем стандартные метрики
from prometheus_client import PROCESS_COLLECTOR, PLATFORM_COLLECTOR, GC_COLLECTOR
REGISTRY.unregister(PROCESS_COLLECTOR)
REGISTRY.unregister(PLATFORM_COLLECTOR)
REGISTRY.unregister(GC_COLLECTOR)

# Регистр метрик
registry = CollectorRegistry()

# Определяем метрики
metrics = {
    "mem_free": Gauge("keenetic_memory_free_kb", "Free memory in KB", ["device_name", "device_ip"], registry=registry),
    "mem_total": Gauge("keenetic_memory_total_kb", "Total memory in KB", ["device_name", "device_ip"], registry=registry),
    "mem_cache": Gauge("keenetic_memory_cache_kb", "Memory cache in KB", ["device_name", "device_ip"], registry=registry),
    "mem_buffers": Gauge("keenetic_memory_buffers_kb", "Memory buffers in KB", ["device_name", "device_ip"], registry=registry),
    "cpuload": Gauge("keenetic_cpu_load", "CPU Load (1 min)", ["device_name", "device_ip"], registry=registry),
    "uptime": Gauge("keenetic_uptime_seconds", "System uptime in seconds", ["device_name", "device_ip"], registry=registry),
    "conn_free": Gauge("keenetic_connections_free", "Free connections", ["device_name", "device_ip"], registry=registry),
    "conn_total": Gauge("keenetic_connections_total", "Total connections", ["device_name", "device_ip"], registry=registry),
}

# Хранилище данных
device_metrics = {
    config["ip"]: {
        "name": config["name"],
        "mem_free": float("nan"),
        "mem_total": float("nan"),
        "mem_cache": float("nan"),
        "mem_buffers": float("nan"),
        "cpuload": float("nan"),
        "uptime": float("nan"),
        "conn_free": float("nan"),
        "conn_total": float("nan"),
    }
    for config in DEVICE_CONFIGS
}


def fetch_data(url, username, password):
    """Запрашиваем данные с устройства"""
    response = requests.get(url, auth=(username, password))
    if response.status_code == 200:
        return response.json()
    else:
        raise Exception(f"HTTP {response.status_code}: {response.text}")


def parse_data(data):
    """Парсим нужные поля"""
    return {
        "mem_free": data.get("memfree"),
        "mem_total": data.get("memtotal"),
        "mem_cache": data.get("memcache"),
        "mem_buffers": data.get("membuffers"),
        "cpuload": data.get("cpuload"),
        "uptime": data.get("uptime"),
        "conn_free": data.get("connfree"),
        "conn_total": data.get("conntotal")
    }


def update_device_metrics(config):
    """Фоновое обновление метрик для одного устройства"""
    device_url = config["ip"]
    while True:
        try:
            data = fetch_data(device_url, config["username"], config["password"])
            parsed = parse_data(data)
            device_metrics[device_url].update(parsed)
        except Exception as e:
            print(f"Error updating {device_url}: {e}")
            device_metrics[device_url] = {
                "name": config["name"],
                "mem_free": float("nan"),
                "mem_total": float("nan"),
                "mem_cache": float("nan"),
                "mem_buffers": float("nan"),
                "cpuload": float("nan"),
                "uptime": float("nan"),
                "conn_free": float("nan"),
                "conn_total": float("nan"),
            }
        time.sleep(15)


def start_background_updater():
    """Запускаем фоновые потоки для всех устройств"""
    for config in DEVICE_CONFIGS:
        Thread(target=update_device_metrics, args=(config,), daemon=True).start()


def metrics_app(environ, start_response):
    """WSGI-приложение для Prometheus"""
    if environ["PATH_INFO"] == "/metrics":
        for url, data in device_metrics.items():
            name = data["name"]
            ip = url
            metrics["mem_free"].labels(device_name=name, device_ip=ip).set(data["mem_free"])
            metrics["mem_total"].labels(device_name=name, device_ip=ip).set(data["mem_total"])
            metrics["mem_cache"].labels(device_name=name, device_ip=ip).set(data["mem_cache"])
            metrics["mem_buffers"].labels(device_name=name, device_ip=ip).set(data["mem_buffers"])
            metrics["cpuload"].labels(device_name=name, device_ip=ip).set(data["cpuload"])
            metrics["uptime"].labels(device_name=name, device_ip=ip).set(data["uptime"])
            metrics["conn_free"].labels(device_name=name, device_ip=ip).set(data["conn_free"])
            metrics["conn_total"].labels(device_name=name, device_ip=ip).set(data["conn_total"])

        output = generate_latest(registry)
        start_response("200 OK", [("Content-type", CONTENT_TYPE_LATEST)])
        return [output]

    start_response("404 Not Found", [("Content-type", "text/plain")])
    return [b"Not Found"]

def handle_signal(signum, frame):
    print("Received shutdown signal, exiting...", flush=True)
    exit(0)

if __name__ == "__main__":
    print(f"Starting server on http://localhost:{EXPORTER_PORT}/metrics", flush=True)
    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    start_background_updater()

    try:
        server = make_server("", EXPORTER_PORT, metrics_app)
        print("Serving on port", EXPORTER_PORT, flush=True)
        server.serve_forever()
    except KeyboardInterrupt:
        print("Shutting down server.", flush=True)
    finally:
        server.shutdown()
