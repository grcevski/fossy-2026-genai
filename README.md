# Three local Ollama agents

This repository contains three deliberately small, standalone tool-using agents:

| Agent | Language | Local tools |
| --- | --- | --- |
| Travel planner | Python | Search destinations, estimate trip cost in CAD |
| Recipe helper | Go | Search recipes, scale metric ingredients |
| Entertainment guide | Node.js | Search titles, retrieve title details |

Each agent provides an interactive CLI and an HTTP service backed by the local Ollama `/api/chat` endpoint. The bundled catalogs are static demo data, not current professional advice.

## Requirements

- Ollama with `qwen3:8b` installed
- Python 3.11+
- Go 1.22+
- Node.js 22+
- k6 for workload generation
- Make (optional)

Start Ollama and pull the model if needed:

```sh
ollama serve
ollama pull qwen3:8b
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

Run services individually or together:

```sh
make serve-travel
make serve-recipe
make serve-entertainment
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
