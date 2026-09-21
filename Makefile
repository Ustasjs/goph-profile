# Load .env if it exists. All variables from it become available to make targets.
-include .env
export

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.buildVersion=$(VERSION) -X main.buildDate=$(BUILD_DATE)

.PHONY: run run-worker test test-integration cover lint build clean

# Connection strings matching docker-compose defaults. Used by the
# integration targets; plain "make test" skips integration suites
# unless .env already provides these.
INTEGRATION_ENV := \
	DATABASE_DSN=postgres://gophprofile:gophprofile@localhost:5434/gophprofile?sslmode=disable \
	S3_ENDPOINT=localhost:9000 \
	S3_ACCESS_KEY=minioadmin \
	S3_SECRET_KEY=minioadmin \
	RABBITMQ_URL=amqp://guest:guest@localhost:5672/

run:
	go run ./cmd/server

run-worker:
	go run ./cmd/worker

test:
	go test ./... -count=1

# Integration and e2e suites against the docker-compose services.
test-integration:
	$(INTEGRATION_ENV) go test ./... -count=1

# Coverage across unit and integration tests.
cover:
	$(INTEGRATION_ENV) go test ./... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

# Build the server and worker binaries with version info.
build:
	go build -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o bin/worker ./cmd/worker

clean:
	rm -rf bin
