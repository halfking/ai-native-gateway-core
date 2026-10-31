#!/bin/bash
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
# =====================================================================
# scripts/run-injection-test.sh — 运行会话注入检测测试
#
# 用法:
#   LLM_GATEWAY_DATABASE_URL=... bash scripts/run-injection-test.sh
#   bash scripts/run-injection-test.sh --db-url "..."    # 指定数据库URL
#   bash scripts/run-injection-test.sh --generate-data    # 先生成测试数据
#   bash scripts/run-injection-test.sh --help             # 显示帮助
# =====================================================================

set -euo pipefail

# Database credentials must be supplied at runtime, never embedded in this tool.
DB_URL="${LLM_GATEWAY_DATABASE_URL:-${DATABASE_URL:-}}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEST_DIR="$SCRIPT_DIR/injection-test"

# 颜色
GREEN=$'\033[0;32m'
YELLOW=$'\033[1;33m'
RED=$'\033[0;31m'
NC=$'\033[0m'

log()  { echo -e "${GREEN}[injection-test]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[error]${NC} $*" >&2; }

# 参数解析
GENERATE_DATA=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --db-url)         DB_URL="$2"; shift 2 ;;
    --generate-data)  GENERATE_DATA=true; shift ;;
    --help|-h)
      sed -n '2,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) err "未知参数 $1"; exit 1 ;;
  esac
done

if [[ -z "$DB_URL" ]]; then
  err "需要 --db-url 或 LLM_GATEWAY_DATABASE_URL / DATABASE_URL"
  exit 1
fi

log "数据库URL已配置"

# 检查 Go 环境
if ! command -v go &>/dev/null; then
  err "Go 未安装"
  exit 1
fi

# 进入测试目录
cd "$TEST_DIR"

# 检查并初始化 go module
if [[ ! -f "go.sum" ]]; then
  log "初始化 go module..."
  go mod tidy
fi

# 先生成测试数据（如果指定）
if [[ "$GENERATE_DATA" == "true" ]]; then
  log "生成测试数据..."
  export LLM_GATEWAY_DATABASE_URL="$DB_URL"
  go run generate-test-data.go
fi

# 运行测试
log "开始运行会话注入检测测试..."

export LLM_GATEWAY_DATABASE_URL="$DB_URL"

# 运行测试脚本
go run test-prompt-injection-detection.go

log "测试完成"
