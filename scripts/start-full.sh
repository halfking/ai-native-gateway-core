#!/usr/bin/env bash
# start-full.sh — 以 full 存储模式（PostgreSQL + Redis）启动 gateway
#
# 双模式存储架构（Task 4.2）：env 名与 config/storage.go 的 LLM_GATEWAY_* 定义一致。
# full 模式要求 postgres_url 与 redis_url 非空（见 config.StorageConfig.Validate）。
#
# 用法:
#   ./scripts/start-full.sh                 # 前台启动（go run）
#   BUILD=1 ./scripts/start-full.sh         # 先编译再启动（bin/gateway）
#   BIN=/path/to/gateway ./scripts/start-full.sh   # 指定二进制
#
# 说明: LLM_GATEWAY_DATABASE_URL 是网关既有的主连接串（路由/凭证等子系统继续
# 使用）；full_storage 段的双模式工厂连接串独立配置。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

# ── 存储模式（必设）─────────────────────────────────────────────
export LLM_GATEWAY_STORAGE_MODE="${LLM_GATEWAY_STORAGE_MODE:-full}"

# ── full 段（与 config/storage.go FullStorageConfig 的 env tag 一致）──
export LLM_GATEWAY_POSTGRES_URL="${LLM_GATEWAY_POSTGRES_URL:-${LLM_GATEWAY_DATABASE_URL:-postgres://gateway:gateway@127.0.0.1:5432/llm_gateway}}"
export LLM_GATEWAY_REDIS_URL="${LLM_GATEWAY_REDIS_URL:-127.0.0.1:6379}"
export LLM_GATEWAY_STORAGE_MAX_CONNECTIONS="${LLM_GATEWAY_STORAGE_MAX_CONNECTIONS:-100}"

# ── 网关主连接串（缺省回退 full_storage 同款）───────────────────
export LLM_GATEWAY_DATABASE_URL="${LLM_GATEWAY_DATABASE_URL:-$LLM_GATEWAY_POSTGRES_URL}"

echo "[start-full] storage_mode=full (PostgreSQL + Redis)"
echo "[start-full] postgres_url=$LLM_GATEWAY_POSTGRES_URL"
echo "[start-full] redis_url=$LLM_GATEWAY_REDIS_URL"

# ── 启动 ────────────────────────────────────────────────────────
if [ "${BUILD:-0}" = "1" ]; then
    echo "[start-full] building..."
    go build -o bin/gateway ./cmd/gateway
    BIN="${BIN:-bin/gateway}"
fi

if [ -n "${BIN:-}" ]; then
    exec "$BIN"
fi
exec go run ./cmd/gateway
