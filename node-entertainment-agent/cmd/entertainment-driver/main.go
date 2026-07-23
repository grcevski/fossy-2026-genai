package main

import (
	"os"

	entertainmentdriver "example.com/ollama-entertainment-driver"
)

func main() {
	os.Exit(entertainmentdriver.Main(os.Args[1:]))
}
