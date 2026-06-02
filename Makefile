SRC     := src
BINARY  := apiary
BIN_DIR := bin
PKG     := github.com/orlandoburli/apiary/internal/version

VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0-dev")
LDFLAGS  := -ldflags "-X $(PKG).Version=$(VERSION)"

.DEFAULT_GOAL := help

# ── build ──────────────────────────────────────────────────────────────────────

.PHONY: build
build: ## Build the apiary binary into bin/
	@mkdir -p $(BIN_DIR)
	cd $(SRC) && go build $(LDFLAGS) -o ../$(BIN_DIR)/$(BINARY) ./cmd/apiary
	@echo "→ $(BIN_DIR)/$(BINARY)  ($(VERSION))"

.PHONY: install
install: ## Install apiary to $$GOPATH/bin (makes it available on PATH)
	cd $(SRC) && go install $(LDFLAGS) ./cmd/apiary
	@echo "→ installed to $$(go env GOPATH)/bin/$(BINARY)  ($(VERSION))"

# ── test ───────────────────────────────────────────────────────────────────────

.PHONY: test
test: ## Run all tests
	cd $(SRC) && go test ./...

.PHONY: test-verbose
test-verbose: ## Run all tests with per-test output
	cd $(SRC) && go test -v ./...

.PHONY: test-cover
test-cover: ## Run tests and open an HTML coverage report
	@mkdir -p $(BIN_DIR)
	cd $(SRC) && go test -coverprofile=../$(BIN_DIR)/coverage.out ./...
	go tool cover -html=$(BIN_DIR)/coverage.out

# ── code quality ───────────────────────────────────────────────────────────────

.PHONY: check
check: build test ## Build + test (use in CI)

.PHONY: tidy
tidy: ## Run go mod tidy
	cd $(SRC) && go mod tidy

.PHONY: vet
vet: ## Run go vet
	cd $(SRC) && go vet ./...

# ── clean ──────────────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

# ── help ───────────────────────────────────────────────────────────────────────

.PHONY: help
help: ## Show available targets
	@echo "Usage: make <target>\n"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
