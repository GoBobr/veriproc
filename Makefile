.PHONY: all build test test-short run fmt vet tidy clean help console

GO      ?= go
PKG     := ./...
BIN_DIR := bin
DAEMON  := veriprocd
CLI     := veriproc
CONSOLE := veriproc-console
WEBAPP_DIR := webapp
NODE    ?= node
NPM     ?= npm

# Collect all Go source files for dependency tracking.
GO_SRCS := $(shell find . -name '*.go' -not -path './vendor/*')

# Default target.
all: build

# ── binaries ────────────────────────────────────────────────────────────────

$(BIN_DIR)/$(DAEMON): $(GO_SRCS) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $@ ./cmd/$(DAEMON)

$(BIN_DIR)/$(CLI): $(GO_SRCS) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $@ ./cmd/$(CLI)

$(BIN_DIR)/$(CONSOLE): $(GO_SRCS) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $@ ./cmd/$(CONSOLE)

build: $(BIN_DIR)/$(DAEMON) $(BIN_DIR)/$(CLI) $(BIN_DIR)/$(CONSOLE)

# ── webapp ─────────────────────────────────────────────────────────────────

.PHONY: webapp-install webapp-build webapp-test

webapp-install:
	cd $(WEBAPP_DIR) && $(NPM) install --no-fund --no-audit

webapp-build:
	cd $(WEBAPP_DIR) && $(NPM) run build

webapp-test:
	cd $(WEBAPP_DIR) && $(NPM) test

# ── development ─────────────────────────────────────────────────────────────

test:
	$(GO) test -race -count=1 $(PKG)

test-short:
	$(GO) test -short -count=1 $(PKG)

run:
	$(GO) run ./cmd/$(DAEMON)

fmt:
	$(GO) fmt $(PKG)

vet:
	$(GO) vet $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)

# ── help ────────────────────────────────────────────────────────────────────

help:
	@echo "Usage: make [TARGET]"
	@echo ""
	@echo "Targets:"
	@echo "  all          Build both binaries (default)"
	@echo "  build        Build bin/$(DAEMON) and bin/$(CLI) (incremental)"
	@echo "  test         Run all tests with -race"
	@echo "  test-short   Run tests with -short (skip integration tests)"
	@echo "  run          Run $(DAEMON) via 'go run' (no binary written)"
	@echo "  fmt          Run 'go fmt' across all packages"
	@echo "  vet          Run 'go vet' across all packages"
	@echo "  tidy         Run 'go mod tidy'"
	@echo "  clean        Remove the bin/ directory"
	@echo "  help         Show this message"
	@echo ""
	@echo "Variables:"
	@echo "  GO           Go binary to use  (default: go)"
