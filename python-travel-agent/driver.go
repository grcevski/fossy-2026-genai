package traveldriver

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed data/destinations.json
var destinationsJSON []byte

//go:embed data/scenarios.json
var scenariosJSON []byte

const systemPrompt = `You are a friendly offline travel-planning agent.
Use the supplied tools whenever you recommend a destination or estimate a budget.
The catalog is static demo data. Clearly label costs as illustrative CAD estimates and
never claim current prices, availability, visa rules, safety conditions, or professional
advice. Ask at most one focused clarification when missing information would materially
change the result; otherwise state reasonable assumptions. After tool use, give concise,
practical recommendations and a possible itinerary. Never invent tool results.`

const maxToolRounds = 5
const maxHistoryTurns = 10

type config struct {
	OllamaHost       string
	Model            string
	Timeout          time.Duration
	Temperature      float64
	Workers          int
	MinDelay         time.Duration
	MaxDelay         time.Duration
	SessionMinDelay  time.Duration
	SessionMaxDelay  time.Duration
	RandomSeed       int64
	RandomSeedWasSet bool
	MaxSessions      int
}

type destination struct {
	Name         string             `json:"name"`
	Country      string             `json:"country"`
	Climate      string             `json:"climate"`
	Interests    []string           `json:"interests"`
	BestMonths   []string           `json:"best_months"`
	DailyCostCAD map[string]float64 `json:"daily_cost_cad"`
	Highlights   []string           `json:"highlights"`
	Summary      string             `json:"summary"`
}

type scenario struct {
	Name    string   `json:"name"`
	Prompts []string `json:"prompts"`
}

type toolFunction struct {
	Index     int            `json:"index,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolCall struct {
	Type     string       `json:"type,omitempty"`
	Function toolFunction `json:"function"`
}

type message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []message        `json:"messages"`
	Tools    []map[string]any `json:"tools"`
	Stream   bool             `json:"stream"`
	Think    bool             `json:"think"`
	Options  map[string]any   `json:"options"`
}

type chatResponse struct {
	Message message `json:"message"`
}

type travelTools struct {
	destinations []destination
}

type agent struct {
	config config
	client *http.Client
	tools  *travelTools
	turns  [][]message
}

type statistics struct {
	mu                sync.Mutex
	sessionsStarted   int
	sessionsCompleted int
	turnsCompleted    int
	failures          int
}

var printMu sync.Mutex

var toolSchemas = []map[string]any{
	{
		"type": "function",
		"function": map[string]any{
			"name":        "search_destinations",
			"description": "Search the bundled destination catalog by climate, interests, travel month, and indicative daily budget in CAD.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"climate":               map[string]any{"type": "string"},
					"interests":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"travel_month":          map[string]any{"type": "string", "description": "English month name, for example September"},
					"budget_per_person_cad": map[string]any{"type": "number", "description": "Maximum daily ground budget per person in CAD"},
					"limit":                 map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
				},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]any{
			"name":        "estimate_trip_cost",
			"description": "Estimate a trip cost from bundled daily costs. All values are illustrative CAD amounts, not live prices.",
			"parameters": map[string]any{
				"type":     "object",
				"required": []string{"destination", "days", "travelers"},
				"properties": map[string]any{
					"destination":        map[string]any{"type": "string"},
					"days":               map[string]any{"type": "integer", "minimum": 1, "maximum": 90},
					"travelers":          map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
					"transportation_cad": map[string]any{"type": "number", "minimum": 0, "description": "Total round-trip transportation for the party"},
				},
			},
		},
	},
}

func Main(args []string) int {
	configuration, err := parseConfig(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	var destinations []destination
	if err := json.Unmarshal(destinationsJSON, &destinations); err != nil {
		fmt.Fprintf(os.Stderr, "error: load destinations: %v\n", err)
		return 2
	}
	var scenarios []scenario
	if err := json.Unmarshal(scenariosJSON, &scenarios); err != nil {
		fmt.Fprintf(os.Stderr, "error: load scenarios: %v\n", err)
		return 2
	}

	seed := configuration.RandomSeed
	if !configuration.RandomSeedWasSet && seed == 0 {
		seed = time.Now().UnixNano()
	}
	var stopping atomic.Bool
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		logLine("[driver] shutdown requested; finishing current turn")
		stopping.Store(true)
	}()

	logLine(fmt.Sprintf(
		"[driver] role=travel model=%s workers=%d seed=%d max_sessions=%s",
		configuration.Model,
		configuration.Workers,
		seed,
		maximumLabel(configuration.MaxSessions),
	))
	stats := &statistics{}
	tools := &travelTools{destinations: destinations}
	var workers sync.WaitGroup
	for workerID := 1; workerID <= configuration.Workers; workerID++ {
		workers.Add(1)
		go runWorker(workerID, seed, configuration, scenarios, tools, stats, &stopping, &workers)
	}
	workers.Wait()
	started, completed, turns, failures := stats.snapshot()
	logLine(fmt.Sprintf(
		"[final] sessions_started=%d sessions_completed=%d turns=%d failures=%d",
		started,
		completed,
		turns,
		failures,
	))
	return 0
}

func runWorker(
	workerID int,
	seed int64,
	configuration config,
	scenarios []scenario,
	tools *travelTools,
	stats *statistics,
	stopping *atomic.Bool,
	workers *sync.WaitGroup,
) {
	defer workers.Done()
	rng := rand.New(rand.NewSource(seed + int64(workerID)))
	for !stopping.Load() {
		sessionID, ok := stats.claimSession(configuration.MaxSessions)
		if !ok {
			return
		}
		selected := scenarios[rng.Intn(len(scenarios))]
		sessionAgent := &agent{config: configuration, client: &http.Client{}, tools: tools}
		for promptIndex, prompt := range selected.Prompts {
			if stopping.Load() {
				break
			}
			prefix := fmt.Sprintf("[worker=%d session=%d]", workerID, sessionID)
			logLine(fmt.Sprintf("%s user> %s", prefix, prompt))
			backoff := time.Second
			for !stopping.Load() {
				started := time.Now()
				answer, err := sessionAgent.ask(context.Background(), prompt, func(name string, arguments, result map[string]any) {
					compact, _ := json.Marshal(arguments)
					logLine(fmt.Sprintf("%s [tool] %s %s -> %s", prefix, name, compact, summarizeToolResult(result)))
				})
				if err == nil {
					stats.addTurn()
					logLine(fmt.Sprintf("%s agent> %s [duration=%.2fs]", prefix, answer, time.Since(started).Seconds()))
					break
				}
				stats.addFailure()
				logLine(fmt.Sprintf("%s error: %v; retrying in %s", prefix, err, backoff))
				if waitFor(stopping, backoff) {
					break
				}
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
			if stopping.Load() {
				break
			}
			if promptIndex+1 < len(selected.Prompts) && waitFor(stopping, randomDuration(rng, configuration.MinDelay, configuration.MaxDelay)) {
				break
			}
		}
		sessionsCompleted, turnsCompleted, failures := stats.finishSession()
		logLine(fmt.Sprintf("[summary] sessions=%d turns=%d failures=%d", sessionsCompleted, turnsCompleted, failures))
		if configuration.MaxSessions > 0 && stats.allSessionsClaimed(configuration.MaxSessions) {
			return
		}
		if !stopping.Load() {
			waitFor(stopping, randomDuration(rng, configuration.SessionMinDelay, configuration.SessionMaxDelay))
		}
	}
}

func (a *agent) ask(ctx context.Context, userText string, onTool func(string, map[string]any, map[string]any)) (string, error) {
	current := []message{{Role: "user", Content: userText}}
	for round := 0; round < maxToolRounds; round++ {
		messages := []message{{Role: "system", Content: systemPrompt}}
		for _, turn := range a.turns {
			messages = append(messages, turn...)
		}
		messages = append(messages, current...)
		assistant, err := a.chat(ctx, messages)
		if err != nil {
			return "", err
		}
		assistant.Role = "assistant"
		current = append(current, assistant)
		if len(assistant.ToolCalls) == 0 {
			answer := strings.TrimSpace(assistant.Content)
			if answer == "" {
				answer = "I could not produce a response for that request."
			}
			a.remember(current)
			return answer, nil
		}
		for _, call := range assistant.ToolCalls {
			name := call.Function.Name
			arguments := call.Function.Arguments
			if name == "" {
				name = "invalid_tool_call"
			}
			if arguments == nil {
				arguments = map[string]any{}
			}
			result := a.tools.invoke(name, arguments)
			if onTool != nil {
				onTool(name, arguments, result)
			}
			encoded, _ := json.Marshal(result)
			current = append(current, message{Role: "tool", ToolName: name, Content: string(encoded)})
		}
	}
	answer := "I stopped after five tool rounds to avoid an accidental loop."
	current = append(current, message{Role: "assistant", Content: answer})
	a.remember(current)
	return answer, nil
}

func (a *agent) remember(turn []message) {
	a.turns = append(a.turns, turn)
	if len(a.turns) > maxHistoryTurns {
		a.turns = a.turns[len(a.turns)-maxHistoryTurns:]
	}
}

func (a *agent) chat(ctx context.Context, messages []message) (message, error) {
	payload := chatRequest{
		Model:    a.config.Model,
		Messages: messages,
		Tools:    toolSchemas,
		Stream:   false,
		Think:    false,
		Options:  map[string]any{"temperature": a.config.Temperature},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return message{}, fmt.Errorf("encode Ollama request: %w", err)
	}
	requestContext, cancel := context.WithTimeout(ctx, a.config.Timeout)
	defer cancel()
	url := strings.TrimRight(a.config.OllamaHost, "/") + "/api/chat"
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return message{}, fmt.Errorf("create Ollama request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return message{}, fmt.Errorf("cannot reach Ollama: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return message{}, fmt.Errorf("read Ollama response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return message{}, fmt.Errorf("Ollama returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result chatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return message{}, fmt.Errorf("decode Ollama response: %w", err)
	}
	if result.Message.Role == "" && result.Message.Content == "" && len(result.Message.ToolCalls) == 0 {
		return message{}, errors.New("Ollama returned an invalid chat response")
	}
	return result.Message, nil
}

func (t *travelTools) invoke(name string, arguments map[string]any) map[string]any {
	var result map[string]any
	var err error
	switch name {
	case "search_destinations":
		result, err = t.search(arguments)
	case "estimate_trip_cost":
		result, err = t.estimate(arguments)
	default:
		err = fmt.Errorf("unknown tool: %s", name)
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return result
}

type rankedDestination struct {
	score       int
	destination destination
}

func (t *travelTools) search(arguments map[string]any) (map[string]any, error) {
	if err := rejectUnknown(arguments, "climate", "interests", "travel_month", "budget_per_person_cad", "limit"); err != nil {
		return nil, err
	}
	climate, err := optionalString(arguments["climate"], "climate")
	if err != nil {
		return nil, err
	}
	month, err := optionalString(arguments["travel_month"], "travel_month")
	if err != nil {
		return nil, err
	}
	interests, err := stringSlice(arguments["interests"], "interests")
	if err != nil {
		return nil, err
	}
	budget, err := optionalNumber(arguments["budget_per_person_cad"], "budget_per_person_cad", 0, math.MaxFloat64)
	if err != nil {
		return nil, err
	}
	limit := 5
	if arguments["limit"] != nil {
		limit, err = requiredInt(arguments["limit"], "limit", 1, 10)
		if err != nil {
			return nil, err
		}
	}

	ranked := make([]rankedDestination, 0)
	for _, item := range t.destinations {
		daily := sumCosts(item.DailyCostCAD)
		if climate != "" && !strings.Contains(strings.ToLower(item.Climate), strings.ToLower(climate)) {
			continue
		}
		if month != "" && !containsFolded(item.BestMonths, month) {
			continue
		}
		if budget != nil && daily > *budget {
			continue
		}
		score := 0
		for _, interest := range interests {
			if containsFolded(item.Interests, interest) {
				score++
			}
		}
		if len(interests) > 0 && score == 0 {
			continue
		}
		ranked = append(ranked, rankedDestination{score: score, destination: item})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].destination.Name < ranked[j].destination.Name
		}
		return ranked[i].score > ranked[j].score
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	matches := make([]map[string]any, 0, len(ranked))
	for _, item := range ranked {
		matches = append(matches, map[string]any{
			"name":                      item.destination.Name,
			"country":                   item.destination.Country,
			"climate":                   item.destination.Climate,
			"matched_interests":         item.score,
			"interests":                 item.destination.Interests,
			"best_months":               item.destination.BestMonths,
			"daily_cost_per_person_cad": sumCosts(item.destination.DailyCostCAD),
			"highlights":                item.destination.Highlights,
			"summary":                   item.destination.Summary,
		})
	}
	return map[string]any{"ok": true, "count": len(matches), "destinations": matches}, nil
}

func (t *travelTools) estimate(arguments map[string]any) (map[string]any, error) {
	if err := rejectUnknown(arguments, "destination", "days", "travelers", "transportation_cad"); err != nil {
		return nil, err
	}
	name, err := requiredString(arguments["destination"], "destination")
	if err != nil {
		return nil, err
	}
	days, err := requiredInt(arguments["days"], "days", 1, 90)
	if err != nil {
		return nil, err
	}
	travelers, err := requiredInt(arguments["travelers"], "travelers", 1, 20)
	if err != nil {
		return nil, err
	}
	transportation, err := optionalNumber(arguments["transportation_cad"], "transportation_cad", 0, math.MaxFloat64)
	if err != nil {
		return nil, err
	}
	var selected *destination
	for index := range t.destinations {
		if strings.EqualFold(t.destinations[index].Name, name) {
			selected = &t.destinations[index]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("destination not found: %s", name)
	}
	breakdown := make(map[string]float64, len(selected.DailyCostCAD)+1)
	for category, amount := range selected.DailyCostCAD {
		breakdown[category] = roundMoney(amount * float64(days) * float64(travelers))
	}
	if transportation != nil {
		breakdown["round_trip_transportation"] = roundMoney(*transportation)
	} else {
		breakdown["round_trip_transportation"] = 0
	}
	return map[string]any{
		"ok":            true,
		"destination":   selected.Name,
		"currency":      "CAD",
		"days":          days,
		"travelers":     travelers,
		"breakdown_cad": breakdown,
		"total_cad":     roundMoney(sumCosts(breakdown)),
		"notice":        "Illustrative catalog estimate; verify current prices.",
	}, nil
}

func summarizeToolResult(result map[string]any) string {
	if ok, _ := result["ok"].(bool); !ok {
		return "error: " + fmt.Sprint(result["error"])
	}
	if matches, ok := result["destinations"].([]map[string]any); ok {
		names := make([]string, 0, len(matches))
		for _, item := range matches {
			names = append(names, fmt.Sprint(item["name"]))
		}
		return fmt.Sprintf("%d match(es): %s", len(matches), strings.Join(names, ", "))
	}
	if total, ok := result["total_cad"].(float64); ok {
		return fmt.Sprintf("estimated total CAD %.2f", total)
	}
	return "completed"
}

func parseConfig(args []string) (config, error) {
	timeout, err := envFloat("OLLAMA_TIMEOUT", 120)
	if err != nil {
		return config{}, err
	}
	temperature, err := envFloat("OLLAMA_TEMPERATURE", 0.3)
	if err != nil {
		return config{}, err
	}
	workers, err := envInt("WORKERS", 1)
	if err != nil {
		return config{}, err
	}
	minDelay, err := envFloat("MIN_DELAY", 2)
	if err != nil {
		return config{}, err
	}
	maxDelay, err := envFloat("MAX_DELAY", 8)
	if err != nil {
		return config{}, err
	}
	sessionMinDelay, err := envFloat("SESSION_MIN_DELAY", 5)
	if err != nil {
		return config{}, err
	}
	sessionMaxDelay, err := envFloat("SESSION_MAX_DELAY", 15)
	if err != nil {
		return config{}, err
	}
	maxSessions, err := envInt("MAX_SESSIONS", 0)
	if err != nil {
		return config{}, err
	}
	seedText, seedWasSet := os.LookupEnv("RANDOM_SEED")
	var seed int64
	if seedWasSet {
		seed, err = strconv.ParseInt(seedText, 10, 64)
		if err != nil {
			return config{}, errors.New("RANDOM_SEED must be an integer")
		}
	}
	configuration := config{
		OllamaHost:       envString("OLLAMA_HOST", "http://localhost:11434"),
		Model:            envString("OLLAMA_MODEL", "qwen3:8b"),
		Timeout:          seconds(timeout),
		Temperature:      temperature,
		Workers:          workers,
		MinDelay:         seconds(minDelay),
		MaxDelay:         seconds(maxDelay),
		SessionMinDelay:  seconds(sessionMinDelay),
		SessionMaxDelay:  seconds(sessionMaxDelay),
		RandomSeed:       seed,
		RandomSeedWasSet: seedWasSet,
		MaxSessions:      maxSessions,
	}
	flags := flag.NewFlagSet("travel-driver", flag.ContinueOnError)
	flags.StringVar(&configuration.OllamaHost, "ollama-host", configuration.OllamaHost, "Ollama base URL")
	flags.StringVar(&configuration.Model, "model", configuration.Model, "Ollama model")
	flags.Var(secondsValue{target: &configuration.Timeout}, "timeout", "HTTP request timeout in seconds")
	flags.Float64Var(&configuration.Temperature, "temperature", configuration.Temperature, "sampling temperature")
	flags.IntVar(&configuration.Workers, "workers", configuration.Workers, "concurrent simulated users")
	flags.Var(secondsValue{target: &configuration.MinDelay}, "min-delay", "minimum delay between prompts in seconds")
	flags.Var(secondsValue{target: &configuration.MaxDelay}, "max-delay", "maximum delay between prompts in seconds")
	flags.Var(secondsValue{target: &configuration.SessionMinDelay}, "session-min-delay", "minimum delay between sessions in seconds")
	flags.Var(secondsValue{target: &configuration.SessionMaxDelay}, "session-max-delay", "maximum delay between sessions in seconds")
	flags.Int64Var(&configuration.RandomSeed, "random-seed", configuration.RandomSeed, "random seed; zero chooses one when unset")
	flags.IntVar(&configuration.MaxSessions, "max-sessions", configuration.MaxSessions, "maximum total sessions; zero runs forever")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	configuration.OllamaHost = strings.TrimRight(configuration.OllamaHost, "/")
	if !strings.HasPrefix(configuration.OllamaHost, "http://") && !strings.HasPrefix(configuration.OllamaHost, "https://") {
		return config{}, errors.New("OLLAMA_HOST must start with http:// or https://")
	}
	if configuration.Timeout <= 0 {
		return config{}, errors.New("OLLAMA_TIMEOUT must be greater than zero")
	}
	if configuration.Temperature < 0 {
		return config{}, errors.New("OLLAMA_TEMPERATURE cannot be negative")
	}
	if configuration.Workers < 1 {
		return config{}, errors.New("WORKERS must be at least 1")
	}
	if configuration.MaxSessions < 0 {
		return config{}, errors.New("MAX_SESSIONS cannot be negative")
	}
	if configuration.MinDelay < 0 || configuration.MaxDelay < configuration.MinDelay {
		return config{}, errors.New("invalid prompt delay range")
	}
	if configuration.SessionMinDelay < 0 || configuration.SessionMaxDelay < configuration.SessionMinDelay {
		return config{}, errors.New("invalid session delay range")
	}
	return configuration, nil
}

type secondsValue struct {
	target *time.Duration
}

func (value secondsValue) String() string {
	if value.target == nil {
		return "0"
	}
	return strconv.FormatFloat(value.target.Seconds(), 'f', -1, 64)
}

func (value secondsValue) Set(raw string) error {
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return errors.New("must be a number of seconds")
	}
	*value.target = seconds(parsed)
	return nil
}

func envString(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func envFloat(name string, fallback float64) (float64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", name)
	}
	return parsed, nil
}

func envInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
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

func optionalNumber(value any, name string, minimum, maximum float64) (*float64, error) {
	if value == nil {
		return nil, nil
	}
	number, ok := value.(float64)
	if !ok || number < minimum || number > maximum {
		return nil, fmt.Errorf("%s must be a number from %.2f to %.2f", name, minimum, maximum)
	}
	return &number, nil
}

func stringSlice(value any, name string) ([]string, error) {
	if value == nil {
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

func rejectUnknown(arguments map[string]any, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name := range arguments {
		if _, ok := allowedSet[name]; !ok {
			return fmt.Errorf("unknown argument: %s", name)
		}
	}
	return nil
}

func containsFolded(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func sumCosts(costs map[string]float64) float64 {
	total := 0.0
	for _, amount := range costs {
		total += amount
	}
	return total
}

func roundMoney(value float64) float64 {
	return math.Round(value*100) / 100
}

func (s *statistics) claimSession(maximum int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maximum > 0 && s.sessionsStarted >= maximum {
		return 0, false
	}
	s.sessionsStarted++
	return s.sessionsStarted, true
}

func (s *statistics) addTurn() {
	s.mu.Lock()
	s.turnsCompleted++
	s.mu.Unlock()
}

func (s *statistics) addFailure() {
	s.mu.Lock()
	s.failures++
	s.mu.Unlock()
}

func (s *statistics) finishSession() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionsCompleted++
	return s.sessionsCompleted, s.turnsCompleted, s.failures
}

func (s *statistics) snapshot() (int, int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionsStarted, s.sessionsCompleted, s.turnsCompleted, s.failures
}

func (s *statistics) allSessionsClaimed(maximum int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionsStarted >= maximum
}

func logLine(text string) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), text)
}

func waitFor(stopping *atomic.Bool, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if stopping.Load() {
			return true
		}
		select {
		case <-timer.C:
			return stopping.Load()
		case <-ticker.C:
		}
	}
}

func randomDuration(rng *rand.Rand, minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	return minimum + time.Duration(rng.Int63n(int64(maximum-minimum)+1))
}

func maximumLabel(maximum int) string {
	if maximum == 0 {
		return "unlimited"
	}
	return strconv.Itoa(maximum)
}
