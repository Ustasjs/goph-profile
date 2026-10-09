# Load .env if it exists. All variables from it become available to make targets.
-include .env
export

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.buildVersion=$(VERSION) -X main.buildDate=$(BUILD_DATE)

.PHONY: run run-worker test test-integration cover lint build clean \
	image helm-install-local helm-uninstall monitoring-install \
	swagger helm-validate

HELM_CHART := deploy/helm/gophprofile
HELM_NAMESPACE := gophprofile
# kube-prometheus-stack pin; bump deliberately, not with the repo.
KPS_VERSION := 91.9.0

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

# Regenerate the OpenAPI spec (docs/) from the swag annotations.
swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init \
		-g cmd/server/main.go -o docs --parseInternal

# Chart checks: helm lint plus kubeconform over both value sets. The
# local set renders from the committed example (its change-me
# placeholders are enough — validation needs shapes, not secrets).
# -ignore-missing-schemas covers the CRDs (ServiceMonitor, Middleware).
helm-validate:
	helm lint $(HELM_CHART)
	helm template gophprofile $(HELM_CHART) -f $(HELM_CHART)/values-local.example.yaml \
		| kubeconform -strict -ignore-missing-schemas -summary
	helm template gophprofile $(HELM_CHART) -f $(HELM_CHART)/values-prod.yaml \
		| kubeconform -strict -ignore-missing-schemas -summary

# Build the server and worker binaries with version info.
build:
	go build -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o bin/worker ./cmd/worker

clean:
	rm -rf bin

# Build the image where Rancher Desktop's k3s can see it without a
# registry. With the moby engine (default here) k3s runs through
# cri-dockerd and shares dockerd's image store, so a plain docker
# build is enough. With the containerd engine build into the k8s.io
# namespace instead: nerdctl --namespace k8s.io build -t gophprofile:local .
# The context is pinned: with Docker Desktop also installed, the
# default context would build into the wrong daemon.
image:
	docker --context rancher-desktop build \
		--build-arg VERSION=$(VERSION) --build-arg BUILD_DATE=$(BUILD_DATE) \
		-t gophprofile:local .

helm-install-local:
	@test -f $(HELM_CHART)/values-local.yaml || { \
		echo "no $(HELM_CHART)/values-local.yaml:"; \
		echo "  cp $(HELM_CHART)/values-local.example.yaml $(HELM_CHART)/values-local.yaml"; \
		echo "and set the passwords in it (the file is gitignored)."; exit 1; }
	helm upgrade --install gophprofile $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		-f $(HELM_CHART)/values-local.yaml \
		--wait --timeout 5m

helm-uninstall:
	helm uninstall gophprofile --namespace $(HELM_NAMESPACE)

# Prometheus Operator + Prometheus + Grafana, then the sprint-2
# dashboards as a sidecar-watched ConfigMap.
monitoring-install:
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
	helm repo update prometheus-community
	helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
		--namespace monitoring --create-namespace --version $(KPS_VERSION) \
		-f deploy/k8s/monitoring-values.yaml --wait --timeout 10m
	kubectl -n monitoring create configmap gophprofile-dashboards \
		--from-file=deploy/grafana/dashboards/ --dry-run=client -o yaml | kubectl apply -f -
	kubectl -n monitoring label configmap gophprofile-dashboards grafana_dashboard="1" --overwrite
