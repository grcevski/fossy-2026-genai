package recipeagent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

//go:embed data/recipes.json
var recipesJSON []byte

type Ingredient struct {
	Name     string  `json:"name"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
}

type Recipe struct {
	Name        string       `json:"name"`
	Cuisine     string       `json:"cuisine"`
	Diets       []string     `json:"diets"`
	TimeMinutes int          `json:"time_minutes"`
	Servings    int          `json:"servings"`
	Ingredients []Ingredient `json:"ingredients"`
	Steps       []string     `json:"steps"`
	Tags        []string     `json:"tags"`
}

type RecipeTools struct {
	recipes []Recipe
}

var ToolSchemas = []map[string]any{
	{
		"type": "function",
		"function": map[string]any{
			"name":        "search_recipes",
			"description": "Search the bundled recipe catalog by ingredients, cuisine, dietary labels, and cooking time.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"available_ingredients": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"cuisine":               map[string]any{"type": "string"},
					"dietary_needs":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"max_time_minutes":      map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
					"limit":                 map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
				},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]any{
			"name":        "scale_recipe",
			"description": "Scale a bundled recipe's metric ingredient quantities to a requested serving count.",
			"parameters": map[string]any{
				"type":     "object",
				"required": []string{"recipe", "servings"},
				"properties": map[string]any{
					"recipe":   map[string]any{"type": "string"},
					"servings": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				},
			},
		},
	},
}

func NewRecipeTools() (*RecipeTools, error) {
	var recipes []Recipe
	if err := json.Unmarshal(recipesJSON, &recipes); err != nil {
		return nil, fmt.Errorf("load recipe catalog: %w", err)
	}
	return &RecipeTools{recipes: recipes}, nil
}

func (t *RecipeTools) Invoke(name string, arguments map[string]any) map[string]any {
	var result map[string]any
	var err error
	switch name {
	case "search_recipes":
		result, err = t.search(arguments)
	case "scale_recipe":
		result, err = t.scale(arguments)
	default:
		err = fmt.Errorf("unknown tool: %s", name)
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return result
}

type rankedRecipe struct {
	score   int
	recipe  Recipe
	missing []string
}

func (t *RecipeTools) search(arguments map[string]any) (map[string]any, error) {
	if err := rejectUnknown(arguments, "available_ingredients", "cuisine", "dietary_needs", "max_time_minutes", "limit"); err != nil {
		return nil, err
	}
	available, err := stringSlice(arguments["available_ingredients"], "available_ingredients", false)
	if err != nil {
		return nil, err
	}
	diets, err := stringSlice(arguments["dietary_needs"], "dietary_needs", false)
	if err != nil {
		return nil, err
	}
	cuisine, err := optionalString(arguments["cuisine"], "cuisine")
	if err != nil {
		return nil, err
	}
	maxTime, err := optionalInt(arguments["max_time_minutes"], "max_time_minutes", 1, 300)
	if err != nil {
		return nil, err
	}
	limit := 5
	if arguments["limit"] != nil {
		limitValue, err := requiredInt(arguments["limit"], "limit", 1, 10)
		if err != nil {
			return nil, err
		}
		limit = limitValue
	}

	availableSet := foldedSet(available)
	ranked := make([]rankedRecipe, 0)
	for _, recipe := range t.recipes {
		if cuisine != "" && !strings.Contains(strings.ToLower(recipe.Cuisine), strings.ToLower(cuisine)) {
			continue
		}
		if maxTime > 0 && recipe.TimeMinutes > maxTime {
			continue
		}
		dietSet := foldedSet(recipe.Diets)
		matchesDiet := true
		for _, diet := range diets {
			if _, ok := dietSet[strings.ToLower(diet)]; !ok {
				matchesDiet = false
				break
			}
		}
		if !matchesDiet {
			continue
		}

		score := 0
		missing := make([]string, 0)
		for _, ingredient := range recipe.Ingredients {
			matched := false
			for availableName := range availableSet {
				name := strings.ToLower(ingredient.Name)
				if strings.Contains(name, availableName) || strings.Contains(availableName, name) {
					score++
					matched = true
					break
				}
			}
			if len(available) > 0 && !matched {
				missing = append(missing, ingredient.Name)
			}
		}
		ranked = append(ranked, rankedRecipe{score: score, recipe: recipe, missing: missing})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].recipe.Name < ranked[j].recipe.Name
		}
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	matches := make([]map[string]any, 0, len(ranked))
	for _, item := range ranked {
		matches = append(matches, map[string]any{
			"name":                          item.recipe.Name,
			"cuisine":                       item.recipe.Cuisine,
			"diets":                         item.recipe.Diets,
			"time_minutes":                  item.recipe.TimeMinutes,
			"servings":                      item.recipe.Servings,
			"matched_available_ingredients": item.score,
			"missing_ingredients":           item.missing,
			"ingredients":                   item.recipe.Ingredients,
			"steps":                         item.recipe.Steps,
			"tags":                          item.recipe.Tags,
		})
	}
	return map[string]any{"ok": true, "count": len(matches), "recipes": matches}, nil
}

func (t *RecipeTools) scale(arguments map[string]any) (map[string]any, error) {
	if err := rejectUnknown(arguments, "recipe", "servings"); err != nil {
		return nil, err
	}
	name, err := requiredString(arguments["recipe"], "recipe")
	if err != nil {
		return nil, err
	}
	servings, err := requiredInt(arguments["servings"], "servings", 1, 50)
	if err != nil {
		return nil, err
	}
	var recipe *Recipe
	for i := range t.recipes {
		if strings.EqualFold(t.recipes[i].Name, name) {
			recipe = &t.recipes[i]
			break
		}
	}
	if recipe == nil {
		return nil, fmt.Errorf("recipe not found: %s", name)
	}
	factor := float64(servings) / float64(recipe.Servings)
	ingredients := make([]Ingredient, 0, len(recipe.Ingredients))
	for _, ingredient := range recipe.Ingredients {
		ingredient.Quantity = math.Round(ingredient.Quantity*factor*100) / 100
		ingredients = append(ingredients, ingredient)
	}
	return map[string]any{
		"ok":          true,
		"recipe":      recipe.Name,
		"servings":    servings,
		"ingredients": ingredients,
		"steps":       recipe.Steps,
		"notice":      "Check ingredient labels and personal allergy requirements.",
	}, nil
}

func SummarizeToolResult(result map[string]any) string {
	if ok, _ := result["ok"].(bool); !ok {
		return "error: " + fmt.Sprint(result["error"])
	}
	if recipes, ok := result["recipes"].([]map[string]any); ok {
		names := make([]string, 0, len(recipes))
		for _, recipe := range recipes {
			names = append(names, fmt.Sprint(recipe["name"]))
		}
		return fmt.Sprintf("%d match(es): %s", len(recipes), strings.Join(names, ", "))
	}
	if recipe, ok := result["recipe"].(string); ok {
		return fmt.Sprintf("scaled %s to %v serving(s)", recipe, result["servings"])
	}
	return "completed"
}

func requiredString(value any, name string) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", name)
	}
	return strings.TrimSpace(text), nil
}

func optionalString(value any, name string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return strings.TrimSpace(text), nil
}

func requiredInt(value any, name string, minimum, maximum int) (int, error) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) || int(number) < minimum || int(number) > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	return int(number), nil
}

func optionalInt(value any, name string, minimum, maximum int) (int, error) {
	if value == nil {
		return 0, nil
	}
	return requiredInt(value, name, minimum, maximum)
}

func stringSlice(value any, name string, required bool) ([]string, error) {
	if value == nil && !required {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of strings", name)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of strings", name)
		}
		if strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result, nil
}

func foldedSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = struct{}{}
	}
	return result
}

func rejectUnknown(arguments map[string]any, allowed ...string) error {
	allowedSet := foldedSet(allowed)
	for name := range arguments {
		if _, ok := allowedSet[strings.ToLower(name)]; !ok {
			return fmt.Errorf("unknown argument: %s", name)
		}
	}
	return nil
}
