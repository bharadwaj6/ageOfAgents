# Age of Agents — common dev commands. Run `make help` for the catalog.

GO        ?= go
BIN       ?= aoa
FMT_DIRS  := cmd internal pkg
COUNT     ?= 1
TLA2TOOLS ?= tla2tools.jar

.DEFAULT_GOAL := help

.PHONY: help build install test test-short race vet fmt fmt-check check bench chaos smoke formal clean

help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-11s\033[0m %s\n", $$1, $$2}'

build: ## Build the aoa CLI binary (-> ./aoa)
	$(GO) build -o $(BIN) ./cmd/aoa

test: ## Run the full hermetic test suite
	$(GO) test ./...

test-short: ## Run tests in short mode (faster; fewer chaos seeds)
	$(GO) test -short ./...

race: ## Run the test suite with the race detector
	$(GO) test -race ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go code in place
	gofmt -w $(FMT_DIRS)

fmt-check: ## Fail if any Go file is not gofmt-clean
	@out="$$(gofmt -l $(FMT_DIRS))"; \
	if [ -n "$$out" ]; then echo "gofmt needed for:"; echo "$$out"; exit 1; fi

check: build vet test fmt-check ## Full pre-commit gate (build + vet + test + gofmt)

install: ## Build and install the aoa binary into $(GOBIN) (or $(GOPATH)/bin)
	$(GO) install ./cmd/aoa

bench: build ## Run the hermetic coordination benchmark suite
	./$(BIN) bench

chaos: ## Chaos/fault-injection soak (override with COUNT=N for a longer soak)
	$(GO) test ./internal/orchestrator -run TestChaos -count=$(COUNT)

smoke: ## Live real-LLM smoke test (needs an authenticated `claude` CLI)
	scripts/live_smoke.sh

formal: ## Model-check the TLA+ spec (needs java + TLA2TOOLS=path/to/tla2tools.jar)
	cd docs/design/formal && java -cp $(abspath $(TLA2TOOLS)) tlc2.TLC -config Orchestrator.cfg Orchestrator.tla

clean: ## Remove build artifacts and TLC working output
	rm -f $(BIN)
	rm -rf docs/design/formal/states
