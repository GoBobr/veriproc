.PHONY: all build test test-short run fmt vet tidy clean help console \
         docker-build docker-build-daemon docker-build-console \
         docker-push  docker-push-daemon  docker-push-console

GO      ?= go
PKG     := ./...
BIN_DIR := bin
DAEMON  := veriprocd
CLI     := veriproc
CONSOLE := veriproc-console
WEBAPP_DIR := webapp
NODE    ?= node
NPM     ?= npm

# ── versioning ──────────────────────────────────────────────────────────────
# Change MAKEFILE_VERSION here to update the baseline version for all components.
# If the current commit carries an exact git tag, that tag takes priority.
MAKEFILE_VERSION := 0.5-dev
GIT_TAG          := $(shell git describe --tags --exact-match 2>/dev/null)
GIT_COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE       := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION          := $(if $(GIT_TAG),$(GIT_TAG),$(MAKEFILE_VERSION))
MODULE           := github.com/gobobr/veriproc
LDFLAGS          := -X $(MODULE)/internal/version.Version=$(VERSION) \
                    -X $(MODULE)/internal/version.Commit=$(GIT_COMMIT) \
                    -X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)

# ── docker ───────────────────────────────────────────────────────────────────
# Override DOCKER_REGISTRY and IMAGE_PREFIX to push to your own registry.
# Example: make docker-build DOCKER_REGISTRY=registry.example.com/veriproc
DOCKER          ?= docker
DOCKER_REGISTRY ?= ghcr.io/gobobr
IMAGE_DAEMON    := $(DOCKER_REGISTRY)/veriproc
IMAGE_CONSOLE   := $(DOCKER_REGISTRY)/veriproc-console
# DOCKER_TAG and DOCKER_BUILDARGS use = (recursive expansion) so they pick up
# VERSION, GIT_COMMIT and BUILD_DATE after the versioning block below.
DOCKER_TAG       = $(VERSION)
DOCKER_BUILDARGS  = \
  --build-arg VERSION=$(VERSION) \
  --build-arg GIT_COMMIT=$(GIT_COMMIT) \
  --build-arg BUILD_DATE=$(BUILD_DATE)

# Collect all Go source files for dependency tracking.
GO_SRCS := $(shell find . -name '*.go' -not -path './vendor/*')

# Default target.
all: build

# ── binaries ────────────────────────────────────────────────────────────────

$(BIN_DIR)/$(DAEMON): $(GO_SRCS) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/$(DAEMON)

$(BIN_DIR)/$(CLI): $(GO_SRCS) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/$(CLI)

$(BIN_DIR)/$(CONSOLE): $(GO_SRCS) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/$(CONSOLE)

build: $(BIN_DIR)/$(DAEMON) $(BIN_DIR)/$(CLI) $(BIN_DIR)/$(CONSOLE)

# ── webapp ─────────────────────────────────────────────────────────────────

.PHONY: webapp-install webapp-build webapp-test

webapp-install:
	cd $(WEBAPP_DIR) && $(NPM) install --no-fund --no-audit

webapp-build:
	cd $(WEBAPP_DIR) && VITE_APP_VERSION=$(VERSION) VITE_APP_COMMIT=$(GIT_COMMIT) $(NPM) run build

webapp-test:
	cd $(WEBAPP_DIR) && $(NPM) test

# ── docker ──────────────────────────────────────────────────────────────────

.PHONY: docker-build-daemon docker-build-console docker-build
.PHONY: docker-push-daemon  docker-push-console  docker-push

docker-build-daemon:
	$(DOCKER) build $(DOCKER_BUILDARGS) \
	  -f docker/Dockerfile.veriproc \
	  -t $(IMAGE_DAEMON):$(DOCKER_TAG) \
	  -t $(IMAGE_DAEMON):latest \
	  .

docker-build-console:
	$(DOCKER) build $(DOCKER_BUILDARGS) \
	  -f docker/Dockerfile.console \
	  -t $(IMAGE_CONSOLE):$(DOCKER_TAG) \
	  -t $(IMAGE_CONSOLE):latest \
	  .

docker-build: docker-build-daemon docker-build-console

docker-push-daemon:
	@$(DOCKER) image inspect $(IMAGE_DAEMON):$(DOCKER_TAG) >/dev/null 2>&1 || \
		{ echo "ERROR: $(IMAGE_DAEMON):$(DOCKER_TAG) not found. Run 'make docker-build-daemon' first."; exit 1; }
	$(DOCKER) push $(IMAGE_DAEMON):$(DOCKER_TAG)
	$(DOCKER) push $(IMAGE_DAEMON):latest

docker-push-console:
	@$(DOCKER) image inspect $(IMAGE_CONSOLE):$(DOCKER_TAG) >/dev/null 2>&1 || \
		{ echo "ERROR: $(IMAGE_CONSOLE):$(DOCKER_TAG) not found. Run 'make docker-build-console' first."; exit 1; }
	$(DOCKER) push $(IMAGE_CONSOLE):$(DOCKER_TAG)
	$(DOCKER) push $(IMAGE_CONSOLE):latest

docker-push: docker-push-daemon docker-push-console

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
	@echo "Go binaries:"
	@echo "  all              Build all Go binaries (default)"
	@echo "  build            Build bin/$(DAEMON), bin/$(CLI), bin/$(CONSOLE) (incremental)"
	@echo "  run              Run $(DAEMON) via 'go run' (no binary written)"
	@echo "  test             Run all tests with -race"
	@echo "  test-short       Run tests with -short (skip integration tests)"
	@echo "  fmt              Run 'go fmt' across all packages"
	@echo "  vet              Run 'go vet' across all packages"
	@echo "  tidy             Run 'go mod tidy'"
	@echo "  clean            Remove the bin/ directory"
	@echo ""
	@echo "Webapp (operator UI in $(WEBAPP_DIR)/):"
	@echo "  webapp-install   Install npm dependencies  (run once, or after package.json changes)"
	@echo "  webapp-build     Build the frontend bundle into $(WEBAPP_DIR)/dist/"
	@echo "  webapp-test      Run Vitest unit tests for the webapp"
	@echo ""
	@echo "  Typical first-time build:"
	@echo "    make webapp-install webapp-build"
	@echo ""
	@echo "  If Node.js is not on PATH, prepend the repo-local toolchain first:"
	@echo "    export PATH=\"\$$PWD/.tools/node/bin:\$$PATH\""
	@echo "    make webapp-install webapp-build"
	@echo ""
	@echo "  Or use a Python virtualenv with nodeenv:"
	@echo "    python3 -m venv .venv && . .venv/bin/activate"
	@echo "    pip install nodeenv && nodeenv -p --node=20.11.1"
	@echo "    make webapp-install webapp-build"
	@echo ""
	@echo "Docker images (docker/ Dockerfiles):"
	@echo "  docker-build           Build both Docker images"
	@echo "  docker-build-daemon    Build $(IMAGE_DAEMON):$(DOCKER_TAG)"
	@echo "  docker-build-console   Build $(IMAGE_CONSOLE):$(DOCKER_TAG)"
	@echo "  docker-push            Build and push both images to the registry"
	@echo "  docker-push-daemon     Build and push $(IMAGE_DAEMON)"
	@echo "  docker-push-console    Build and push $(IMAGE_CONSOLE)"
	@echo ""
	@echo "  Images are tagged with VERSION and 'latest'."
	@echo "  For a release: git tag vX.Y.Z && make docker-build"
	@echo "  Override the registry: make docker-build DOCKER_REGISTRY=registry.example.com/veriproc"
	@echo ""
	@echo "Versioning:"
	@echo "  MAKEFILE_VERSION    Baseline version string  (currently: $(MAKEFILE_VERSION))"
	@echo "  VERSION             Resolved version: git tag if HEAD is tagged, else MAKEFILE_VERSION"
	@echo "                      Currently: $(VERSION)"
	@echo "  GIT_COMMIT          Short git commit embedded in every binary (currently: $(GIT_COMMIT))"
	@echo "  To release: git tag vX.Y.Z && make build webapp-build"
	@echo ""
	@echo "Variables:"
	@echo "  GO      Go binary to use  (default: go)"
	@echo "  NPM     npm binary to use (default: npm)"
