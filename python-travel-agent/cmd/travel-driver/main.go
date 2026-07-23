package main

import (
	"os"

	traveldriver "example.com/ollama-travel-driver"
)

func main() {
	os.Exit(traveldriver.Main(os.Args[1:]))
}
