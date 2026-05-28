# Multi-stage build for llm-gateway-go data plane
# Build with: docker build -t kx-llm-gateway-go:latest .

# ── Build stage ──────────────────────────────────────────────────────────────
FROM --platform=linux/amd64 golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
ARG GOTOOLCHAIN=auto
RUN GOTOOLCHAIN=auto go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=auto \
    go build -ldflags="-s -w" -o /llm-gateway-go ./cmd/gateway

# ── Runtime stage ───────────────────────────────────────────────────────────
FROM --platform=linux/amd64 alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1001 llmgw

COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go

USER llmgw

EXPOSE 8781

ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
