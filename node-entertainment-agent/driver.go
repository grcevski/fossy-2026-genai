package entertainmentdriver

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

//go:embed data/titles.json
var titlesJSON []byte

//go:embed data/scenarios.json
var scenariosJSON []byte

const systemPrompt = `You are a friendly offline entertainment recommendation agent.
Use the supplied tools whenever you recommend or compare books, movies, or shows. The
catalog and metadata are static demo data. Do not claim current streaming, store, or
library availability; tell users to check their local providers. Ask at most one focused
clarification when a missing preference would materially change the result; otherwise
state reasonable assumptions. Explain why each recommendation fits and never invent
tool results.`

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

type title struct {
	Title               string   `json:"title"`
	Medium              string   `json:"medium"`
	Year                int      `json:"year"`
	Creator             string   `json:"creator"`
	Genres              []string `json:"genres"`
	Moods               []string `json:"moods"`
	TimeCommitmentHours float64  `json:"time_commitment_hours"`
	Length              string   `json:"length"`
	Summary             string   `json:"summary"`
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

type entertainmentTools struct {
	titles []title
}

type agent struct {
	config config
	client *http.Client
	tools  *entertainmentTools
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
			"name":        "search_titles",
			"description": "Search the bundled books, movies, and shows by medium, genre, mood, year, and approximate time commitment.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"medium":         map[string]any{"type": "string", "enum": []string{"book", "movie", "show"}},
					"genre":          map[string]any{"type": "string"},
					"mood":           map[string]any{"type": "string"},
					"minimum_year":   map[string]any{"type": "integer", "minimum": 1900, "maximum": 2100},
					"max_time_hours": map[string]any{"type": "number", "minimum": 0.1, "maximum": 500},
					"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
				},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]any{
			"name":        "get_title_details",
			"description": "Get full local-catalog details for one shortlisted title.",
			"parameters": map[string]any{
				"type":     "object",
				"required": []string{"title"},
				"properties": map[string]any{
					"title": map[string]any{"type": "string"},
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

	var titles []title
	if err := json.Unmarshal(titlesJSON, &titles); err != nil {
		fmt.Fprintf(os.Stderr, "error: load titles: %v\n", err)
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
		"[driver] role=entertainment model=%s workers=%d seed=%d max_sessions=%s",
		configuration.Model,
		configuration.Workers,
		seed,
		maximumLabel(configuration.MaxSessions),
	))
	stats := &statistics{}
	tools := &entertainmentTools{titles: titles}
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
	tools *entertainmentTools,
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

func (t *entertainmentTools) invoke(name string, arguments map[string]any) map[string]any {
	var result map[string]any
	var err error
	switch name {
	case "search_titles":
		result, err = t.search(arguments)
	case "get_title_details":
		result, err = t.details(arguments)
	default:
		err = fmt.Errorf("unknown tool: %s", name)
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return result
}

func (t *entertainmentTools) search(arguments map[string]any) (map[string]any, error) {
	if err := rejectUnknown(arguments, "medium", "genre", "mood", "minimum_year", "max_time_hours", "limit"); err != nil {
		return nil, err
	}
	medium, err := optionalString(arguments["medium"], "medium")
	if err != nil {
		return nil, err
	}
	if medium != "" && medium != "book" && medium != "movie" && medium != "show" {
		return nil, errors.New("medium must be book, movie, or show")
	}
	genre, err := optionalString(arguments["genre"], "genre")
	if err != nil {
		return nil, err
	}
	mood, err := optionalString(arguments["mood"], "mood")
	if err != nil {
		return nil, err
	}
	minimumYear := 0
	if arguments["minimum_year"] != nil {
		minimumYear, err = requiredInt(arguments["minimum_year"], "minimum_year", 1900, 2100)
		if err != nil {
			return nil, err
		}
	}
	maxTime, err := optionalNumber(arguments["max_time_hours"], "max_time_hours", 0.1, 500)
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

	matches := make([]title, 0)
	for _, item := range t.titles {
		if medium != "" && !strings.EqualFold(item.Medium, medium) {
			continue
		}
		if genre != "" && !containsSubstringFolded(item.Genres, genre) {
			continue
		}
		if mood != "" && !containsSubstringFolded(item.Moods, mood) {
			continue
		}
		if minimumYear > 0 && item.Year < minimumYear {
			continue
		}
		if maxTime != nil && item.TimeCommitmentHours > *maxTime {
			continue
		}
		matches = append(matches, item)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Year == matches[j].Year {
			return matches[i].Title < matches[j].Title
		}
		return matches[i].Year > matches[j].Year
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	results := make([]map[string]any, 0, len(matches))
	for _, item := range matches {
		results = append(results, map[string]any{
			"title":                 item.Title,
			"medium":                item.Medium,
			"year":                  item.Year,
			"genres":                item.Genres,
			"moods":                 item.Moods,
			"time_commitment_hours": item.TimeCommitmentHours,
			"length":                item.Length,
			"summary":               item.Summary,
		})
	}
	return map[string]any{"ok": true, "count": len(results), "titles": results}, nil
}

func (t *entertainmentTools) details(arguments map[string]any) (map[string]any, error) {
	if err := rejectUnknown(arguments, "title"); err != nil {
		return nil, err
	}
	name, err := requiredString(arguments["title"], "title")
	if err != nil {
		return nil, err
	}
	var exact *title
	partial := make([]*title, 0)
	for index := range t.titles {
		item := &t.titles[index]
		if strings.EqualFold(item.Title, name) {
			exact = item
			break
		}
		if strings.Contains(strings.ToLower(item.Title), strings.ToLower(name)) {
			partial = append(partial, item)
		}
	}
	selected := exact
	if selected == nil && len(partial) == 1 {
		selected = partial[0]
	}
	if selected == nil {
		return nil, fmt.Errorf("title not found or ambiguous: %s", name)
	}
	return map[string]any{
		"ok":                    true,
		"title":                 selected.Title,
		"medium":                selected.Medium,
		"year":                  selected.Year,
		"creator":               selected.Creator,
		"genres":                selected.Genres,
		"moods":                 selected.Moods,
		"time_commitment_hours": selected.TimeCommitmentHours,
		"length":                selected.Length,
		"summary":               selected.Summary,
		"notice":                "Catalog metadata is static; check a local provider or bookseller for availability.",
	}, nil
}

func summarizeToolResult(result map[string]any) string {
	if ok, _ := result["ok"].(bool); !ok {
		return "error: " + fmt.Sprint(result["error"])
	}
	if matches, ok := result["titles"].([]map[string]any); ok {
		names := make([]string, 0, len(matches))
		for _, item := range matches {
			names = append(names, fmt.Sprint(item["title"]))
		}
		return fmt.Sprintf("%d match(es): %s", len(matches), strings.Join(names, ", "))
	}
	if selected, ok := result["title"].(string); ok {
		return "details for " + selected
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
	flags := flag.NewFlagSet("entertainment-driver", flag.ContinueOnError)
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
	return strings.ToLower(strings.TrimSpace(text)), nil
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
		return nil, fmt.Errorf("%s must be a number from %.1f to %.1f", name, minimum, maximum)
	}
	return &number, nil
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

func containsSubstringFolded(values []string, value string) bool {
	needle := strings.ToLower(value)
	for _, candidate := range values {
		if strings.Contains(strings.ToLower(candidate), needle) {
			return true
		}
	}
	return false
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
