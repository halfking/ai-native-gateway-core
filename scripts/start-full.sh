#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
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
