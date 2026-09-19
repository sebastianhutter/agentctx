MODULE := github.com/sebastianhutter/agentctx
BINARY := agentctx
BUILD_DIR := bin
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test test-v lint check-names clean tidy install

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/agentctx

test:
	go test -race -count=1 ./...

test-v:
	go test -race -count=1 -v ./...

# check-names runs before the linter on purpose: a leaked customer name is a
# worse defect than anything golangci-lint reports, and it is permanent once
# committed. See Rule 0 in CLAUDE.md.
lint: check-names
	golangci-lint run ./...

check-names:
	./scripts/check-names.sh

clean:
	rm -rf $(BUILD_DIR)

tidy:
	go mod tidy

install: build
	install -m 0755 $(BUILD_DIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
