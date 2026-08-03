# Three local Ollama agents

This repository contains three deliberately small, standalone tool-using agents:

| Agent | Language | Local tools | Model
| --- | --- | --- | --- |
| Travel planner | Python | Search destinations, estimate trip cost in CAD | qwen:3b
| Recipe helper | Go | Search recipes, scale metric ingredients | mistral:latest
| Entertainment guide | Node.js | Search titles, retrieve title details | qwen:3b

Each agent provides an interactive CLI and an HTTP service backed by the local Ollama `/api/chat` endpoint. The bundled catalogs are static demo data, not current professional advice.

## Requirements

- Ollama with `qwen3:8b` and `mistral:latest` installed
- Python 3.11+
- Go 1.22+
- Node.js 22+
- k6 for workload generation
- Make (optional)

Start Ollama and pull the model if needed:

```sh
ollama serve
ollama pull qwen3:8b
ollama pull mistral:latest
```

Install FastAPI/Uvicorn and Express, then build the Go programs:

```sh
make all
```

## Interactive agents

```sh
make travel
make recipe
make entertainment
```

Each CLI supports `/help`, `/reset`, and `/quit`.

## HTTP services

Run services individually:

```sh
make serve-travel
make serve-recipe
make serve-entertainment
```

Or together:

```sh
make serve-all
```

`make run-all` is an alias for `make serve-all`. Services bind to `0.0.0.0` on ports `8081`, `8082`, and `8083` by default. Stop them with `Ctrl-C`.

## Workload

With the services running in another terminal:

```sh
make load
```

The k6 workload defaults to one virtual user per service for one minute, with a random 2–8 second pause between simulations. Configure it with `VUS`, `DURATION`, `MIN_DELAY`, `MAX_DELAY`, the role-specific `*_VUS` and `*_DURATION` variables, or `TRAVEL_URL`, `RECIPE_URL`, and `ENTERTAINMENT_URL`.

## Service configuration

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama base URL |
| `OLLAMA_MODEL` | `qwen3:8b` | Chat model |
| `OLLAMA_TIMEOUT` | `120` | Per-call Ollama timeout in seconds |
| `OLLAMA_TEMPERATURE` | `0.3` | Sampling temperature |
| `HTTP_HOST` | `0.0.0.0` | Service bind address |
| `HTTP_PORT` | role-specific | Service port |
| `SESSION_TTL` | `1800` | Idle session expiry in seconds |
| `MAX_SESSIONS` | `100` | In-memory session cap |
| `MAX_CONCURRENT_REQUESTS` | `4` | Concurrent Ollama-backed requests |
| `CHAT_TIMEOUT` | `300` | Overall chat deadline in seconds |
| `SIMULATE_TIMEOUT` | `900` | Overall simulation deadline in seconds |

The services have no authentication and are reachable from the network when bound to `0.0.0.0`.

# Running local observability stack

The easiest way to get started is by getting an OSS local observability stack like the Grafana LGTM (Loki = Logs, Grafana = UI, Tempo = Traces, Mimir = Metrics) stack. There's a packaged version of these products configured together with the OpenTelemetry Collector, as a single [Docker image](https://github.com/grafana/docker-otel-lgtm).

Easiest way to run it is by running the following commands:
```sh
git checkout git@github.com:grafana/docker-otel-lgtm.git
cd docker-otel-lgtm
./run-lgtm.sh
```

The Grafana instance shipped in this image contains a few dashboards, for example RED (Request/Error/Duration) metrics. I've included an additional sample dashboard in the [dashboards](./dashboards/) folder that is specifically designed for the OpenTelemetry GenAI metrics. Once you launch the LGTM stack, you can import this dashboard JSON file into your Grafana dashboards.

# Capturing Telemetry with OpenTelemetry eBPF Instrumentation

In the [obi](./obi/) folder I've included the sample configuration (and a run script) I used to instrument the three services built here. The script shows how to enable various features in [OpenTelemetry eBPF Instrumentation](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation) to successfully capture GenAI signals.