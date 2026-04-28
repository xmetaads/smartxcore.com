.PHONY: help build build-windows test lint clean

VERSION ?= 0.1.0
BINARY_WIN = bin/worktrack-agent.exe

help:
	@echo "Targets:"
	@echo "  build         - build agent for current OS"
	@echo "  build-windows - build agent for Windows (signed in CI)"
	@echo "  test          - run tests"
	@echo "  lint          - run golangci-lint"

build:
	go build -ldflags="-s -w -X main.Version=$(VERSION)" -o bin/worktrack-agent ./cmd/agent

build-windows:
	GOOS=windows GOARCH=amd64 go build \
		-ldflags="-s -w -X main.Version=$(VERSION) -H=windowsgui" \
		-o $(BINARY_WIN) ./cmd/agent

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
