# Three local Ollama agents

This repository contains three deliberately small, standalone tool-using agents:

| Agent | Language | Local tools |
| --- | --- | --- |
| Travel planner | Python | Search destinations, estimate trip cost in CAD |
| Recipe helper | Go | Search recipes, scale metric ingredients |
| Entertainment guide | Node.js | Search titles, retrieve title details |

Every agent talks directly to Ollama's `/api/chat` endpoint with the language's standard HTTP library. There are no SDKs, external APIs, shared runtime packages, or third-party dependencies. The bundled catalogs are static demo data, not current professional advice.

## Requirements

- Ollama with `qwen3:8b` installed
- Python 3.11+
- Go 1.22+
- Node.js 22+
- Make (optional)

Start Ollama and pull the model if needed:

```sh
ollama serve
ollama pull qwen3:8b
```

## Interactive agents

```sh
make travel
make recipe
make entertainment
```

Each CLI supports `/help`, `/reset`, and `/quit`. Tool names, arguments, and compact result summaries are printed as the agent works.

## Workload drivers

All workload drivers are compiled Go programs. Each target builds its binary under the corresponding agent's `bin/` directory, then repeatedly creates short independent user sessions until interrupted:

```sh
make drive-travel
make drive-recipe
make drive-entertainment
make drive-all
```

Press `Ctrl-C` to stop. A driver finishes its current request and prints totals. Use `--help` on an individual driver for its flags.

## Configuration

Flags override environment variables. All agents recognize:

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `OLLAMA_HOST` | `http://localhost:11434` | Ollama base URL |
| `OLLAMA_MODEL` | `qwen3:8b` | Chat model |
| `OLLAMA_TIMEOUT` | `120` | HTTP timeout in seconds |
| `OLLAMA_TEMPERATURE` | `0.3` | Sampling temperature |

Workload drivers also recognize:

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `WORKERS` | `1` | Concurrent simulated users |
| `MIN_DELAY` / `MAX_DELAY` | `2` / `8` | Seconds between prompts |
| `SESSION_MIN_DELAY` / `SESSION_MAX_DELAY` | `5` / `15` | Seconds between sessions |
| `RANDOM_SEED` | random | Repeatable scenario selection |
| `MAX_SESSIONS` | unlimited | Stop after this many sessions |

Set `NO_COLOR` to disable interactive ANSI colors.
