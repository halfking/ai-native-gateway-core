# Local-buildable Dockerfile for cmd/gateway-v2 (R1.12 demo entry point).
#
# 与根 Dockerfile 对齐：默认 REGISTRY="" 走本地缓存的 kx-base:go-vue-amd64，
# 生产构建通过 --build-arg REGISTRY=registry.kxpms.cn/ 覆盖。
#
# Build:
#   docker compose -f docker-compose.local-r112.yml build gateway-v2
#   # 或生产:
#   docker build --build-arg REGISTRY=registry.kxpms.cn/ -t kx-gateway-v2 ./cmd/gateway-v2

ARG REGISTRY=""
FROM --platform=linux/amd64 ${REGISTRY}kx-base:go-vue-amd64 AS builder

WORKDIR /src
USER root
ENV GOPATH=/tmp/go
RUN mkdir -p /tmp/go /tmp/go/pkg /tmp/go/bin
COPY go.mod go.sum ./
ARG GOTOOLCHAIN=auto
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
RUN GOTOOLCHAIN=auto GOPROXY=${GOPROXY} go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=auto go build -a -ldflags="-s -w" -o /llm-gateway-go ./cmd/gateway-v2

FROM --platform=linux/amd64 ${REGISTRY}kx-base:go-vue-alpine-slim-runtime
WORKDIR /
COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
USER appuser
EXPOSE 8782
ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
