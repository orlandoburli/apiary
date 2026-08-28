SRC     := src
SDK     := sdk
BINARY  := apiary
BIN_DIR := bin
PKG     := github.com/orlandoburli/apiary/internal/version
PLUGINPKG := github.com/orlandoburli/apiary/internal/plugin

# The minisign public key the official plugin registry index is signed with.
# Empty means the index is used unverified, and every registry command says so.
REGISTRY_KEY ?= $(APIARY_REGISTRY_PUBLIC_KEY)

VERSION  := $(shell git describe --tags --match "v*" --always --dirty 2>/dev/null || echo "0.1.0-dev")
LDFLAGS  := -ldflags "-X $(PKG).Version=$(VERSION) -X $(PLUGINPKG).OfficialRegistryPublicKey=$(REGISTRY_KEY)"

.DEFAULT_GOAL := help

# ── build ──────────────────────────────────────────────────────────────────────

.PHONY: build
build: ## Build the apiary binary into bin/
	@mkdir -p $(BIN_DIR)
	cd $(SDK) && go build ./...
	cd $(SRC) && go build $(LDFLAGS) -o ../$(BIN_DIR)/$(BINARY) ./cmd/apiary
	@echo "→ $(BIN_DIR)/$(BINARY)  ($(VERSION))"

.PHONY: install
install: ## Install apiary to $$GOPATH/bin (makes it available on PATH)
	cd $(SRC) && go install $(LDFLAGS) ./cmd/apiary
	@echo "→ installed to $$(go env GOPATH)/bin/$(BINARY)  ($(VERSION))"

# ── test ───────────────────────────────────────────────────────────────────────

.PHONY: test
test: ## Run all tests (daemon module + Go SDK module + Python SDK)
	cd $(SDK) && go test ./...
	cd $(SRC) && go test ./...
	cd $(SDK)/python && python3 -m unittest discover -s tests

.PHONY: test-python
test-python: ## Run the Python SDK's unit tests (stdlib only)
	cd $(SDK)/python && python3 -m unittest discover -s tests

.PHONY: conformance
conformance: ## Run the plugin protocol conformance kit against every example we ship
	$(SDK)/conformance/check-examples.sh

.PHONY: registry-check
registry-check: ## Verify every plugin registry entry against its published artifacts
	cd $(SRC) && go run ./cmd/apiary-registry check --dir ../registry \
		--conformance-runner ../sdk/conformance/run.py --results ../$(BIN_DIR)/conformance.json

.PHONY: registry-build
registry-build: ## Compile the registry entries into docs/registry/v1/index.json
	cd $(SRC) && go run ./cmd/apiary-registry build --dir ../registry \
		--results ../$(BIN_DIR)/conformance.json --out ../docs/registry/v1/index.json

.PHONY: registry-sign
registry-sign: ## Sign docs/registry/v1/index.json with minisign (needs MINISIGN_KEY_FILE)
	@test -n "$(MINISIGN_KEY_FILE)" || { echo "set MINISIGN_KEY_FILE=<path to the minisign secret key>"; exit 1; }
	minisign -S -s "$(MINISIGN_KEY_FILE)" -m docs/registry/v1/index.json \
		-t "apiary registry index $(shell git rev-parse --short HEAD)"
	@echo "→ docs/registry/v1/index.json.minisig"

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
tidy: ## Run go mod tidy (both modules)
	cd $(SDK) && go mod tidy
	cd $(SRC) && go mod tidy

.PHONY: vet
vet: ## Run go vet (both modules)
	cd $(SDK) && go vet ./...
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
