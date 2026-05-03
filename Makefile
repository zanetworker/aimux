.PHONY: build run clean test install lint build-mcp build-agent-claude push-agent-claude build-agent-gemini push-agent-gemini web-build web-dev build-all

BINARY=aimux
REGISTRY=quay.io/azaalouk
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

web-build:
	cd web && pnpm install && pnpm build
	cp -r web/dist/* internal/frontend/web/dist/

web-dev:
	cd web && pnpm dev

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/aimux

build-all: web-build build

run: build
	./$(BINARY)

test:
	go test ./... -v

install: build
	cp $(BINARY) /usr/local/bin/

lint:
	golangci-lint run ./...

build-mcp:
	go build -o bin/k8s-agents-mcp ./cmd/mcp

build-agent-claude:
	podman build --platform linux/amd64 \
		-t $(REGISTRY)/agent-claude:latest \
		-f runtime/agents/claude/Dockerfile .

push-agent-claude: build-agent-claude
	podman push $(REGISTRY)/agent-claude:latest

build-agent-gemini:
	podman build --platform linux/amd64 \
		-t $(REGISTRY)/agent-gemini:latest \
		-f runtime/agents/gemini/Dockerfile .

push-agent-gemini: build-agent-gemini
	podman push $(REGISTRY)/agent-gemini:latest

clean:
	rm -f $(BINARY) bin/k8s-agents-mcp
