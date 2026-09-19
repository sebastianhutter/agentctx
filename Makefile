MODULE := github.com/sebastianhutter/agentctx
BINARY := agentctx
BUILD_DIR := bin
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test test-v lint dist clean tidy install

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/agentctx

test:
	go test -race -count=1 ./...

test-v:
	go test -race -count=1 -v ./...

lint:
	golangci-lint run ./...

# dist cross-compiles every supported target. Pure Go with no CGO, so this needs
# nothing but the toolchain — one runner builds all of them.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

dist:
	@mkdir -p $(BUILD_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "building $(BINARY)-$$os-$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-$$os-$$arch ./cmd/agentctx || exit 1; \
	done

clean:
	rm -rf $(BUILD_DIR)

tidy:
	go mod tidy

install: build
	install -m 0755 $(BUILD_DIR)/$(BINARY) $(HOME)/.local/bin/$(BINARY)
