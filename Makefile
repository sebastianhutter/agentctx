MODULE := github.com/sebastianhutter/agentctx
BINARY := agentctx
BUILD_DIR := bin
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

# CGO is required: the menu bar app is Fyne, which binds to Cocoa. That also
# makes this macOS-only, which matches where it is used.
CGO_ENABLED := 1

.PHONY: all build test test-v lint app dmg clean tidy

all: build

build:
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/agentctx

test:
	CGO_ENABLED=$(CGO_ENABLED) go test -race -count=1 ./...

test-v:
	CGO_ENABLED=$(CGO_ENABLED) go test -race -count=1 -v ./...

lint:
	golangci-lint run ./...

# macOS .app bundle — a menu bar app, installed by dragging it to Applications.
app: build
	@echo "Building macOS .app bundle..."
	VERSION=$(VERSION) ./scripts/build-app.sh

# macOS DMG installer: the app plus an Applications symlink.
dmg: app
	@echo "Building DMG installer..."
	./scripts/build-dmg.sh

clean:
	rm -rf $(BUILD_DIR)

tidy:
	go mod tidy
