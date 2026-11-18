.PHONY: build run clean test test-integration test-all install lint lint-linux build-mcp build-agent-claude push-agent-claude web-build web-dev build-all

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

# 300s: the slow packages (cmd, mcpproxy, remoteapi) pass in seconds alone
# but went past 30s under parallel load.
test:
	go test ./... -timeout 300s

test-integration:
	go test -tags integration ./... -timeout 30s

test-all:
	go test -tags "integration e2e" ./... -timeout 60s

install: build
	cp $(BINARY) /usr/local/bin/

lint:
	golangci-lint run ./...

# CI lints on Linux: build tags (peercred, procident, controllock) differ.
# New issues since LINT_BASE (origin/main) only, so old findings in untouched
# files (a newer local golangci-lint finds more) do not fail it; without that
# ref (a single-branch clone) it lints everything.
LINT_BASE ?= origin/main
lint-linux:
	@if git rev-parse -q --verify "$(LINT_BASE)^{commit}" >/dev/null; then \
		GOOS=linux golangci-lint run --new-from-rev=$(LINT_BASE) ./...; \
	else \
		echo "lint-linux: $(LINT_BASE) not found; linting everything" >&2; \
		GOOS=linux golangci-lint run ./...; \
	fi

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

clean:
	rm -f $(BINARY) bin/k8s-agents-mcp
	rm -rf internal/frontend/web/dist
