package recipeagent

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type AgentConfig struct {
	OllamaHost  string
	Model       string
	Timeout     time.Duration
	Temperature float64
}

func AgentConfigFromEnv() (AgentConfig, error) {
	timeout, err := envFloat("OLLAMA_TIMEOUT", 120)
	if err != nil {
		return AgentConfig{}, err
	}
	temperature, err := envFloat("OLLAMA_TEMPERATURE", 0.3)
	if err != nil {
		return AgentConfig{}, err
	}
	return AgentConfig{
		OllamaHost:  envString("OLLAMA_HOST", "http://localhost:11434"),
		Model:       envString("OLLAMA_MODEL", "qwen3:8b"),
		Timeout:     time.Duration(timeout * float64(time.Second)),
		Temperature: temperature,
	}, nil
}

func BindAgentFlags(fs *flag.FlagSet, config *AgentConfig) {
	fs.StringVar(&config.OllamaHost, "ollama-host", config.OllamaHost, "Ollama base URL")
	fs.StringVar(&config.Model, "model", config.Model, "Ollama model")
	fs.Var(secondsValue{target: &config.Timeout}, "timeout", "HTTP request timeout in seconds")
	fs.Float64Var(&config.Temperature, "temperature", config.Temperature, "sampling temperature")
}

func ValidateConfig(agent AgentConfig) error {
	if !strings.HasPrefix(agent.OllamaHost, "http://") && !strings.HasPrefix(agent.OllamaHost, "https://") {
		return errors.New("OLLAMA_HOST must start with http:// or https://")
	}
	if agent.Timeout <= 0 {
		return errors.New("OLLAMA_TIMEOUT must be greater than zero")
	}
	if agent.Temperature < 0 {
		return errors.New("OLLAMA_TEMPERATURE cannot be negative")
	}
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

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
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
