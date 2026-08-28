# LLM Gateway Dockerfile
#
# MEDIUM hardening (2026-08-29):
#   - Runtime image pins a non-root user (UID 65532, GID 65532) and the
#     final USER directive switches to it. The previous Dockerfile ran the
#     binary as root, which let any container-escape vuln inherit root.
#   - Digest pinning is opt-in via GO_IMAGE_DIGEST / RUNTIME_IMAGE_DIGEST
#     build args. CI overrides them with the immutable @sha256:... value
#     resolved from the registry; leaving them empty falls back to the
#     mutable tag (local dev only — production builds must pin).

ARG BASE_REGISTRY=registry.kxpms.cn/kx-base
ARG GO_BASE_IMAGE=${BASE_REGISTRY}/golang:1.25-alpine
ARG GO_IMAGE_DIGEST=
ARG RUNTIME_BASE_IMAGE=${BASE_REGISTRY}/alpine:3.22
ARG RUNTIME_IMAGE_DIGEST=

# 构建阶段
FROM ${GO_BASE_IMAGE}${GO_IMAGE_DIGEST:+@${GO_IMAGE_DIGEST}} AS builder

# China network: proxy.golang.org is unreachable, use goproxy.cn instead.
# The offline-package build script already does this; Docker builds need it too.
ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /app

# 复制依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 编译
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -o /app/bin/llm-gateway ./cmd/gateway

# 运行阶段
# Compose the digest-pinned reference when the CI-provided digest is
# non-empty; otherwise fall back to the mutable tag (dev only).
FROM ${RUNTIME_BASE_IMAGE}${RUNTIME_IMAGE_DIGEST:+@${RUNTIME_IMAGE_DIGEST}} AS runtime

RUN apk --no-cache add ca-certificates wget \
    && addgroup -g 65532 -S llmgw \
    && adduser -u 65532 -S -G llmgw llmgw

WORKDIR /app

# 复制二进制文件
COPY --from=builder /app/bin/llm-gateway .

# 复制配置文件
COPY config.example.yaml config.yaml

# Ensure the non-root user owns everything we might write to at runtime
# (e.g. fsstore, ledger ndjson).
RUN chown -R llmgw:llmgw /app

# 暴露端口
EXPOSE 8781

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8781/healthz || exit 1

# Drop root privileges before exec.
USER llmgw:llmgw

# 运行
CMD ["./llm-gateway"]
