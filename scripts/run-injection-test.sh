#!/bin/bash
# =====================================================================
# scripts/run-injection-test.sh — 运行会话注入检测测试
#
# 用法:
#   bash scripts/run-injection-test.sh                    # 使用默认252数据库
#   bash scripts/run-injection-test.sh --db-url "..."    # 指定数据库URL
#   bash scripts/run-injection-test.sh --generate-data    # 先生成测试数据
#   bash scripts/run-injection-test.sh --help             # 显示帮助
# =====================================================================

set -euo pipefail

# 默认值
DB_URL="${LLM_GATEWAY_DATABASE_URL:-postgres://llm_gateway:4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg@172.16.2.210:5432/llm_gateway?sslmode=disable}"
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

log "数据库URL: $DB_URL"

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