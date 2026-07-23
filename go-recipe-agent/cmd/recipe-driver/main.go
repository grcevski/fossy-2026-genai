package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	recipeagent "example.com/ollama-recipe-agent"
)

//go:embed scenarios.json
var scenariosJSON []byte

type scenario struct {
	Name    string   `json:"name"`
	Prompts []string `json:"prompts"`
}

type stats struct {
	mu                sync.Mutex
	sessionsStarted   int
	sessionsCompleted int
	turnsCompleted    int
	failures          int
}

var printMu sync.Mutex

func main() {
	agentConfig, err := recipeagent.AgentConfigFromEnv()
	if err != nil {
		fatal(err)
	}
	driverConfig, err := recipeagent.DriverConfigFromEnv()
	if err != nil {
		fatal(err)
	}
	recipeagent.BindAgentFlags(flag.CommandLine, &agentConfig)
	recipeagent.BindDriverFlags(flag.CommandLine, &driverConfig)
	flag.Parse()
	if err := recipeagent.ValidateConfig(agentConfig, &driverConfig); err != nil {
		fatal(err)
	}
	var scenarios []scenario
	if err := json.Unmarshal(scenariosJSON, &scenarios); err != nil {
		fatal(fmt.Errorf("load scenarios: %w", err))
	}
	seed := driverConfig.RandomSeed
	if !driverConfig.SeedWasSet && seed == 0 {
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

	logLine(fmt.Sprintf("[driver] model=%s workers=%d seed=%d max_sessions=%s",
		agentConfig.Model, driverConfig.Workers, seed, maximumLabel(driverConfig.MaxSessions)))
	statistics := &stats{}
	var workers sync.WaitGroup
	for workerID := 1; workerID <= driverConfig.Workers; workerID++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			rng := rand.New(rand.NewSource(seed + int64(id)))
			for !stopping.Load() {
				sessionID, ok := statistics.claimSession(driverConfig.MaxSessions)
				if !ok {
					return
				}
				selected := scenarios[rng.Intn(len(scenarios))]
				agent, err := recipeagent.NewAgent(agentConfig)
				if err != nil {
					logLine(fmt.Sprintf("[worker=%d session=%d] error: %v", id, sessionID, err))
					statistics.addFailure()
					return
				}
				for promptIndex, prompt := range selected.Prompts {
					if stopping.Load() {
						break
					}
					prefix := fmt.Sprintf("[worker=%d session=%d]", id, sessionID)
					logLine(fmt.Sprintf("%s user> %s", prefix, prompt))
					backoff := time.Second
					for !stopping.Load() {
						started := time.Now()
						answer, err := agent.Ask(context.Background(), prompt, func(name string, arguments, result map[string]any) {
							compact, _ := json.Marshal(arguments)
							logLine(fmt.Sprintf("%s [tool] %s %s -> %s", prefix, name, compact, recipeagent.SummarizeToolResult(result)))
						})
						if err == nil {
							statistics.addTurn()
							logLine(fmt.Sprintf("%s agent> %s [duration=%.2fs]", prefix, answer, time.Since(started).Seconds()))
							break
						}
						statistics.addFailure()
						logLine(fmt.Sprintf("%s error: %v; retrying in %s", prefix, err, backoff))
						if waitFor(&stopping, backoff) {
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
					if promptIndex+1 < len(selected.Prompts) && waitFor(&stopping, randomDuration(rng, driverConfig.MinDelay, driverConfig.MaxDelay)) {
						break
					}
				}
				sessions, turns, failures := statistics.finishSession()
				logLine(fmt.Sprintf("[summary] sessions=%d turns=%d failures=%d", sessions, turns, failures))
				if driverConfig.MaxSessions > 0 && statistics.allSessionsClaimed(driverConfig.MaxSessions) {
					return
				}
				if !stopping.Load() {
					waitFor(&stopping, randomDuration(rng, driverConfig.SessionMinDelay, driverConfig.SessionMaxDelay))
				}
			}
		}(workerID)
	}
	workers.Wait()
	started, completed, turns, failures := statistics.snapshot()
	logLine(fmt.Sprintf("[final] sessions_started=%d sessions_completed=%d turns=%d failures=%d", started, completed, turns, failures))
}

func (s *stats) claimSession(maximum int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maximum > 0 && s.sessionsStarted >= maximum {
		return 0, false
	}
	s.sessionsStarted++
	return s.sessionsStarted, true
}

func (s *stats) addTurn() {
	s.mu.Lock()
	s.turnsCompleted++
	s.mu.Unlock()
}

func (s *stats) addFailure() {
	s.mu.Lock()
	s.failures++
	s.mu.Unlock()
}

func (s *stats) finishSession() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionsCompleted++
	return s.sessionsCompleted, s.turnsCompleted, s.failures
}

func (s *stats) snapshot() (int, int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionsStarted, s.sessionsCompleted, s.turnsCompleted, s.failures
}

func (s *stats) allSessionsClaimed(maximum int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionsStarted >= maximum
}

func logLine(message string) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), message)
}

func waitFor(stopping *atomic.Bool, duration time.Duration) bool {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		if stopping.Load() {
			return true
		}
		select {
		case <-deadline.C:
			return stopping.Load()
		case <-time.After(100 * time.Millisecond):
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
	return fmt.Sprint(maximum)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(2)
}
