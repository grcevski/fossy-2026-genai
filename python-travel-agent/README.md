# Python travel agent

An interactive, multi-turn travel planner backed by a static destination catalog. It calls Ollama directly with `urllib.request` and exposes two native tools:

- `search_destinations` filters by climate, interests, month, and daily CAD budget.
- `estimate_trip_cost` calculates an illustrative party total in CAD.

Run from the repository root:

```sh
make travel
make drive-travel
```

The interactive agent remains Python. `make drive-travel` compiles the Go workload driver to `python-travel-agent/bin/travel-driver` and launches it. Run that binary with `--help` for all options. It embeds `data/scenarios.json` and the destination catalog, creates fresh short sessions, retries transient Ollama failures, and runs until interrupted unless `--max-sessions` is supplied.
