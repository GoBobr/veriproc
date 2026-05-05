.PHONY: build test run fmt vet tidy clean

GO ?= go
PKG := ./...
BIN_DIR := bin
DAEMON := veriprocd

build:
	$(GO) build -o $(BIN_DIR)/$(DAEMON) ./cmd/$(DAEMON)

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
