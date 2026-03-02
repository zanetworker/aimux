.PHONY: build run clean test install lint

BINARY=aimux
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/aimux

run: build
	./$(BINARY)

test:
	go test ./... -v

install: build
	cp $(BINARY) /usr/local/bin/

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY)
