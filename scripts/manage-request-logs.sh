#!/bin/bash
# manage-request-logs.sh — 请求日志统一管理脚本（统一入口）
#
# 合并自: cleanup-request-logs.sh + archive-request-logs.sh + delete-old-request-logs.sh
#         + analyze-request-logs-size.sh + check-archive-table-sizes.sh
# 修订: 2026-07-05
#
# 模式:
#   --cleanup     完整清理流程（默认）：分析 → 裁剪 → 归档 → 删除 → 分析
#   --analyze     仅分析数据量分布
#   --archive     仅归档冷数据（30-90天）
#   --delete      仅删除过期数据（>90天）
#   --check-sizes 检查归档表实际存储占用
#   --dry-run     预览模式（不实际执行删除/归档）
#
# 用法:
#   ./scripts/manage-request-logs.sh                    # 全流程
#   ./scripts/manage-request-logs.sh --analyze          # 仅分析
#   ./scripts/manage-request-logs.sh --check-sizes      # 检查归档表
#   ./scripts/manage-request-logs.sh --dry-run          # 预览模式
#
# 环境变量:
#   DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD
#   ARCHIVE_DIR, LOG_FILE, ARCHIVE_DAYS, DELETE_DAYS, TRIM_DAYS

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── 默认配置 ──
LOG_FILE="${LOG_FILE:-/var/log/llm-gateway-cleanup.log}"
ARCHIVE_DIR="${ARCHIVE_DIR:-/opt/llm-gateway-archive}"
ENABLE_TRIM="${ENABLE_TRIM:-false}"
ENABLE_ARCHIVE="${ENABLE_ARCHIVE:-true}"
ENABLE_DELETE="${ENABLE_DELETE:-true}"
TRIM_DAYS="${TRIM_DAYS:-7-30}"
ARCHIVE_DAYS="${ARCHIVE_DAYS:-30-90}"
DELETE_DAYS="${DELETE_DAYS:-90}"
DRY_RUN="${DRY_RUN:-false}"

DB_HOST="${DB_HOST:-__INTERNAL_DB_HOST__}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-__DB_USER__}"
DB_PASSWORD="${DB_PASSWORD:-__REDACTED_DB_PASSWORD__}"

# ── 参数解析 ──
MODE="cleanup"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --analyze)      MODE="analyze"; shift ;;
    --archive)      MODE="archive"; shift ;;
    --delete)       MODE="delete"; shift ;;
    --check-sizes)  MODE="check-sizes"; shift ;;
    --cleanup)      MODE="cleanup"; shift ;;
    --dry-run)      DRY_RUN="true"; shift ;;
    --help|-h)      echo "用法: $0 [--analyze|--archive|--delete|--check-sizes|--cleanup] [--dry-run]"; exit 0 ;;
    *)              echo "❌ 未知参数: $1"; exit 1 ;;
  esac
done

mkdir -p "$(dirname "$LOG_FILE")" "$ARCHIVE_DIR"

# ── 颜色 / 日志 ──
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
log_info()    { local m="[$(date '+%Y-%m-%d %H:%M:%S')] [INFO] $*";    echo -e "${BLUE}${m}${NC}"; echo "$m" >> "$LOG_FILE"; }
log_success() { local m="[$(date '+%Y-%m-%d %H:%M:%S')] [SUCCESS] $*"; echo -e "${GREEN}${m}${NC}"; echo "$m" >> "$LOG_FILE"; }
log_warn()    { local m="[$(date '+%Y-%m-%d %H:%M:%S')] [WARN] $*";    echo -e "${YELLOW}${m}${NC}"; echo "$m" >> "$LOG_FILE"; }
log_error()   { local m="[$(date '+%Y-%m-%d %H:%M:%S')] [ERROR] $*";   echo -e "${RED}${m}${NC}"; echo "$m" >> "$LOG_FILE"; }

PSQL="PGPASSWORD=$DB_PASSWORD psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -t -A"

# ══════════════════════════════════════════════════════════════════════
# 功能: 分析数据量分布
# ══════════════════════════════════════════════════════════════════════
analyze_sizes() {
  log_info "分析 request_logs 数据量分布..."
  local sql="
    SELECT '总行数' AS metric, count(*)::text FROM request_logs
    UNION ALL SELECT '总大小', pg_size_pretty(pg_total_relation_size('request_logs'))
    UNION ALL SELECT '主表', pg_size_pretty(pg_relation_size('request_logs'))
    UNION ALL SELECT 'TOAST', pg_size_pretty(pg_total_relation_size('request_logs') - pg_relation_size('request_logs'))
    UNION ALL SELECT '索引', pg_size_pretty(pg_indexes_size('request_logs'))
    UNION ALL SELECT '今日', count(*)::text FROM request_logs WHERE created_at >= CURRENT_DATE
    UNION ALL SELECT '近7天', count(*)::text FROM request_logs WHERE created_at >= CURRENT_DATE - INTERVAL '7 days'
    UNION ALL SELECT '近30天', count(*)::text FROM request_logs WHERE created_at >= CURRENT_DATE - INTERVAL '30 days'
    UNION ALL SELECT '30-90天', count(*)::text FROM request_logs WHERE created_at >= CURRENT_DATE - INTERVAL '90 days' AND created_at < CURRENT_DATE - INTERVAL '30 days'
    UNION ALL SELECT '>90天', count(*)::text FROM request_logs WHERE created_at < CURRENT_DATE - INTERVAL '90 days';
  "
  eval "$PSQL -c \"$sql\"" 2>/dev/null | while IFS='|' read -r metric value; do
    log_info "  $metric: $value"
  done
  log_success "数据分析完成"
}

# ══════════════════════════════════════════════════════════════════════
# 功能: 归档冷数据
# ══════════════════════════════════════════════════════════════════════
archive_data() {
  local days="${ARCHIVE_DAYS##*-}"
  local oldest="${ARCHIVE_DAYS%%-*}"
  log_info "归档 $oldest~$days 天的数据到 $ARCHIVE_DIR ..."

  local date_from date_to
  date_from=$(date -d "-$days days" +%Y-%m-%d)
  date_to=$(date -d "-$oldest days" +%Y-%m-%d)
  local archive_file="${ARCHIVE_DIR}/request_logs_${date_from}_to_${date_to}.jsonl.gz"

  if [[ -f "$archive_file" ]] && [[ "$DRY_RUN" == "false" ]]; then
    log_warn "归档文件已存在: $archive_file，跳过"
    return 0
  fi

  local where="created_at >= '$date_from' AND created_at < '$date_to'"
  local count_sql="SELECT count(*) FROM request_logs WHERE $where"
  local total
  total=$(eval "$PSQL -c \"$count_sql\"" 2>/dev/null | tr -d ' ')

  if [[ -z "$total" || "$total" -eq 0 ]]; then
    log_warn "没有 $days~$oldest 天的数据需要归档"
    return 0
  fi
  log_info "  待归档行数: $total"

  if [[ "$DRY_RUN" == "true" ]]; then
    log_info "  [DRY RUN] 将归档 $total 行到 $archive_file"
    return 0
  fi

  eval "$PSQL -c \"\\copy (SELECT * FROM request_logs WHERE $where) TO PROGRAM 'gzip > $archive_file' WITH CSV HEADER;\"" 2>/dev/null && \
    log_success "归档完成: $(du -h "$archive_file" | cut -f1) ($total 行)" || \
    log_error "归档失败"
}

# ══════════════════════════════════════════════════════════════════════
# 功能: 删除过期数据
# ══════════════════════════════════════════════════════════════════════
delete_old_data() {
  local cutoff
  cutoff=$(date -d "-${DELETE_DAYS} days" +%Y-%m-%d)

  local count_sql="SELECT count(*) FROM request_logs WHERE created_at < '$cutoff'"
  local total
  total=$(eval "$PSQL -c \"$count_sql\"" 2>/dev/null | tr -d ' ')

  if [[ -z "$total" || "$total" -eq 0 ]]; then
    log_warn "没有超过 ${DELETE_DAYS} 天的数据需要删除"
    return 0
  fi

  log_info "过期数据 (>${DELETE_DAYS}天, <${cutoff}): $total 行"

  if [[ "$DRY_RUN" == "true" ]]; then
    log_info "  [DRY RUN] 将删除 $total 行过期数据"
    return 0
  fi

  log_info "  逐步删除 (每次 10000 行)..."
  local deleted=0
  while :; do
    local batch
    batch=$(eval "$PSQL -c \"DELETE FROM request_logs WHERE created_at < '$cutoff' AND ctid IN (SELECT ctid FROM request_logs WHERE created_at < '$cutoff' LIMIT 10000);\"" 2>/dev/null | tr -d ' ')
    deleted=$((deleted + 10000))
    log_info "  已删除: $deleted 行"
    [[ "$batch" -lt 10000 ]] && break
  done
  log_success "删除完成: 共删除 $deleted 行"
}

# ══════════════════════════════════════════════════════════════════════
# 功能: 检查归档表存储占用
# ══════════════════════════════════════════════════════════════════════
check_archive_sizes() {
  local target="${1:-184}"
  log_info "检查归档表存储占用 (target=$target)..."

  case "$target" in
    184)
      local ssh_host="root@47.97.111.154" ssh_port="25022"  # 154 替代 184
      local ns="pms-test" deploy="llm-gateway-pg"
      local sql="
        SELECT relname, pg_size_pretty(pg_total_relation_size(oid)) AS size
        FROM pg_class WHERE relname LIKE '%\_archive%' AND relnamespace = 'public'::regnamespace
        ORDER BY pg_total_relation_size(oid) DESC;
      "
      ssh -p "$ssh_port" "$ssh_host" \
        "kubectl -n $ns exec deployment/$deploy -- psql -U $DB_USER -d $DB_NAME -tA -c \"$sql\"" 2>/dev/null
      ;;
    local)
      docker exec r112_postgres psql -U kxuser -d llm_gateway -tA -c "
        SELECT relname, pg_size_pretty(pg_total_relation_size(oid)) AS size
        FROM pg_class WHERE relname LIKE '%\_archive%' AND relnamespace = 'public'::regnamespace
        ORDER BY pg_total_relation_size(oid) DESC;
      " 2>/dev/null
      ;;
  esac | while IFS='|' read -r tbl size; do
    log_info "  $tbl: $size"
  done
  log_success "归档表检查完成"
}

# ══════════════════════════════════════════════════════════════════════
# 主流程
# ══════════════════════════════════════════════════════════════════════
main() {
  log_info "=========================================="
  log_info "请求日志管理: 模式=$MODE"
  log_info "=========================================="

  START_TIME=$(date +%s)
  TOTAL_ERRORS=0

  case "$MODE" in
    analyze)
      analyze_sizes
      ;;
    archive)
      archive_data
      ;;
    delete)
      delete_old_data
      ;;
    check-sizes)
      check_archive_sizes "${1:-184}"
      ;;
    cleanup)
      log_info "配置: TRIM=$ENABLE_TRIM ARCHIVE=$ENABLE_ARCHIVE DELETE=$ENABLE_DELETE DRY_RUN=$DRY_RUN"
      analyze_sizes

      if [[ "$ENABLE_TRIM" == "true" ]]; then
        log_info "裁剪温数据 ($TRIM_DAYS 天)..."
        log_warn "温数据裁剪尚未实施，需在 Phase 3 实现"
      fi

      if [[ "$ENABLE_ARCHIVE" == "true" ]]; then
        archive_data || TOTAL_ERRORS=$((TOTAL_ERRORS + 1))
      fi

      if [[ "$ENABLE_DELETE" == "true" ]]; then
        delete_old_data || TOTAL_ERRORS=$((TOTAL_ERRORS + 1))
      fi

      analyze_sizes
      ;;
  esac

  DURATION=$(( $(date +%s) - START_TIME ))
  log_info "完成: ${DURATION}s, 错误: $TOTAL_ERRORS"
  [ "$TOTAL_ERRORS" -eq 0 ] && log_success "✅ 成功" || log_error "❌ 有错误"
  exit "$TOTAL_ERRORS"
}

main
