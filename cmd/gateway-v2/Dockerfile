FROM --platform=linux/amd64 registry.internal.example.com/kx-base:go-vue AS builder

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

FROM --platform=linux/amd64 registry.internal.example.com/kx-base:go-vue-alpine-slim-runtime
WORKDIR /
COPY --from=builder /llm-gateway-go /usr/local/bin/llm-gateway-go
USER appuser
EXPOSE 8782
ENTRYPOINT ["/usr/local/bin/llm-gateway-go"]
