# Load .env if it exists. All variables from it become available to make targets.
-include .env
export

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.buildVersion=$(VERSION) -X main.buildDate=$(BUILD_DATE)

.PHONY: run run-worker test lint build clean

run:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

test:
	go test ./... -count=1

lint:
	golangci-lint run ./...

# Build the server and worker binaries with version info.
build:
	go build -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o bin/worker ./cmd/worker

clean:
	rm -rf bin
