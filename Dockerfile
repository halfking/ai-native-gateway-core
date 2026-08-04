# LLM Gateway Dockerfile

ARG BASE_REGISTRY=registry.kxpms.cn/kx-base
ARG GO_BASE_IMAGE=${BASE_REGISTRY}/golang:1.25-alpine
ARG RUNTIME_BASE_IMAGE=${BASE_REGISTRY}/alpine:3.22

# 构建阶段
FROM ${GO_BASE_IMAGE} AS builder

WORKDIR /app

# 复制依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 编译
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -o /app/bin/llm-gateway ./cmd/gateway

# 运行阶段
FROM ${RUNTIME_BASE_IMAGE}

RUN apk --no-cache add ca-certificates

WORKDIR /app

# 复制二进制文件
COPY --from=builder /app/bin/llm-gateway .

# 复制配置文件
COPY config.example.yaml config.yaml

# 暴露端口
EXPOSE 8781

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8781/healthz || exit 1

# 运行
CMD ["./llm-gateway"]
