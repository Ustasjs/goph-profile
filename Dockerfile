# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=docker
ARG BUILD_DATE=unknown
ENV CGO_ENABLED=0
RUN go build -ldflags "-X main.buildVersion=${VERSION} -X main.buildDate=${BUILD_DATE}" -o /out/server ./cmd/server && \
    go build -ldflags "-X main.buildVersion=${VERSION} -X main.buildDate=${BUILD_DATE}" -o /out/worker ./cmd/worker

# Runtime stage. The web UI is embedded into the server binary, so
# nothing besides the binaries is copied.
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/server /out/worker ./

# The default command runs the API server; compose overrides it with
# /app/worker for the worker service.
CMD ["/app/server"]
