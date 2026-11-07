.DEFAULT_GOAL := help
SHELL := /bin/bash

NETWORK        := monthra-net
IMAGE          := ghcr.io/nickealves/monthra-server
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOLANGCI_IMAGE := golangci/golangci-lint:v2.15.0@sha256:fbfcfd12a6f8d7f1b97b3362ca80fc2a244d46863148c73cb88e7793af96c481
COMPOSE        := docker compose
COMPOSE_STG    := docker compose -f docker-compose.yml -f docker-compose.staging.yml

.PHONY: help env network up down logs ps run test test-integration cover fmt lint vuln build staging-up staging-down

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-17s %s\n", $$1, $$2}'

REQUIRED_ENV := MONGO_URI REDIS_ADDR MONGO_ROOT_USERNAME MONGO_ROOT_PASSWORD MONGO_APP_USERNAME MONGO_APP_PASSWORD REDIS_PASSWORD

env: ## Fail unless .env exists and every required value is set (cp .env.example .env)
	@test -f .env || { echo ".env is missing: cp .env.example .env and set the passwords"; exit 1; }
	@for v in $(REQUIRED_ENV); do \
	  grep -qE "^$$v=.+" .env || { echo "$$v is empty in .env"; exit 1; }; \
	done
	@! grep -qE '^[A-Z_]+=.*change-me' .env || { echo "replace every change-me value in .env"; exit 1; }

network: ## Create the shared Docker network used by every Monthra compose file
	@docker network inspect $(NETWORK) >/dev/null 2>&1 || docker network create $(NETWORK) >/dev/null
	@echo "network $(NETWORK) ready"

up: env network ## Start the local environment (API with hot reload, Mongo, Redis, Mailpit)
	$(COMPOSE) up -d --build --wait

down: ## Stop the local environment (volumes are kept)
	$(COMPOSE) --profile tools down

logs: ## Follow the logs of every service
	$(COMPOSE) logs -f --tail=100

ps: ## Show service status
	$(COMPOSE) ps

run: env network ## Run the API on the host, with Mongo and Redis from compose
	$(COMPOSE) up -d --wait mongo redis
	set -a && . ./.env && set +a && \
	MONGO_URI="$${MONGO_URI/@mongo:/@localhost:}" REDIS_ADDR=localhost:6379 \
	go run -ldflags "-X main.version=$(VERSION)" ./cmd/api

test: ## Unit tests with the race detector
	go test -race ./...

test-integration: ## Unit + integration tests (needs Docker: testcontainers runs Mongo and Redis)
	go test -race -tags=integration ./...

COVERPKG := ./internal/...,./migrations/...

cover: ## Unit + integration tests with a cross-package coverage report (coverage.out)
	go test -race -tags=integration -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt: ## Format the code
	gofmt -w .

lint: ## gofmt, go vet, golangci-lint (includes gosec), govulncheck and gitleaks
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet -tags=integration ./...
	docker run --rm -v "$(CURDIR)":/src -w /src -v monthra-golangci-cache:/root/.cache $(GOLANGCI_IMAGE) golangci-lint run ./...
	$(MAKE) vuln
	gitleaks git --no-banner --redact .
	gitleaks git --no-banner --redact --pre-commit --staged .

vuln: ## Known vulnerabilities in reachable code (govulncheck)
	go tool govulncheck ./...

build: ## Build the production image
	docker build --target runtime --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):local .

staging-up: env network ## Start the homologation stack locally (production image, no debug tools)
	IMAGE_TAG=$(VERSION) $(COMPOSE_STG) up -d --build --wait

staging-down: ## Stop the homologation stack
	$(COMPOSE_STG) down
