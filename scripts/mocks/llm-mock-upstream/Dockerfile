# OpenAI-compatible mock upstream for local integration testing.
# Implements /v1/chat/completions (stream + non-stream) with configurable latency.
#
# Build: docker build -t llm-mock-upstream scripts/mocks/llm-mock-upstream
# Run:   docker run -p 18080:18080 -e MOCK_LATENCY_MS_MIN=50 llm-mock-upstream

# 使用国内镜像源避免 docker.io 超时；本地有缓存时直接用 python:3.11-alpine
FROM docker.m.daocloud.io/library/python:3.11-alpine

RUN pip install --no-cache-dir aiohttp==3.10.5 && \
    apk add --no-cache wget

WORKDIR /app
COPY server.py .

# Defaults tuned for local fast-feedback testing (not the 10-15s fp_slot
# regression profile). Override via docker-compose environment.
ENV MOCK_PORT=18080 \
    MOCK_LATENCY_MS_MIN=50 \
    MOCK_LATENCY_MS_MAX=100 \
    MOCK_TOKEN=local-mock

EXPOSE 18080

# HEALTHCHECK uses the /healthz JSON endpoint aiohttp serves.
HEALTHCHECK --interval=5s --timeout=3s --retries=10 --start-period=3s \
    CMD wget -q -O- http://127.0.0.1:18080/healthz | grep -q '"ok"' || exit 1

CMD ["python", "server.py"]
