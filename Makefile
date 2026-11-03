# Loads .env if present so `make serve` etc. pick up local settings.
-include .env
export

.PHONY: up down migrate serve build test test-integration lint sqlc

up: ## Start local infrastructure
	docker compose up -d --wait

down: ## Stop local infrastructure and delete its data
	docker compose down -v

build:
	go build -o bin/server ./cmd/server

migrate: build ## Apply migrations (as the owner role)
	./bin/server migrate

serve: build ## Run the API
	./bin/server serve

test: ## Unit tests (no Docker needed)
	go test -race ./...

test-integration: ## Unit + integration tests (needs Docker)
	go test -race -tags integration ./...

lint:
	golangci-lint run ./...

sqlc: ## Regenerate query code after editing migrations or queries
	go tool sqlc generate
