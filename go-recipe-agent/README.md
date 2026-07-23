# Go recipe agent

An interactive recipe helper using a bundled catalog and direct `net/http` calls to Ollama. It exposes two native tools:

- `search_recipes` filters by ingredients, cuisine, dietary labels, and cooking time.
- `scale_recipe` adjusts metric quantities for a requested number of servings.

Run from the repository root:

```sh
make recipe
make drive-recipe
```

The catalog is embedded into the Go binary. The data is illustrative and cannot guarantee allergen safety; users must verify ingredient labels. Run either Go command with `--help` to see configuration flags. The driver runs independent multi-turn scenarios until interrupted or until `--max-sessions` is reached.
