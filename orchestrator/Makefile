# jcode Cloud Agent orchestrator — developer tasks.
# Config comes from the environment; `make dev` loads .env if present.

BINARY      := orchestrator
PKG         := ./cmd/orchestrator
IMAGE       ?= ghcr.io/cnjack/jcloud-orchestrator
VERSION     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
LDFLAGS     := -s -w -X github.com/cnjack/jcloud/internal/version.Version=$(VERSION) -X github.com/cnjack/jcloud/internal/version.Commit=$(COMMIT)

# Load .env into the environment for run/dev/migrate targets.
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the server binary into ./bin.
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

.PHONY: run
run: ## Run the server (reads env / .env).
	go run $(PKG)

.PHONY: test
test: ## Run all unit tests (no cluster/DB needed).
	go test ./... -timeout 120s

.PHONY: test-race
test-race: ## Run tests with the race detector.
	go test -race ./... -timeout 180s

.PHONY: integration
integration: ## Run the cluster integration test (needs a reachable K8s + JCLOUD_IT=1).
	JCLOUD_IT=1 go test ./internal/k8s/ -run Integration -v -timeout 300s

.PHONY: vet
vet: ## go vet.
	go vet ./...

.PHONY: tidy
tidy: ## go mod tidy.
	go mod tidy

.PHONY: migrate
migrate: ## Apply migrations against DATABASE_URL (also runs automatically at boot).
	go run $(PKG) -migrate-only

.PHONY: pg-up
pg-up: ## Start the dev Postgres via docker compose.
	docker compose -f compose.yml up -d
	@echo "waiting for postgres..."
	@until docker compose -f compose.yml exec -T postgres pg_isready -U jcloud -d jcloud >/dev/null 2>&1; do sleep 1; done
	@echo "postgres ready on :5432"

.PHONY: pg-down
pg-down: ## Stop the dev Postgres.
	docker compose -f compose.yml down

.PHONY: dev
dev: pg-up ## Bring up Postgres and run the server with sane dev defaults.
	@echo "starting orchestrator (DISABLE_K8S honored from env; set a kubeconfig to schedule)"
	DATABASE_URL?="postgres://jcloud:jcloud@localhost:5432/jcloud?sslmode=disable" \
	CONSOLE_TOKEN=$${CONSOLE_TOKEN:-dev-console-token} \
	DISABLE_K8S=$${DISABLE_K8S:-1} \
	go run $(PKG)

.PHONY: docker-build
docker-build: ## Build the multi-stage Docker image.
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf bin
