.PHONY: help build run test lint dev migrate-up migrate-down clean

VERSION ?= 0.1.0
BINARY = bin/worktrack-server
BINARY_WIN = bin/worktrack-server.exe

help:
	@echo "Targets:"
	@echo "  build         - build server binary"
	@echo "  build-windows - build for Windows"
	@echo "  run           - run server (uses .env)"
	@echo "  dev           - run with file-watch"
	@echo "  test          - run tests"
	@echo "  lint          - run golangci-lint"
	@echo "  migrate-up    - apply migrations"
	@echo "  migrate-down  - rollback last migration"

build:
	go build -ldflags="-s -w -X main.Version=$(VERSION)" -o $(BINARY) ./cmd/server

build-windows:
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.Version=$(VERSION)" -o $(BINARY_WIN) ./cmd/server

run: build
	./$(BINARY)

dev:
	go run ./cmd/server

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

migrate-up:
	@command -v migrate >/dev/null || (echo "install: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest" && exit 1)
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

clean:
	rm -rf bin/
