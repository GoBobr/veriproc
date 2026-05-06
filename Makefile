.PHONY: all build test test-short run fmt vet tidy clean help

GO      ?= go
PKG     := ./...
BIN_DIR := bin
DAEMON  := veriprocd
CLI     := veriproc

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

build: $(BIN_DIR)/$(DAEMON) $(BIN_DIR)/$(CLI)

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
