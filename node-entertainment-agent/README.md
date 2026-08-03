# Node.js entertainment agent

An interactive recommender backed by a static catalog of real books, movies, and shows. It uses built-in `fetch` to call Ollama and exposes two native tools:

- `search_titles` filters by medium, genre, mood, year, and approximate time commitment.
- `get_title_details` retrieves the complete local metadata for a shortlist item.

No availability data is included because streaming and retail catalogs change. The interactive CLI and Express service reuse the same agent core and tools. From the repository root, use `make entertainment` for the CLI or `make serve-entertainment` for the service. Express is installed by `make install`.
