BINARY     := foundry-registry
CTL_BINARY := regctl
DATA_DIR   := ./data
ADDR       := :8080

GOLANGCI_LINT ?= golangci-lint

.PHONY: all build run tidy fmt fmt-check vet lint lint-fix test test-race test-integration check clean clean-data test-push help

all: build

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-18s %s\n", $$1, $$2}'

build: ## Build registry and regctl binaries
	go build -o $(BINARY) ./cmd/registry
	go build -o $(CTL_BINARY) ./cmd/regctl

run: build ## Build and run the registry
	./$(BINARY) -data $(DATA_DIR) -addr $(ADDR)

tidy: ## Tidy go.mod/go.sum
	go mod tidy

# ---------------------------------------------------------------------------
# Code quality
# ---------------------------------------------------------------------------

fmt: ## Format code (gofmt + goimports via golangci-lint)
	$(GOLANGCI_LINT) fmt ./...

fmt-check: ## Fail if any file is not formatted
	@out=$$($(GOLANGCI_LINT) fmt --diff ./...); \
	if [ -n "$$out" ]; then echo "$$out"; echo "Run 'make fmt' to fix formatting."; exit 1; fi

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint (config: .golangci.yml)
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { \
		echo "golangci-lint not found. Install: brew install golangci-lint"; \
		echo "  or: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; }
	$(GOLANGCI_LINT) run ./...

lint-fix: ## Run golangci-lint and apply automatic fixes
	$(GOLANGCI_LINT) run --fix ./...

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

test: ## Run all tests
	go test ./... -count=1

test-race: ## Run all tests with the race detector
	go test ./... -count=1 -race

test-integration: ## Run end-to-end HTTP tests only
	go test ./tests/integration/... -count=1 -v

check: fmt-check vet lint test-race ## Everything CI should run

# Quick Docker push test (requires daemon with insecure-registries)
test-push: ## Push/pull nginx through a running registry
	docker tag nginx localhost$(ADDR)/nginx:test
	docker push localhost$(ADDR)/nginx:test
	docker pull localhost$(ADDR)/nginx:test

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean: ## Remove built binaries (keeps registry data)
	rm -f $(BINARY) $(CTL_BINARY)

clean-data: ## DESTRUCTIVE: delete all registry data in $(DATA_DIR)
	@printf "Delete all registry data in $(DATA_DIR)? [y/N] "; read ans; \
	[ "$$ans" = "y" ] && rm -rf $(DATA_DIR) || echo "aborted"
