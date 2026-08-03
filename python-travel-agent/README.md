# Python travel agent

An interactive, multi-turn travel planner backed by a static destination catalog. It calls Ollama directly with `urllib.request` and exposes two native tools:

- `search_destinations` filters by climate, interests, month, and daily CAD budget.
- `estimate_trip_cost` calculates an illustrative party total in CAD.

The interactive agent and FastAPI service both reuse the same agent core and local tools. From the repository root, use `make travel` for the CLI or `make serve-travel` for the service. Python dependencies are installed into `python-travel-agent/.venv` by `make install`.
