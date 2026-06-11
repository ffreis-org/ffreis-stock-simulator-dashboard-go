.DEFAULT_GOAL := help

SHELL := /usr/bin/env bash

CONTAINER_COMMAND ?= podman
COMPOSE_COMMAND ?= compose
DASHBOARD_IMAGE ?= ffreis-stock-dashboard:local
DASHBOARD_BUILDER_IMAGE ?= golang:1.25.8-alpine
DASHBOARD_RUNTIME_IMAGE ?= gcr.io/distroless/static-debian12:nonroot

GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
GITLEAKS ?= gitleaks
GOVULNCHECK ?= govulncheck

LEFTHOOK_VERSION ?= 1.7.10

MUTATION_PACKAGES ?= ./internal/...
MUTATION_THRESHOLD ?= 60
COVERAGE_MIN      ?= 51
COVERAGE_PACKAGES ?= ./...
LEFTHOOK_DIR ?= $(CURDIR)/.bin
LEFTHOOK_BIN ?= $(LEFTHOOK_DIR)/lefthook

.PHONY: mutation-test help \
	docker-build compose-up compose-down \
	fmt-check lint test build-check openapi-drift-check secrets-scan-staged quality-gates hook-generated-drift \
	lefthook-bootstrap lefthook-install lefthook-run lefthook

## mutation-test: run mutation testing with gremlins (slow — intended for CI/weekly)
mutation-test: ## Run mutation testing with gremlins (slow — CI only)
	@which gremlins >/dev/null 2>&1 || go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
	gremlins unleash --threshold-efficacy $(MUTATION_THRESHOLD) $(MUTATION_PACKAGES)

help: ## Show help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

docker-build: ## Build dashboard image
	$(CONTAINER_COMMAND) build \
		--build-arg BUILDER_IMAGE=$(DASHBOARD_BUILDER_IMAGE) \
		--build-arg RUNTIME_IMAGE=$(DASHBOARD_RUNTIME_IMAGE) \
		-t $(DASHBOARD_IMAGE) .

compose-up: ## Start simulator + dashboard stack
	$(CONTAINER_COMMAND) $(COMPOSE_COMMAND) up --build

compose-down: ## Stop simulator + dashboard stack
	$(CONTAINER_COMMAND) $(COMPOSE_COMMAND) down --remove-orphans

fmt-check: ## Fail if Go files are not gofmt-formatted
	@./scripts/hooks/check_required_tools.sh $(GOFMT)
	@out="$$(find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*' -print0 | xargs -0 -r $(GOFMT) -l)"; \
	if [ -n "$$out" ]; then \
		echo "Unformatted Go files:"; \
		echo "$$out"; \
		echo "Run: $(GOFMT) -w <files>"; \
		exit 1; \
	fi

lint: ## Run go vet and optional golangci-lint
	go vet ./...
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		$(GOLANGCI_LINT) run; \
	else \
		echo "$(GOLANGCI_LINT) not found; running go vet only for dashboard lint target."; \
	fi

test: ## Run tests
	go test -race -shuffle=on ./...

build-check: ## Validate dashboard build
	go build ./cmd/dashboard

openapi-drift-check: ## Verify OpenAPI route surface matches dashboard handlers
	go test ./cmd/dashboard -run TestOpenAPISpecCoversDashboardRoutes -count=1

secrets-scan-staged: ## Scan staged diff for secrets
	@./scripts/hooks/check_required_tools.sh $(GITLEAKS)
	$(GITLEAKS) protect --staged --redact

## coverage-gate: run tests with coverage; fail if below COVERAGE_MIN
coverage-gate:
	@COVERAGE_MIN="$(COVERAGE_MIN)" COVERAGE_PACKAGES="$(COVERAGE_PACKAGES)" \
		./scripts/hooks/check_coverage_gate.sh

quality-gates: ## Run strict pre-push dashboard quality gates
	@./scripts/hooks/check_required_tools.sh $(GOVULNCHECK)
	$(MAKE) lint
	$(MAKE) openapi-drift-check
	$(MAKE) test
	$(MAKE) build-check
	$(MAKE) coverage-gate
	$(GOVULNCHECK) ./...

hook-generated-drift: ## Run generate target if present and fail on drift
	@set -euo pipefail; \
	if $(MAKE) -n generate >/dev/null 2>&1; then \
		$(MAKE) generate; \
		if ! git diff --quiet -- .; then \
			echo "Generated files are out of date. Run 'make generate' and commit updates."; \
			git status --short; \
			exit 1; \
		fi; \
	else \
		echo "No 'generate' target found; skipping generated drift check."; \
	fi

lefthook-bootstrap: ## Download lefthook binary into ./.bin
	LEFTHOOK_VERSION="$(LEFTHOOK_VERSION)" BIN_DIR="$(LEFTHOOK_DIR)" bash ./scripts/bootstrap_lefthook.sh

lefthook-install: lefthook-bootstrap ## Install git hooks if missing
	@if [ -x "$(LEFTHOOK_BIN)" ] && [ -x ".git/hooks/pre-commit" ] && [ -x ".git/hooks/pre-push" ] && [ -x ".git/hooks/commit-msg" ]; then \
		echo "lefthook hooks already installed"; \
		exit 0; \
	fi
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" install

lefthook-run: lefthook-bootstrap ## Run hooks (pre-commit + commit-msg + pre-push)
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-commit
	@tmp_msg="$$(mktemp)"; \
	echo "chore(hooks): validate commit-msg hook" > "$$tmp_msg"; \
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run commit-msg -- "$$tmp_msg"; \
	rm -f "$$tmp_msg"
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-push

lefthook: lefthook-bootstrap lefthook-install lefthook-run ## Install hooks and run them

PLATFORM_STANDARDS_SHA ?= 3c787edb4e96ddea2e86b2add2c32139685e8db7  # v1.2.1
PLATFORM_STANDARDS_RAW ?= https://raw.githubusercontent.com/FelipeFuhr/ffreis-platform-standards

install-act: ## Download pinned act binary into .bin/
	@mkdir -p scripts
	@curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(PLATFORM_STANDARDS_SHA)/scripts/install_act.sh" \
		-o scripts/install_act.sh && chmod +x scripts/install_act.sh
	@bash ./scripts/install_act.sh

ci-local: ## Run workflows locally via act (GH Actions quota fallback). Args via ARGS=...
	@mkdir -p scripts
	@curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(PLATFORM_STANDARDS_SHA)/scripts/run-ci-local.sh" \
		-o scripts/run-ci-local.sh && chmod +x scripts/run-ci-local.sh
	@PATH="$(CURDIR)/.bin:$(PATH)" bash ./scripts/run-ci-local.sh $(ARGS)
