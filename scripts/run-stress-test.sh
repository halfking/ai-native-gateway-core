#!/bin/bash
# =====================================================================
# scripts/run-stress-test.sh — 运行会话注入检测并发压力测试
#
# 用法:
#   bash scripts/run-stress-test.sh                    # 默认配置（100并发，5分钟）
#   bash scripts/run-stress-test.sh --quick            # 快速测试（10并发，30秒）
#   bash scripts/run-stress-test.sh --heavy            # 重度测试（500并发，10分钟）
#   bash scripts/run-stress-test.sh --help             # 显示帮助
# =====================================================================

set -euo pipefail

# 默认值
TEST_MODE="default"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEST_DIR="$SCRIPT_DIR/injection-test"

# 颜色
GREEN=$'\033[0;32m'
YELLOW=$'\033[1;33m'
RED=$'\033[0;31m'
NC=$'\033[0m'

log()  { echo -e "${GREEN}[stress-test]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[error]${NC} $*" >&2; }

# 参数解析
while [[ $# -gt 0 ]]; do
  case "$1" in
    --quick)  TEST_MODE="quick"; shift ;;
    --heavy)  TEST_MODE="heavy"; shift ;;
    --help|-h)
      sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) err "未知参数 $1"; exit 1 ;;
  esac
done

log "测试模式: $TEST_MODE"

# 检查 Go 环境
if ! command -v go &>/dev/null; then
  err "Go 未安装"
  exit 1
fi

# 进入测试目录
cd "$TEST_DIR"

# 编译测试程序
log "编译压力测试程序..."
go build -o /tmp/stress-test stress-test.go

# 运行测试
log "开始运行压力测试..."
case "$TEST_MODE" in
  quick)
    log "快速测试模式：10并发，30秒"
    timeout 40 /tmp/stress-test || true
    ;;
  heavy)
    log "重度测试模式：500并发，10分钟"
    timeout 660 /tmp/stress-test || true
    ;;
  *)
    log "默认测试模式：100并发，5分钟"
    timeout 360 /tmp/stress-test || true
    ;;
esac

log "压力测试完成"

# 查找并显示测试结果
RESULT_FILE=$(ls -t stress_test_result_*.json 2>/dev/null | head -1 || echo "")
if [[ -n "$RESULT_FILE" ]]; then
  log "测试结果已保存到: $RESULT_FILE"
  
  # 提取关键指标
  if command -v jq &>/dev/null; then
    log "关键指标摘要:"
    jq -r '
      "  总请求数: \(.TotalRequests)",
      "  平均QPS: \(.AvgQPS | floor)",
      "  峰值QPS: \(.PeakQPS | floor)",
      "  平均延迟: \(.AvgLatencyMs | tostring)ms",
      "  P99延迟: \(.P99LatencyMs)ms",
      "  内存增长: \(.MemoryGrowthMB | tostring)MB",
      "  Goroutine增长: \(.GoroutineGrowth)",
      "  内存泄漏: \(if .MemoryLeakDetect then "⚠️ 是" else "✅ 否" end)",
      "  Goroutine泄漏: \(if .GoroutineLeakDetect then "⚠️ 是" else "✅ 否" end)"
    ' "$RESULT_FILE"
  fi
else
  warn "未找到测试结果文件"
fi

log "测试完成"