package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	recipeagent "example.com/ollama-recipe-agent"
)

func main() {
	config, err := recipeagent.AgentConfigFromEnv()
	if err != nil {
		fatal(err)
	}
	recipeagent.BindAgentFlags(flag.CommandLine, &config)
	flag.Parse()
	if err := recipeagent.ValidateConfig(config); err != nil {
		fatal(err)
	}
	agent, err := recipeagent.NewAgent(config)
	if err != nil {
		fatal(err)
	}

	color := terminal(os.Stdout) && os.Getenv("NO_COLOR") == ""
	cyan, green, yellow, reset := "", "", "", ""
	if color {
		cyan, green, yellow, reset = "\033[36m", "\033[32m", "\033[33m", "\033[0m"
	}
	fmt.Println("Go recipe agent — static demo catalog; verify labels and allergy requirements.")
	fmt.Println("Commands: /help, /reset, /quit")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("%syou>%s ", cyan, reset)
		if !scanner.Scan() {
			fmt.Println("\nGoodbye.")
			return
		}
		text := strings.TrimSpace(scanner.Text())
		switch text {
		case "":
			continue
		case "/quit":
			fmt.Println("Goodbye.")
			return
		case "/help":
			fmt.Println("Ask for recipes, ingredient-based ideas, substitutions, or scaled servings.")
			continue
		case "/reset":
			agent.Reset()
			fmt.Println("Conversation reset.")
			continue
		}

		answer, err := agent.Ask(context.Background(), text, func(name string, arguments, result map[string]any) {
			compact, _ := json.Marshal(arguments)
			fmt.Printf("%s[tool]%s %s %s -> %s\n", yellow, reset, name, compact, recipeagent.SummarizeToolResult(result))
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		fmt.Printf("%sagent>%s %s\n", green, reset, answer)
	}
}

func terminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(2)
}
