PYTHON ?= python3
GO ?= go
NODE ?= node
NPM ?= npm
K6 ?= k6

PYTHON_VENV := python-travel-agent/.venv
RECIPE_AGENT_BINARY := go-recipe-agent/bin/recipe-agent
RECIPE_SERVER_BINARY := go-recipe-agent/bin/recipe-server
HTTP_HOST ?= 0.0.0.0
TRAVEL_HTTP_PORT ?= 8081
RECIPE_HTTP_PORT ?= 8082
ENTERTAINMENT_HTTP_PORT ?= 8083

.PHONY: all install install-python install-node travel recipe entertainment build-recipe-agent build-recipe-server serve-travel serve-recipe serve-entertainment serve-all run-all load

all: install build-recipe-agent build-recipe-server

install: install-python install-node

install-python:
	$(PYTHON) -m venv $(PYTHON_VENV)
	$(PYTHON_VENV)/bin/pip install -r python-travel-agent/requirements.txt

install-node:
	cd node-entertainment-agent && $(NPM) install

travel:
	$(PYTHON) python-travel-agent/cli.py

build-recipe-agent:
	mkdir -p go-recipe-agent/bin
	cd go-recipe-agent && $(GO) build -buildvcs=false -o bin/recipe-agent ./cmd/recipe-agent

recipe: build-recipe-agent
	./$(RECIPE_AGENT_BINARY)

entertainment:
	$(NODE) node-entertainment-agent/src/cli.js

build-recipe-server:
	mkdir -p go-recipe-agent/bin
	cd go-recipe-agent && $(GO) build -buildvcs=false -o bin/recipe-server ./cmd/recipe-server

serve-travel: install-python
	cd python-travel-agent && HTTP_HOST=$(HTTP_HOST) HTTP_PORT=$(TRAVEL_HTTP_PORT) .venv/bin/python server.py

serve-recipe: build-recipe-server
	HTTP_HOST=$(HTTP_HOST) HTTP_PORT=$(RECIPE_HTTP_PORT) ./$(RECIPE_SERVER_BINARY)

serve-entertainment: install-node
	cd node-entertainment-agent && HTTP_HOST=$(HTTP_HOST) HTTP_PORT=$(ENTERTAINMENT_HTTP_PORT) $(NODE) src/server.js

serve-all: install build-recipe-server
	@(cd python-travel-agent && HTTP_HOST=$(HTTP_HOST) HTTP_PORT=$(TRAVEL_HTTP_PORT) .venv/bin/python server.py) & travel_pid=$$!; \
	HTTP_HOST=$(HTTP_HOST) HTTP_PORT=$(RECIPE_HTTP_PORT) ./$(RECIPE_SERVER_BINARY) & recipe_pid=$$!; \
	(cd node-entertainment-agent && HTTP_HOST=$(HTTP_HOST) HTTP_PORT=$(ENTERTAINMENT_HTTP_PORT) $(NODE) src/server.js) & entertainment_pid=$$!; \
	trap 'kill $$travel_pid $$recipe_pid $$entertainment_pid 2>/dev/null || true' INT TERM EXIT; \
	wait

run-all: serve-all

load:
	$(K6) run load/k6.js
