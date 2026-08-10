# JustRAG developer entrypoints.
#
# Every target here mirrors a step in .github/workflows/ci.yml so a local run
# and a CI run cannot silently diverge. When you change a command in one place,
# change it in the other — the pinned tool versions below are the same ones CI
# installs.
#
# Quick start:
#   make check      # what CI runs on every PR, minus the DB-backed suites
#   make ci         # everything, including integration tests (needs a DB)

GO_BACKEND   := go-backend
WEB          := web

# Pinned to match .github/workflows/ci.yml. Bump both together.
GOLANGCI_VERSION   := v2.1.6
GOVULNCHECK_VERSION := v1.6.0

# Integration tests are DB-backed and opt in via the `integration` build tag.
INTEGRATION_PKGS := ./internal/cascade/... ./internal/vector/... ./internal/kg/... ./internal/tabular/... ./internal/processor/...

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Go backend
# ---------------------------------------------------------------------------

.PHONY: build
build: ## Build server, worker, migrate, eval
	cd $(GO_BACKEND) && go build ./cmd/server ./cmd/worker ./cmd/migrate ./cmd/eval

.PHONY: vet
vet: ## go vet ./...
	cd $(GO_BACKEND) && go vet ./...

.PHONY: fmt
fmt: ## Rewrite Go sources with gofmt
	cd $(GO_BACKEND) && gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is not gofmt-clean
	@cd $(GO_BACKEND) && out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: ## golangci-lint (installs the CI-pinned version on demand)
	cd $(GO_BACKEND) && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run

.PHONY: vulncheck
vulncheck: ## govulncheck (installs the CI-pinned version on demand)
	cd $(GO_BACKEND) && go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: test
test: ## Unit tests with the race detector
	cd $(GO_BACKEND) && go test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## DB-backed integration tests (needs Postgres + pgvector; see README env vars)
	cd $(GO_BACKEND) && go test -tags integration -race -count=1 $(INTEGRATION_PKGS)

.PHONY: bench
bench: ## Benchmark smoke run (compile + one iteration each)
	cd $(GO_BACKEND) && go test -run='^$$' -bench=. -benchtime=1x -benchmem ./...

.PHONY: migrate
migrate: ## Apply pending migrations
	cd $(GO_BACKEND) && go run ./cmd/migrate

.PHONY: migrate-status
migrate-status: ## List applied / pending migrations
	cd $(GO_BACKEND) && go run ./cmd/migrate --status

# ---------------------------------------------------------------------------
# Frontend
# ---------------------------------------------------------------------------

.PHONY: web-install
web-install: ## npm ci for the web workspace
	npm ci --workspace=$(WEB)

.PHONY: web-lint
web-lint: ## ESLint (advisory in CI — pre-existing violations are a tracked backlog)
	cd $(WEB) && npm run lint

.PHONY: web-test
web-test: ## Vitest
	cd $(WEB) && npm run test

.PHONY: web-build
web-build: ## Production build (tsc -b + vite build)
	cd $(WEB) && npm run build

.PHONY: csp-check
csp-check: ## Detect CSP inline-script hash drift against the built index.html
	node scripts/check_csp_hash.mjs

# ---------------------------------------------------------------------------
# Aggregates
# ---------------------------------------------------------------------------

.PHONY: check
check: vet fmt-check lint vulncheck test ## Fast pre-push gate (no DB required)

.PHONY: ci
ci: check test-integration bench web-test web-build csp-check ## Everything CI runs
