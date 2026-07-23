# Node.js entertainment agent

An interactive recommender backed by a static catalog of real books, movies, and shows. It uses built-in `fetch` to call Ollama and exposes two native tools:

- `search_titles` filters by medium, genre, mood, year, and approximate time commitment.
- `get_title_details` retrieves the complete local metadata for a shortlist item.

Run from the repository root:

```sh
make entertainment
make drive-entertainment
```

No availability data is included because streaming and retail catalogs change. The interactive agent remains Node.js. `make drive-entertainment` compiles the Go workload driver to `node-entertainment-agent/bin/entertainment-driver` and launches it. Run the CLI or that binary with `--help` for configuration flags. The driver embeds `data/scenarios.json` and the title catalog and runs independent multi-turn sessions until interrupted or until `--max-sessions` is reached.
