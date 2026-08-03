package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	recipeagent "example.com/ollama-recipe-agent"
)

func main() {
	agentConfig, err := recipeagent.AgentConfigFromEnv()
	if err != nil {
		fatal(err)
	}
	if err := recipeagent.ValidateConfig(agentConfig); err != nil {
		fatal(err)
	}
	httpConfig, err := recipeagent.HTTPConfigFromEnv()
	if err != nil {
		fatal(err)
	}
	service, err := recipeagent.NewHTTPService(agentConfig, httpConfig)
	if err != nil {
		fatal(err)
	}
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", httpConfig.Host, httpConfig.Port),
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      httpConfig.SimulateTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("recipe service listening on %s\n", server.Addr)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
		return
	case <-stop:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		fatal(fmt.Errorf("graceful shutdown: %w", err))
	}
	if err := <-serverErrors; !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(2)
}
