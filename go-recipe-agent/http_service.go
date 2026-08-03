package recipeagent

import (
	"context"
	cryptorand "crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxBodyBytes = 32 * 1024
const maxMessageChars = 8_000

//go:embed data/scenarios.json
var scenariosJSON []byte

type HTTPConfig struct {
	Host                  string
	Port                  int
	SessionTTL            time.Duration
	MaxSessions           int
	MaxConcurrentRequests int
	ChatTimeout           time.Duration
	SimulateTimeout       time.Duration
}

type scenario struct {
	Name    string   `json:"name"`
	Prompts []string `json:"prompts"`
}

type toolTrace struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Summary   string         `json:"summary"`
}

type transcriptTurn struct {
	Message string      `json:"message"`
	Answer  string      `json:"answer"`
	Tools   []toolTrace `json:"tools"`
}

type chatSession struct {
	agent    *Agent
	lock     chan struct{}
	lastUsed time.Time
	active   int
}

type sessionStore struct {
	mu          sync.Mutex
	sessions    map[string]*chatSession
	agentConfig AgentConfig
	config      HTTPConfig
}

type HTTPService struct {
	agentConfig AgentConfig
	config      HTTPConfig
	sessions    *sessionStore
	scenarios   []scenario
	limiter     chan struct{}
	client      *http.Client
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

func HTTPConfigFromEnv() (HTTPConfig, error) {
	port, err := httpEnvInt("HTTP_PORT", 8082)
	if err != nil {
		return HTTPConfig{}, err
	}
	sessionTTL, err := httpEnvFloat("SESSION_TTL", 1800)
	if err != nil {
		return HTTPConfig{}, err
	}
	maxSessions, err := httpEnvInt("MAX_SESSIONS", 100)
	if err != nil {
		return HTTPConfig{}, err
	}
	maxConcurrent, err := httpEnvInt("MAX_CONCURRENT_REQUESTS", 4)
	if err != nil {
		return HTTPConfig{}, err
	}
	chatTimeout, err := httpEnvFloat("CHAT_TIMEOUT", 300)
	if err != nil {
		return HTTPConfig{}, err
	}
	simulateTimeout, err := httpEnvFloat("SIMULATE_TIMEOUT", 900)
	if err != nil {
		return HTTPConfig{}, err
	}
	config := HTTPConfig{
		Host:                  envString("HTTP_HOST", "0.0.0.0"),
		Port:                  port,
		SessionTTL:            seconds(sessionTTL),
		MaxSessions:           maxSessions,
		MaxConcurrentRequests: maxConcurrent,
		ChatTimeout:           seconds(chatTimeout),
		SimulateTimeout:       seconds(simulateTimeout),
	}
	if config.Port < 1 || config.Port > 65535 {
		return HTTPConfig{}, errors.New("HTTP_PORT must be from 1 to 65535")
	}
	if config.SessionTTL <= 0 || config.MaxSessions <= 0 || config.MaxConcurrentRequests <= 0 || config.ChatTimeout <= 0 || config.SimulateTimeout <= 0 {
		return HTTPConfig{}, errors.New("HTTP service limits and timeouts must be greater than zero")
	}
	return config, nil
}

func NewHTTPService(agentConfig AgentConfig, config HTTPConfig) (*HTTPService, error) {
	var scenarios []scenario
	if err := json.Unmarshal(scenariosJSON, &scenarios); err != nil {
		return nil, fmt.Errorf("load scenarios: %w", err)
	}
	return &HTTPService{
		agentConfig: agentConfig,
		config:      config,
		sessions: &sessionStore{
			sessions:    make(map[string]*chatSession),
			agentConfig: agentConfig,
			config:      config,
		},
		scenarios: scenarios,
		limiter:   make(chan struct{}, config.MaxConcurrentRequests),
		client:    &http.Client{},
	}, nil
}

func (s *HTTPService) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				writeAPIError(w, &apiError{Status: 500, Code: "internal_error", Message: "unexpected service error"})
			}
		}()
		s.route(w, r)
	})
}

func (s *HTTPService) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "recipe"})
	case r.URL.Path == "/readyz" && r.Method == http.MethodGet:
		s.handleReady(w, r)
	case r.URL.Path == "/v1/chat" && r.Method == http.MethodPost:
		s.handleChat(w, r)
	case r.URL.Path == "/v1/simulate" && r.Method == http.MethodPost:
		s.handleSimulate(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/sessions/") && r.Method == http.MethodDelete:
		s.sessions.delete(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"))
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/v1/chat" || r.URL.Path == "/v1/simulate" || strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
		writeAPIError(w, &apiError{Status: 405, Code: "method_not_allowed", Message: "method not allowed"})
	default:
		writeAPIError(w, &apiError{Status: 404, Code: "not_found", Message: "not found"})
	}
}

func (s *HTTPService) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), min(s.agentConfig.Timeout, 5*time.Second))
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.agentConfig.OllamaHost, "/")+"/api/tags", nil)
	if err != nil {
		writeAPIError(w, &apiError{Status: 503, Code: "not_ready", Message: "Ollama is unavailable"})
		return
	}
	response, err := s.client.Do(request)
	if err != nil {
		writeAPIError(w, &apiError{Status: 503, Code: "not_ready", Message: "Ollama is unavailable"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeAPIError(w, &apiError{Status: 503, Code: "not_ready", Message: "Ollama is unavailable"})
		return
	}
	var payload struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		writeAPIError(w, &apiError{Status: 503, Code: "not_ready", Message: "Ollama returned an invalid response"})
		return
	}
	for _, model := range payload.Models {
		if model.Name == s.agentConfig.Model || model.Model == s.agentConfig.Model {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "service": "recipe", "model": s.agentConfig.Model})
			return
		}
	}
	writeAPIError(w, &apiError{Status: 503, Code: "not_ready", Message: fmt.Sprintf("model %s is unavailable", s.agentConfig.Model)})
}

func (s *HTTPService) handleChat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.ChatTimeout)
	defer cancel()

	var input struct {
		Message   string  `json:"message"`
		SessionID *string `json:"session_id"`
	}
	if err := decodeBody(w, r, false, map[string]struct{}{"message": {}, "session_id": {}}, &input); err != nil {
		writeAPIError(w, err)
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if input.Message == "" {
		writeAPIError(w, &apiError{Status: 400, Code: "invalid_request", Message: "message must be a non-empty string"})
		return
	}
	if utf8.RuneCountInString(input.Message) > maxMessageChars {
		writeAPIError(w, &apiError{Status: 400, Code: "invalid_request", Message: "message exceeds 8000 characters"})
		return
	}
	if input.SessionID != nil && strings.TrimSpace(*input.SessionID) == "" {
		writeAPIError(w, &apiError{Status: 400, Code: "invalid_request", Message: "session_id must be a non-empty string"})
		return
	}
	if !s.enter() {
		writeAPIError(w, &apiError{Status: 429, Code: "too_many_requests", Message: "service concurrency limit reached"})
		return
	}
	defer s.leave()

	identifier, session, err := s.sessions.acquire(input.SessionID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer s.sessions.release(session)
	select {
	case session.lock <- struct{}{}:
		defer func() { <-session.lock }()
	case <-ctx.Done():
		writeAPIError(w, &apiError{Status: 504, Code: "ollama_timeout", Message: "request deadline exceeded"})
		return
	}
	traces := make([]toolTrace, 0)
	answer, askErr := session.agent.Ask(ctx, input.Message, func(name string, arguments, result map[string]any) {
		traces = append(traces, toolTrace{Name: name, Arguments: arguments, Summary: SummarizeToolResult(result)})
	})
	if askErr != nil {
		writeAPIError(w, ollamaError(askErr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": identifier, "answer": answer, "tools": traces})
}

func (s *HTTPService) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Scenario *string `json:"scenario"`
		Seed     *int64  `json:"seed"`
	}
	if err := decodeBody(w, r, true, map[string]struct{}{"scenario": {}, "seed": {}}, &input); err != nil {
		writeAPIError(w, err)
		return
	}
	selected, err := s.selectScenario(input.Scenario, input.Seed)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if !s.enter() {
		writeAPIError(w, &apiError{Status: 429, Code: "too_many_requests", Message: "service concurrency limit reached"})
		return
	}
	defer s.leave()

	ctx, cancel := context.WithTimeout(r.Context(), s.config.SimulateTimeout)
	defer cancel()
	simulationAgent, createErr := NewAgent(s.agentConfig)
	if createErr != nil {
		writeAPIError(w, &apiError{Status: 500, Code: "internal_error", Message: "unexpected service error"})
		return
	}
	transcript := make([]transcriptTurn, 0, len(selected.Prompts))
	for _, prompt := range selected.Prompts {
		traces := make([]toolTrace, 0)
		answer, askErr := simulationAgent.Ask(ctx, prompt, func(name string, arguments, result map[string]any) {
			traces = append(traces, toolTrace{Name: name, Arguments: arguments, Summary: SummarizeToolResult(result)})
		})
		if askErr != nil {
			writeAPIError(w, ollamaError(askErr))
			return
		}
		transcript = append(transcript, transcriptTurn{Message: prompt, Answer: answer, Tools: traces})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenario": selected.Name, "transcript": transcript})
}

func (s *HTTPService) selectScenario(name *string, seed *int64) (scenario, *apiError) {
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return scenario{}, &apiError{Status: 400, Code: "invalid_request", Message: "scenario must be a non-empty string"}
		}
		for _, item := range s.scenarios {
			if item.Name == trimmed {
				return item, nil
			}
		}
		return scenario{}, &apiError{Status: 400, Code: "unknown_scenario", Message: "unknown scenario: " + trimmed}
	}
	index := 0
	if seed != nil {
		rng := mathrand.New(mathrand.NewPCG(uint64(*seed), uint64(*seed)^0x9e3779b97f4a7c15))
		index = rng.IntN(len(s.scenarios))
	} else {
		index = mathrand.IntN(len(s.scenarios))
	}
	return s.scenarios[index], nil
}

func (s *HTTPService) enter() bool {
	select {
	case s.limiter <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *HTTPService) leave() { <-s.limiter }

func (s *sessionStore) acquire(requested *string) (string, *chatSession, *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.expire(now)
	if requested != nil {
		identifier := strings.TrimSpace(*requested)
		session := s.sessions[identifier]
		if session == nil {
			return "", nil, &apiError{Status: 404, Code: "session_not_found", Message: "unknown session_id"}
		}
		session.active++
		session.lastUsed = now
		return identifier, session, nil
	}
	if len(s.sessions) >= s.config.MaxSessions {
		oldestID := ""
		var oldest time.Time
		for identifier, session := range s.sessions {
			if session.active == 0 && (oldestID == "" || session.lastUsed.Before(oldest)) {
				oldestID = identifier
				oldest = session.lastUsed
			}
		}
		if oldestID == "" {
			return "", nil, &apiError{Status: 429, Code: "session_capacity", Message: "session capacity reached"}
		}
		delete(s.sessions, oldestID)
	}
	agent, err := NewAgent(s.agentConfig)
	if err != nil {
		return "", nil, &apiError{Status: 500, Code: "internal_error", Message: "unexpected service error"}
	}
	identifier, err := randomID()
	if err != nil {
		return "", nil, &apiError{Status: 500, Code: "internal_error", Message: "unexpected service error"}
	}
	session := &chatSession{agent: agent, lock: make(chan struct{}, 1), lastUsed: now, active: 1}
	s.sessions[identifier] = session
	return identifier, session, nil
}

func (s *sessionStore) release(session *chatSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session.active > 0 {
		session.active--
	}
	session.lastUsed = time.Now()
}

func (s *sessionStore) delete(identifier string) {
	s.mu.Lock()
	delete(s.sessions, identifier)
	s.mu.Unlock()
}

func (s *sessionStore) expire(now time.Time) {
	for identifier, session := range s.sessions {
		if session.active == 0 && now.Sub(session.lastUsed) > s.config.SessionTTL {
			delete(s.sessions, identifier)
		}
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, allowEmpty bool, allowed map[string]struct{}, target any) *apiError {
	reader := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(reader)
	if err != nil {
		return &apiError{Status: 400, Code: "invalid_request", Message: "request body exceeds 32 KiB"}
	}
	if len(strings.TrimSpace(string(raw))) == 0 && allowEmpty {
		raw = []byte("{}")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &apiError{Status: 400, Code: "invalid_json", Message: "request body must be valid JSON"}
	}
	if fields == nil {
		return &apiError{Status: 400, Code: "invalid_request", Message: "request body must be a JSON object"}
	}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return &apiError{Status: 400, Code: "invalid_request", Message: "unknown field: " + name}
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return &apiError{Status: 400, Code: "invalid_request", Message: "invalid request fields"}
	}
	return nil
}

func ollamaError(err error) *apiError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &apiError{Status: 504, Code: "ollama_timeout", Message: "Ollama request timed out"}
	}
	return &apiError{Status: 502, Code: "ollama_error", Message: err.Error()}
}

func writeAPIError(w http.ResponseWriter, err *apiError) {
	writeJSON(w, err.Status, map[string]any{"error": map[string]string{"code": err.Code, "message": err.Message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := cryptorand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func httpEnvInt(name string, fallback int) (int, error) {
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

func httpEnvFloat(name string, fallback float64) (float64, error) {
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
