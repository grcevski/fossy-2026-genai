PYTHON ?= python3
GO ?= go
NODE ?= node
RECIPE_AGENT_BINARY := go-recipe-agent/bin/recipe-agent
TRAVEL_DRIVER_BINARY := python-travel-agent/bin/travel-driver
RECIPE_DRIVER_BINARY := go-recipe-agent/bin/recipe-driver
ENTERTAINMENT_DRIVER_BINARY := node-entertainment-agent/bin/entertainment-driver

.PHONY: all travel recipe entertainment build-recipe-agent build-travel-driver build-recipe-driver build-entertainment-driver drive-travel drive-recipe drive-entertainment drive-all

all: build-recipe-agent build-travel-driver build-recipe-driver build-entertainment-driver

travel:
	$(PYTHON) python-travel-agent/cli.py

build-recipe-agent:
	mkdir -p go-recipe-agent/bin
	cd go-recipe-agent && $(GO) build -buildvcs=false -o bin/recipe-agent ./cmd/recipe-agent

recipe: build-recipe-agent
	./$(RECIPE_AGENT_BINARY)

entertainment:
	$(NODE) node-entertainment-agent/src/cli.js

build-travel-driver:
	mkdir -p python-travel-agent/bin
	cd python-travel-agent && $(GO) build -buildvcs=false -o bin/travel-driver ./cmd/travel-driver

build-recipe-driver:
	mkdir -p go-recipe-agent/bin
	cd go-recipe-agent && $(GO) build -buildvcs=false -o bin/recipe-driver ./cmd/recipe-driver

build-entertainment-driver:
	mkdir -p node-entertainment-agent/bin
	cd node-entertainment-agent && $(GO) build -buildvcs=false -o bin/entertainment-driver ./cmd/entertainment-driver

drive-travel: build-travel-driver
	./$(TRAVEL_DRIVER_BINARY)

drive-recipe: build-recipe-driver
	./$(RECIPE_DRIVER_BINARY)

drive-entertainment: build-entertainment-driver
	./$(ENTERTAINMENT_DRIVER_BINARY)

drive-all: build-travel-driver build-recipe-driver build-entertainment-driver
	@./$(TRAVEL_DRIVER_BINARY) & travel_pid=$$!; \
	./$(RECIPE_DRIVER_BINARY) & recipe_pid=$$!; \
	./$(ENTERTAINMENT_DRIVER_BINARY) & entertainment_pid=$$!; \
	trap 'kill $$travel_pid $$recipe_pid $$entertainment_pid 2>/dev/null || true' INT TERM EXIT; \
	wait
