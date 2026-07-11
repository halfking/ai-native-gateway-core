#!/bin/bash
# apply-hot-table-migrations.sh — 分区表热表架构迁移（统一入口）
#
# 用法:
#   ./scripts/apply-hot-table-migrations.sh --env local   # 本地环境（默认）
#   ./scripts/apply-hot-table-migrations.sh --env 184     # 184 环境（通过 SSH）
#   ./scripts/apply-hot-table-migrations.sh --env prod    # 生产环境

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="${SCRIPT_DIR}/../sql/migrations/startup"
TESTS_DIR="${SCRIPT_DIR}/../sql/tests"

# ============================================================
# 环境配置
# ============================================================
ENV="local"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env|-e) ENV="$2"; shift 2 ;;
    --help|-h) echo "用法: $0 --env <local|184|test|staging|prod>"; exit 0 ;;
    *) echo "❌ 未知参数: $1"; exit 1 ;;
  esac
done

case "$ENV" in
  local)
    DB_HOST="${LOCAL_DB_HOST:-localhost}"
    DB_PORT="${LOCAL_DB_PORT:-5432}"
    DB_USER="${LOCAL_DB_USER:-postgres}"
    DB_NAME="${LOCAL_DB_NAME:-llm_gateway}"
    PSQL_BASE="psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME"
    PSQL() { eval "$PSQL_BASE $*"; }
    ;;
  184)
    REMOTE_HOST="${REMOTE_HOST:-root@47.97.111.154}"  # 154 替代 184 (2026-07-11 退役)
    REMOTE_PORT="${REMOTE_PORT:-25022}"
    DB_HOST="${REMOTE_DB_HOST:-10.0.0.184}"
    DB_PORT="${REMOTE_DB_PORT:-5432}"
    DB_USER="${REMOTE_DB_USER:-llm_gateway}"
    DB_NAME="${REMOTE_DB_NAME:-llm_gateway}"
    PSQL() {
      local sql_file=""
      if [[ "$1" == "-f" ]]; then
        sql_file="$2"
        shift 2
        scp -P "$REMOTE_PORT" "$sql_file" "$REMOTE_HOST:/tmp/$(basename "$sql_file")" > /dev/null
        ssh -p "$REMOTE_PORT" "$REMOTE_HOST" "PGPASSWORD=\$PGPASSWORD psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f /tmp/$(basename "$sql_file") $*"
        ssh -p "$REMOTE_PORT" "$REMOTE_HOST" "rm -f /tmp/$(basename "$sql_file")" 2>/dev/null || true
      else
        ssh -p "$REMOTE_PORT" "$REMOTE_HOST" "PGPASSWORD=\$PGPASSWORD psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -c '$*'"
      fi
    }
    ;;
  test|staging|prod)
    case "$ENV" in
      test)    DB_HOST="test-db.internal";    DB_USER="postgres"; DB_NAME="llm_gateway_test" ;;
      staging) DB_HOST="staging-db.internal"; DB_USER="postgres"; DB_NAME="llm_gateway_staging" ;;
      prod)    DB_HOST="prod-db.internal";    DB_USER="postgres"; DB_NAME="llm_gateway" ;;
    esac
    DB_PORT="${DB_PORT:-5432}"
    PSQL_BASE="psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME"
    PSQL() { eval "$PSQL_BASE $*"; }
    ;;
  *)
    echo "❌ 无效环境: $ENV (支持: local|184|test|staging|prod)"
    exit 1
    ;;
esac

log_info()    { echo "[$(date +'%Y-%m-%d %H:%M:%S')] ℹ️  $*"; }
log_success() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] ✅ $*"; }
log_error()   { echo "[$(date +'%Y-%m-%d %H:%M:%S')] ❌ $*" >&2; }
log_warning() { echo "[$(date +'%Y-%m-%d %H:%M:%S')] ⚠️  $*"; }

check_db_connection() {
  log_info "检查数据库连接 $DB_HOST..."
  if PSQL -c "SELECT 1" > /dev/null 2>&1; then
    log_success "数据库连接 OK"
  else
    log_error "无法连接数据库"
    exit 1
  fi
}

apply_migration() {
  local migration_file=$1
  local migration_name=$(basename "$migration_file" .sql)
  log_info "应用迁移: $migration_name..."
  if PSQL -f "$migration_file" -v ON_ERROR_STOP=1; then
    log_success "迁移 $migration_name 完成"
    return 0
  else
    log_error "迁移 $migration_name 失败"
    return 1
  fi
}

verify_migration() {
  local table_base=$1
  log_info "验证迁移 $table_base..."
  local hot_exists=$(PSQL -t -c "SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '${table_base}_hot')" 2>/dev/null | xargs)
  if [[ "$hot_exists" != "t" ]]; then
    log_error "Hot 表 ${table_base}_hot 不存在"
    return 1
  fi
  local view_exists=$(PSQL -t -c "SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = '${table_base}_with_current_month')" 2>/dev/null | xargs)
  if [[ "$view_exists" != "t" ]]; then
    log_error "视图 ${table_base}_with_current_month 不存在"
    return 1
  fi
  local func_exists=$(PSQL -t -c "SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'promote_${table_base}_hot_to_partition')" 2>/dev/null | xargs)
  if [[ "$func_exists" != "t" ]]; then
    log_error "函数 promote_${table_base}_hot_to_partition 不存在"
    return 1
  fi
  local hot_count=$(PSQL -t -c "SELECT count(*) FROM ${table_base}_hot" 2>/dev/null | xargs)
  log_info "Hot 表 ${table_base}_hot 包含 $hot_count 行"
  log_success "验证通过: $table_base"
}

run_integration_tests() {
  log_info "运行集成测试..."
  local test_file="${TESTS_DIR}/partition_hot_table_tests.sql"
  local log_file="${SCRIPT_DIR}/test_results_$(date +%Y%m%d_%H%M%S).log"
  if [[ -f "$test_file" ]]; then
    if PSQL -f "$test_file" > "$log_file" 2>&1; then
      log_success "集成测试通过"
    else
      log_warning "部分测试失败，日志: $log_file"
    fi
  else
    log_warning "测试文件不存在: $test_file，跳过"
  fi
}

main() {
  echo "=================================================="
  echo "  分区表热表架构迁移"
  echo "  Environment: $ENV"
  echo "  Database: $DB_NAME @ $DB_HOST"
  echo "  Time: $(date)"
  echo "=================================================="
  echo ""

  if [[ "$ENV" == "prod" ]]; then
    echo "⚠️  即将应用到生产环境!"
    read -p "输入 YES 继续: " confirmation
    if [[ "$confirmation" != "YES" ]]; then
      log_error "用户取消"
      exit 1
    fi
  fi

  check_db_connection

  for entry in "348:tool_usage_stats" "349:credit_ledger" "350:request_logs_bodies"; do
    num="${entry%%:*}"
    tbl="${entry##*:}"
    echo ""
    log_info "=== Migration $num: $tbl ==="
    found=false
    for f in "$MIGRATIONS_DIR"/${num}_*.sql; do
      [[ -f "$f" ]] || continue
      apply_migration "$f" || exit 1
      found=true
    done
    if ! $found; then
      log_error "迁移文件 ${num}_*.sql 不存在于 $MIGRATIONS_DIR"
      exit 1
    fi
    verify_migration "$tbl" || exit 1
  done

  echo ""
  log_info "运行集成测试..."
  run_integration_tests

  echo ""
  echo "=================================================="
  log_success "所有迁移完成!"
  echo "=================================================="
  echo ""
  echo "📊 摘要:"
  echo "  - tool_usage_stats → hot table 架构"
  echo "  - credit_ledger → hot table 架构"
  echo "  - request_logs_bodies → hot table 架构"
  echo ""
}

main
