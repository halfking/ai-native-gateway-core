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
# start-lite.sh — 以 lite 存储模式（SQLite + 本地文件目录，零外部依赖）启动 gateway
#
# 双模式存储架构（Task 4.2）：env 名与 config/storage.go 的 LLM_GATEWAY_* 定义一致。
# 未显式 export 的变量由网关侧 ApplyLiteDefaults 提供默认值（此处显式写出便于运维感知）。
#
# 用法:
#   ./scripts/start-lite.sh                 # 前台启动（go run）
#   BUILD=1 ./scripts/start-lite.sh         # 先编译再启动（bin/gateway）
#   BIN=/path/to/gateway ./scripts/start-lite.sh   # 指定二进制
#
# 注意: lite 模式不使用 PostgreSQL/Redis，本脚本会清空相关 env，
# 避免继承外层 shell 的 DATABASE_URL 拖慢启动。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

# ── 存储模式（必设）─────────────────────────────────────────────
export LLM_GATEWAY_STORAGE_MODE="${LLM_GATEWAY_STORAGE_MODE:-lite}"

# ── lite 段（与 config/storage.go LiteStorageConfig 的 env tag 一致）──
export LLM_GATEWAY_SQLITE_PATH="${LLM_GATEWAY_SQLITE_PATH:-./data/llm-gateway.db}"
export LLM_GATEWAY_BODIES_DIR="${LLM_GATEWAY_BODIES_DIR:-./data/session_bodies}"
export LLM_GATEWAY_CACHE_DIR="${LLM_GATEWAY_CACHE_DIR:-./data/cache}"
export LLM_GATEWAY_LOGS_DIR="${LLM_GATEWAY_LOGS_DIR:-./data/request_logs}"

# ── 清空 full 段变量：lite 模式不使用 PostgreSQL/Redis ──────────
# Redis 六项与 storage_mode_init.go 的 liteRedisEnvKeys 一致（R28 #16），
# 双保险覆盖进程内 unset 之外的继承 env。
unset LLM_GATEWAY_DATABASE_URL DATABASE_URL LLM_GATEWAY_POSTGRES_URL LLM_GATEWAY_REDIS_URL \
    LLM_GATEWAY_REDIS_ADDR LLM_GATEWAY_REDIS_PASSWORD LLM_GATEWAY_REDIS_DB \
    REDIS_URL RATE_LIMIT_REDIS_URL RPM_REDIS_URL || true

# ── 数据目录 ────────────────────────────────────────────────────
mkdir -p ./data

echo "[start-lite] storage_mode=lite (SQLite + local dirs)"
echo "[start-lite] sqlite_path=$LLM_GATEWAY_SQLITE_PATH"
echo "[start-lite] bodies_dir=$LLM_GATEWAY_BODIES_DIR cache_dir=$LLM_GATEWAY_CACHE_DIR logs_dir=$LLM_GATEWAY_LOGS_DIR"

# ── 启动 ────────────────────────────────────────────────────────
if [ "${BUILD:-0}" = "1" ]; then
    echo "[start-lite] building..."
    go build -o bin/gateway ./cmd/gateway
    BIN="${BIN:-bin/gateway}"
fi

if [ -n "${BIN:-}" ]; then
    exec "$BIN"
fi
exec go run ./cmd/gateway
