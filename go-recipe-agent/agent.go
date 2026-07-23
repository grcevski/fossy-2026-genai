package recipeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const systemPrompt = `You are a practical offline recipe agent.
Use the supplied tools whenever you recommend or scale a recipe. The catalog is static
demo data, uses metric units, and cannot guarantee allergen safety. Ask at most one
focused clarification when a missing preference would materially change the result;
otherwise state reasonable assumptions. Explain substitutions carefully, tell users to
check ingredient labels for dietary or allergy requirements, and never invent tool results.`

const maxToolRounds = 5
const maxHistoryTurns = 10

type ToolFunction struct {
	Index     int            `json:"index,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ToolCall struct {
	Type     string       `json:"type,omitempty"`
	Function ToolFunction `json:"function"`
}

type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []Message        `json:"messages"`
	Tools    []map[string]any `json:"tools"`
	Stream   bool             `json:"stream"`
	Think    bool             `json:"think"`
	Options  map[string]any   `json:"options"`
}

type chatResponse struct {
	Message Message `json:"message"`
}

type Agent struct {
	config AgentConfig
	client *http.Client
	tools  *RecipeTools
	turns  [][]Message
}

type ToolCallback func(name string, arguments map[string]any, result map[string]any)

func NewAgent(config AgentConfig) (*Agent, error) {
	tools, err := NewRecipeTools()
	if err != nil {
		return nil, err
	}
	return &Agent{
		config: config,
		client: &http.Client{},
		tools:  tools,
	}, nil
}

func (a *Agent) Reset() {
	a.turns = nil
}

func (a *Agent) Ask(ctx context.Context, userText string, onTool ToolCallback) (string, error) {
	current := []Message{{Role: "user", Content: userText}}
	for round := 0; round < maxToolRounds; round++ {
		messages := []Message{{Role: "system", Content: systemPrompt}}
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
			result := a.tools.Invoke(name, arguments)
			if onTool != nil {
				onTool(name, arguments, result)
			}
			encoded, _ := json.Marshal(result)
			current = append(current, Message{Role: "tool", ToolName: name, Content: string(encoded)})
		}
	}
	answer := "I stopped after five tool rounds to avoid an accidental loop."
	current = append(current, Message{Role: "assistant", Content: answer})
	a.remember(current)
	return answer, nil
}

func (a *Agent) remember(turn []Message) {
	a.turns = append(a.turns, turn)
	if len(a.turns) > maxHistoryTurns {
		a.turns = a.turns[len(a.turns)-maxHistoryTurns:]
	}
}

func (a *Agent) chat(ctx context.Context, messages []Message) (Message, error) {
	payload := chatRequest{
		Model:    a.config.Model,
		Messages: messages,
		Tools:    ToolSchemas,
		Stream:   false,
		Think:    false,
		Options:  map[string]any{"temperature": a.config.Temperature},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("encode Ollama request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.config.Timeout)
	defer cancel()
	url := strings.TrimRight(a.config.OllamaHost, "/") + "/api/chat"
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return Message{}, fmt.Errorf("create Ollama request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return Message{}, fmt.Errorf("cannot reach Ollama: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return Message{}, fmt.Errorf("read Ollama response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Message{}, fmt.Errorf("Ollama returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result chatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return Message{}, fmt.Errorf("decode Ollama response: %w", err)
	}
	if result.Message.Role == "" && result.Message.Content == "" && len(result.Message.ToolCalls) == 0 {
		return Message{}, fmt.Errorf("Ollama returned an invalid chat response")
	}
	return result.Message, nil
}
