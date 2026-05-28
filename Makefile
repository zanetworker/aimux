.PHONY: build run clean test test-integration test-all install lint build-mcp build-agent-claude push-agent-claude build-agent-gemini push-agent-gemini web-build web-dev build-all

BINARY=aimux
REGISTRY=quay.io/azaalouk
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

web-build:
	cd web && pnpm install && pnpm build
	rm -rf internal/frontend/web/dist
	mkdir -p internal/frontend/web/dist
	cp -r web/dist/* internal/frontend/web/dist/

web-dev:
	cd web && pnpm dev

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/aimux

build-all: web-build build

run: build
	./$(BINARY)

test:
	go test ./... -timeout 30s

test-integration:
	go test -tags integration ./... -timeout 30s

test-all:
	go test -tags "integration e2e" ./... -timeout 60s

install: build
	cp $(BINARY) /usr/local/bin/

lint:
	golangci-lint run ./...

build-mcp:
	go build -o bin/k8s-agents-mcp ./cmd/mcp

build-agent-claude:
	podman build --platform linux/amd64 \
		-t $(REGISTRY)/agent-claude:$(VERSION) \
		-t $(REGISTRY)/agent-claude:latest \
		-f runtime/agents/claude/Dockerfile .

push-agent-claude: build-agent-claude
	podman push $(REGISTRY)/agent-claude:$(VERSION)
	podman push $(REGISTRY)/agent-claude:latest

build-agent-gemini:
	podman build --platform linux/amd64 \
		-t $(REGISTRY)/agent-gemini:$(VERSION) \
		-t $(REGISTRY)/agent-gemini:latest \
		-f runtime/agents/gemini/Dockerfile .

push-agent-gemini: build-agent-gemini
	podman push $(REGISTRY)/agent-gemini:$(VERSION)
	podman push $(REGISTRY)/agent-gemini:latest

clean:
	rm -f $(BINARY) bin/k8s-agents-mcp
	rm -rf internal/frontend/web/dist
