#!/bin/bash
# 容量/保留策略基线采集：对目标 PG 运行 01/02/03 号查询，输出到带时间戳的报告文件。
# 连接参数沿用仓库惯例（同 manage-request-logs.sh）：
#   DB_HOST / DB_PORT / DB_USER / DB_PASSWORD / DB_NAME
# 用法：
#   scripts/capacity-baseline/run.sh            # 输出 reports/capacity-<ts>.txt
#   scripts/capacity-baseline/run.sh --stdout   # 直接打印
# 注意：只读查询，但生产库执行请避开业务高峰。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPORT_DIR="$SCRIPT_DIR/reports"
mkdir -p "$REPORT_DIR"

: "${DB_HOST:?请设置 DB_HOST}"
: "${DB_NAME:?请设置 DB_NAME}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
export PGPASSWORD="${DB_PASSWORD:?请设置 DB_PASSWORD}"

# 宿主机无 psql（或 FORCE_PG_CONTAINER=1）时回退到本地开发容器（与仓库其他脚本惯例一致）
if [ "${FORCE_PG_CONTAINER:-0}" != "1" ] && command -v psql >/dev/null 2>&1; then
  PSQL=(psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1)
  PG_CONTAINER=""
else
  PG_CONTAINER="${PG_CONTAINER:-llm-gateway-pg}"
  docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER" || {
    echo "宿主机无 psql 且未找到容器 $PG_CONTAINER" >&2
    exit 2
  }
  echo "（宿主机无 psql，经容器 $PG_CONTAINER 执行；连接参数仅 DB_USER/DB_NAME 生效）"
  PSQL=(docker exec -i "$PG_CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1)
fi

run_section() {
  local title="$1" file="$2"
  echo ""
  echo "=================================================================="
  echo "== $title"
  echo "=================================================================="
  # 统一经 stdin 喂 SQL：docker exec 分支看不到宿主机路径
  "${PSQL[@]}" -f - < "$SCRIPT_DIR/$file"
}

collect() {
  echo "容量/保留策略基线采集"
  echo "时间: $(date '+%Y-%m-%d %H:%M:%S %z')"
  echo "目标: ${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}"
  run_section "1/3 表与分区尺寸全景" 01-table-sizes.sql
  run_section "2/3 分区家族保留覆盖" 02-partition-coverage.sql
  run_section "3/3 增长速率估计"     03-growth-rate.sql
  echo ""
  echo "提示：把以上输出按 scripts/capacity-baseline/README.md 的模板"
  echo "整理为 docs/perf/capacity-retention-baseline-<日期>.md 并提交。"
}

if [ "${1:-}" = "--stdout" ]; then
  collect
else
  out="$REPORT_DIR/capacity-$(date +%Y%m%d-%H%M%S).txt"
  collect | tee "$out"
  echo ""
  echo "报告已保存: $out"
fi
