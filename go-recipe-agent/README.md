# Go recipe agent

An interactive recipe helper using a bundled catalog and direct `net/http` calls to Ollama. It exposes two native tools:

- `search_recipes` filters by ingredients, cuisine, dietary labels, and cooking time.
- `scale_recipe` adjusts metric quantities for a requested number of servings.

The catalogs and simulation scenarios are embedded into the Go binaries. The data is illustrative and cannot guarantee allergen safety; users must verify ingredient labels. From the repository root, use `make recipe` for the CLI or `make serve-recipe` for the HTTP service.
