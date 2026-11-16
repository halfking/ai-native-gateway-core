#!/bin/bash
# F3 检查任务管理脚本

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="$REPO_ROOT/logs/f3-checks"

# 定时任务 ID
CRON_ID="d51ee1ed-95ba-4e4f-a736-7af12dbc3a1b"

usage() {
  cat << EOF
F3 残留地平线检查任务管理

用法: $0 <command>

命令:
  check          立即执行一次检查
  status         查看定时任务状态
  logs           查看最近的检查日志
  trend          显示残留数量趋势
  history        查看所有历史记录
  baseline       查看当前基线
  pause          暂停定时任务
  resume         恢复定时任务
  help           显示此帮助信息

示例:
  $0 check       # 立即执行检查
  $0 trend       # 查看趋势图
  $0 logs        # 查看最近日志
EOF
}

cmd_check() {
  echo "执行 F3 残留地平线检查..."
  bash "$REPO_ROOT/scripts/audit/f3-auto-check.sh"
}

cmd_status() {
  echo "定时任务状态:"
  echo "  任务 ID: $CRON_ID"
  echo "  频率: 每 30 分钟"
  echo "  下次运行: 整点或半点"
  echo ""
  echo "ℹ️  使用 MiniMax Code 的 cron_read 工具查看详细状态"
}

cmd_logs() {
  if [[ ! -d "$LOG_DIR" ]]; then
    echo "❌ 日志目录不存在: $LOG_DIR"
    exit 1
  fi
  
  LATEST_LOG=$(ls -t "$LOG_DIR"/f3-check-*.log 2>/dev/null | head -1)
  if [[ -z "$LATEST_LOG" ]]; then
    echo "❌ 未找到检查日志"
    exit 1
  fi
  
  echo "最近的检查日志:"
  echo "=================="
  cat "$LATEST_LOG"
}

cmd_trend() {
  if [[ ! -d "$LOG_DIR" ]]; then
    echo "❌ 日志目录不存在: $LOG_DIR"
    exit 1
  fi
  
  echo "F3 残留地平线数量趋势:"
  echo "========================"
  echo ""
  
  grep "当前残留数量:" "$LOG_DIR"/f3-check-*.log 2>/dev/null | \
    awk -F'[:/]' '{
      split($0, a, "f3-check-");
      split(a[2], b, ".log");
      timestamp = b[1];
      split($0, c, "当前残留数量: ");
      split(c[2], d, " ");
      count = d[1];
      
      # 格式化时间戳
      date = substr(timestamp, 1, 8);
      time = substr(timestamp, 10, 6);
      formatted_time = substr(time, 1, 2) ":" substr(time, 3, 2) ":" substr(time, 5, 2);
      
      printf "%s %s  %4d 行", date, formatted_time, count;
      
      if (prev_count != "") {
        diff = prev_count - count;
        if (diff > 0) {
          printf "  ⬇ -%d", diff;
        } else if (diff < 0) {
          printf "  ⬆ +%d", -diff;
        } else {
          printf "  ━ 0";
        }
      }
      printf "\n";
      
      prev_count = count;
    }' | tail -20
  
  echo ""
  echo "（最近 20 次检查）"
}

cmd_history() {
  if [[ ! -d "$LOG_DIR" ]]; then
    echo "❌ 日志目录不存在: $LOG_DIR"
    exit 1
  fi
  
  echo "所有检查记录:"
  echo "=============="
  ls -lh "$LOG_DIR"/f3-check-*.log
}

cmd_baseline() {
  BASELINE_FILE="$LOG_DIR/baseline.txt"
  if [[ ! -f "$BASELINE_FILE" ]]; then
    echo "❌ 基线文件不存在"
    exit 1
  fi
  
  BASELINE=$(cat "$BASELINE_FILE")
  echo "当前基线: $BASELINE 行"
  echo ""
  echo "基线文件: $BASELINE_FILE"
  echo "更新时间: $(ls -lh "$BASELINE_FILE" | awk '{print $6, $7, $8}')"
}

cmd_pause() {
  echo "⚠️  暂停定时任务"
  echo "请使用 MiniMax Code 的 cron_update 工具:"
  echo "  任务 ID: $CRON_ID"
  echo "  设置: enabled = false"
}

cmd_resume() {
  echo "▶️  恢复定时任务"
  echo "请使用 MiniMax Code 的 cron_update 工具:"
  echo "  任务 ID: $CRON_ID"
  echo "  设置: enabled = true"
}

# 主入口
if [[ $# -eq 0 ]]; then
  usage
  exit 0
fi

COMMAND="$1"
case "$COMMAND" in
  check)
    cmd_check
    ;;
  status)
    cmd_status
    ;;
  logs)
    cmd_logs
    ;;
  trend)
    cmd_trend
    ;;
  history)
    cmd_history
    ;;
  baseline)
    cmd_baseline
    ;;
  pause)
    cmd_pause
    ;;
  resume)
    cmd_resume
    ;;
  help|-h|--help)
    usage
    ;;
  *)
    echo "❌ 未知命令: $COMMAND"
    echo ""
    usage
    exit 1
    ;;
esac
