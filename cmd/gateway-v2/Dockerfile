# Local-buildable Dockerfile for cmd/gateway-v2 (R1.12 demo entry point).
#
# Production or mirrored builds can override GO_BASE_IMAGE with an approved,
# exact Go 1.27.4 image reference.
#
# Build:
#   docker compose -f docker-compose.dev-research.yml build gateway-v2
#   # 或生产:
#   docker build --build-arg GO_BASE_IMAGE=registry.kxpms.cn/kx-base/golang:1.27.4-alpine -t kx-gateway-v2 ./cmd/gateway-v2
#
# 与根 Dockerfile 对齐：默认使用公开 Go 1.27.4 Alpine 构建器，私有环境通过
# GO_BASE_IMAGE 覆盖为已验证镜像。
ARG GO_BASE_IMAGE=golang:1.27.4-alpine
ARG REGISTRY=""
FROM --platform=linux/amd64 ${GO_BASE_IMAGE} AS builder

WORKDIR /src
USER root
ENV GOPATH=/tmp/go
RUN mkdir -p /tmp/go /tmp/go/pkg /tmp/go/bin
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
ARG GOTOOLCHAIN=local
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ENV GOTOOLCHAIN=${GOTOOLCHAIN}

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -a -ldflags="-s -w" -o /llm-gateway-go ./cmd/gateway-v2

FROM --platform=linux/amd64 ${REGISTRY}kx-base:go-vue-alpine-slim-runtime
WORKDIR /
COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
USER appuser
EXPOSE 8782
ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
