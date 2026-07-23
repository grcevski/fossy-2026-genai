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

type DriverConfig struct {
	Workers         int
	MinDelay        time.Duration
	MaxDelay        time.Duration
	SessionMinDelay time.Duration
	SessionMaxDelay time.Duration
	RandomSeed      int64
	SeedWasSet      bool
	MaxSessions     int
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

func DriverConfigFromEnv() (DriverConfig, error) {
	workers, err := envInt("WORKERS", 1)
	if err != nil {
		return DriverConfig{}, err
	}
	minDelay, err := envFloat("MIN_DELAY", 2)
	if err != nil {
		return DriverConfig{}, err
	}
	maxDelay, err := envFloat("MAX_DELAY", 8)
	if err != nil {
		return DriverConfig{}, err
	}
	sessionMinDelay, err := envFloat("SESSION_MIN_DELAY", 5)
	if err != nil {
		return DriverConfig{}, err
	}
	sessionMaxDelay, err := envFloat("SESSION_MAX_DELAY", 15)
	if err != nil {
		return DriverConfig{}, err
	}
	maxSessions, err := envInt("MAX_SESSIONS", 0)
	if err != nil {
		return DriverConfig{}, err
	}
	seedText, seedWasSet := os.LookupEnv("RANDOM_SEED")
	var seed int64
	if seedWasSet {
		seed, err = strconv.ParseInt(seedText, 10, 64)
		if err != nil {
			return DriverConfig{}, errors.New("RANDOM_SEED must be an integer")
		}
	}
	return DriverConfig{
		Workers:         workers,
		MinDelay:        seconds(minDelay),
		MaxDelay:        seconds(maxDelay),
		SessionMinDelay: seconds(sessionMinDelay),
		SessionMaxDelay: seconds(sessionMaxDelay),
		RandomSeed:      seed,
		SeedWasSet:      seedWasSet,
		MaxSessions:     maxSessions,
	}, nil
}

func BindAgentFlags(fs *flag.FlagSet, config *AgentConfig) {
	fs.StringVar(&config.OllamaHost, "ollama-host", config.OllamaHost, "Ollama base URL")
	fs.StringVar(&config.Model, "model", config.Model, "Ollama model")
	fs.Var(secondsValue{target: &config.Timeout}, "timeout", "HTTP request timeout in seconds")
	fs.Float64Var(&config.Temperature, "temperature", config.Temperature, "sampling temperature")
}

func BindDriverFlags(fs *flag.FlagSet, config *DriverConfig) {
	fs.IntVar(&config.Workers, "workers", config.Workers, "concurrent simulated users")
	fs.Var(secondsValue{target: &config.MinDelay}, "min-delay", "minimum delay between prompts in seconds")
	fs.Var(secondsValue{target: &config.MaxDelay}, "max-delay", "maximum delay between prompts in seconds")
	fs.Var(secondsValue{target: &config.SessionMinDelay}, "session-min-delay", "minimum delay between sessions in seconds")
	fs.Var(secondsValue{target: &config.SessionMaxDelay}, "session-max-delay", "maximum delay between sessions in seconds")
	fs.Int64Var(&config.RandomSeed, "random-seed", config.RandomSeed, "random seed; zero chooses one when unset")
	fs.IntVar(&config.MaxSessions, "max-sessions", config.MaxSessions, "maximum total sessions; zero runs forever")
}

func ValidateConfig(agent AgentConfig, driver *DriverConfig) error {
	if !strings.HasPrefix(agent.OllamaHost, "http://") && !strings.HasPrefix(agent.OllamaHost, "https://") {
		return errors.New("OLLAMA_HOST must start with http:// or https://")
	}
	if agent.Timeout <= 0 {
		return errors.New("OLLAMA_TIMEOUT must be greater than zero")
	}
	if agent.Temperature < 0 {
		return errors.New("OLLAMA_TEMPERATURE cannot be negative")
	}
	if driver == nil {
		return nil
	}
	if driver.Workers < 1 {
		return errors.New("WORKERS must be at least 1")
	}
	if driver.MaxSessions < 0 {
		return errors.New("MAX_SESSIONS cannot be negative")
	}
	if driver.MinDelay < 0 || driver.MaxDelay < driver.MinDelay {
		return errors.New("invalid prompt delay range")
	}
	if driver.SessionMinDelay < 0 || driver.SessionMaxDelay < driver.SessionMinDelay {
		return errors.New("invalid session delay range")
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
