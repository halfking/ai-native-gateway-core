#!/bin/bash
# 分区表架构验证脚本 - 本地 + 184环境
# Author: llm-gateway-ops
# Date: 2026-07-05
#
# 使用方法:
#   ./verify_partition_architecture.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================
# 颜色定义
# ============================================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ============================================================
# 辅助函数
# ============================================================

log_info() {
  echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
  echo -e "${GREEN}[✓]${NC} $*"
}

log_error() {
  echo -e "${RED}[✗]${NC} $*"
}

log_warning() {
  echo -e "${YELLOW}[!]${NC} $*"
}

# ============================================================
# 环境配置
# ============================================================

# 本地环境
LOCAL_DB_HOST="${LOCAL_DB_HOST:-localhost}"
LOCAL_DB_PORT="${LOCAL_DB_PORT:-5432}"
LOCAL_DB_USER="${LOCAL_DB_USER:-postgres}"
LOCAL_DB_NAME="${LOCAL_DB_NAME:-llm_gateway}"
LOCAL_SERVICE_URL="${LOCAL_SERVICE_URL:-http://localhost:8080}"

# 184环境
REMOTE_184_HOST="${REMOTE_184_HOST:-10.0.0.184}"
REMOTE_184_DB_HOST="${REMOTE_184_DB_HOST:-10.0.0.184}"
REMOTE_184_DB_PORT="${REMOTE_184_DB_PORT:-5432}"
REMOTE_184_DB_USER="${REMOTE_184_DB_USER:-postgres}"
REMOTE_184_DB_NAME="${REMOTE_184_DB_NAME:-llm_gateway}"
REMOTE_184_SERVICE_URL="${REMOTE_184_SERVICE_URL:-http://10.0.0.184:8080}"

# 数据库连接命令
LOCAL_PSQL="psql -h $LOCAL_DB_HOST -p $LOCAL_DB_PORT -U $LOCAL_DB_USER -d $LOCAL_DB_NAME"
REMOTE_184_PSQL="ssh $REMOTE_184_HOST \"psql -h $REMOTE_184_DB_HOST -p $REMOTE_184_DB_PORT -U $REMOTE_184_DB_USER -d $REMOTE_184_DB_NAME\""

# ============================================================
# 检查函数
# ============================================================

# 检查数据库连接
check_db_connection() {
  local env_name=$1
  local psql_cmd=$2
  
  log_info "检查 $env_name 数据库连接..."
  
  if eval "$psql_cmd -c 'SELECT 1' > /dev/null 2>&1"; then
    log_success "$env_name 数据库连接正常"
    return 0
  else
    log_error "$env_name 数据库连接失败"
    return 1
  fi
}

# 检查hot表是否存在
check_hot_tables() {
  local env_name=$1
  local psql_cmd=$2
  
  log_info "检查 $env_name 的hot表..."
  
  local tables=(
    "request_logs_hot"
    "usage_ledger_hot"
    "request_wal_hot"
    "routing_decision_log_hot"
    "credential_model_index_hot"
    "credit_ledger_hot"
    "tool_usage_stats_hot"
    "request_logs_bodies_hot"
  )
  
  local missing_tables=()
  
  for table in "${tables[@]}"; do
    local exists=$(eval "$psql_cmd -t -c \"SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '$table')\"" 2>/dev/null | xargs)
    
    if [[ "$exists" == "t" ]]; then
      log_success "  $table 存在"
    else
      log_warning "  $table 不存在（可能需要迁移）"
      missing_tables+=("$table")
    fi
  done
  
  if [ ${#missing_tables[@]} -eq 0 ]; then
    log_success "$env_name 所有hot表都存在"
    return 0
  else
    log_warning "$env_name 缺少 ${#missing_tables[@]} 个hot表: ${missing_tables[*]}"
    return 1
  fi
}

# 检查VIEW是否存在
check_views() {
  local env_name=$1
  local psql_cmd=$2
  
  log_info "检查 $env_name 的VIEW..."
  
  local views=(
    "request_logs_with_current_month"
    "usage_ledger_with_current_month"
    "request_wal_with_current_month"
    "routing_decision_log_with_current_month"
    "credential_model_index_with_current_month"
    "credit_ledger_with_current_month"
    "tool_usage_stats_with_current_month"
    "request_logs_bodies_with_current_month"
  )
  
  local missing_views=()
  
  for view in "${views[@]}"; do
    local exists=$(eval "$psql_cmd -t -c \"SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = '$view')\"" 2>/dev/null | xargs)
    
    if [[ "$exists" == "t" ]]; then
      log_success "  $view 存在"
    else
      log_warning "  $view 不存在"
      missing_views+=("$view")
    fi
  done
  
  if [ ${#missing_views[@]} -eq 0 ]; then
    log_success "$env_name 所有VIEW都存在"
    return 0
  else
    log_warning "$env_name 缺少 ${#missing_views[@]} 个VIEW"
    return 1
  fi
}

# 检查promote函数
check_promote_functions() {
  local env_name=$1
  local psql_cmd=$2
  
  log_info "检查 $env_name 的promote函数..."
  
  local functions=(
    "promote_request_logs_hot_to_partition"
    "promote_usage_ledger_hot_to_partition"
    "promote_request_wal_hot_to_partition"
    "promote_routing_decision_log_hot_to_partition"
    "promote_credential_model_index_hot_to_partition"
    "promote_credit_ledger_hot_to_partition"
    "promote_tool_usage_stats_hot_to_partition"
    "promote_request_logs_bodies_hot_to_partition"
  )
  
  local missing_functions=()
  
  for func in "${functions[@]}"; do
    local exists=$(eval "$psql_cmd -t -c \"SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = '$func')\"" 2>/dev/null | xargs)
    
    if [[ "$exists" == "t" ]]; then
      log_success "  $func 存在"
    else
      log_warning "  $func 不存在"
      missing_functions+=("$func")
    fi
  done
  
  if [ ${#missing_functions[@]} -eq 0 ]; then
    log_success "$env_name 所有promote函数都存在"
    return 0
  else
    log_warning "$env_name 缺少 ${#missing_functions[@]} 个promote函数"
    return 1
  fi
}

# 检查hot表数据量
check_hot_table_data() {
  local env_name=$1
  local psql_cmd=$2
  
  log_info "检查 $env_name hot表数据量..."
  
  local query="
    SELECT 
      'request_logs_hot' as table_name,
      count(*) as row_count,
      COALESCE(min(ts), now()) as oldest,
      COALESCE(max(ts), now()) as newest,
      pg_size_pretty(pg_total_relation_size('request_logs_hot')) as size
    FROM request_logs_hot
    UNION ALL
    SELECT 'usage_ledger_hot', count(*), 
      COALESCE(min(ts), now()), COALESCE(max(ts), now()),
      pg_size_pretty(pg_total_relation_size('usage_ledger_hot'))
    FROM usage_ledger_hot
    UNION ALL
    SELECT 'credit_ledger_hot', count(*), 
      COALESCE(min(created_at), now()), COALESCE(max(created_at), now()),
      pg_size_pretty(pg_total_relation_size('credit_ledger_hot'))
    FROM credit_ledger_hot
    UNION ALL
    SELECT 'tool_usage_stats_hot', count(*), 
      COALESCE(min(usage_date::timestamp), now()), COALESCE(max(usage_date::timestamp), now()),
      pg_size_pretty(pg_total_relation_size('tool_usage_stats_hot'))
    FROM tool_usage_stats_hot;
  "
  
  log_info "$env_name hot表统计:"
  eval "$psql_cmd -c \"$query\"" 2>/dev/null || log_warning "  部分表可能不存在"
}

# 检查_default分区是否还存在（应该已删除）
check_default_partitions() {
  local env_name=$1
  local psql_cmd=$2
  
  log_info "检查 $env_name 是否还有_default分区（应该已删除）..."
  
  local default_tables=(
    "request_logs_default"
    "usage_ledger_default"
    "request_wal_default"
    "routing_decision_log_default"
    "credential_model_index_default"
    "credit_ledger_default"
    "tool_usage_stats_default"
    "request_logs_bodies_default"
  )
  
  local found_defaults=()
  
  for table in "${default_tables[@]}"; do
    local exists=$(eval "$psql_cmd -t -c \"SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = '$table')\"" 2>/dev/null | xargs)
    
    if [[ "$exists" == "t" ]]; then
      log_warning "  $table 仍然存在（应该已迁移到hot表）"
      found_defaults+=("$table")
    fi
  done
  
  if [ ${#found_defaults[@]} -eq 0 ]; then
    log_success "$env_name 所有_default分区已清理"
    return 0
  else
    log_warning "$env_name 还有 ${#found_defaults[@]} 个_default分区未迁移"
    return 1
  fi
}

# 检查服务状态
check_service_health() {
  local env_name=$1
  local service_url=$2
  
  log_info "检查 $env_name 服务健康状态..."
  
  # 尝试访问健康检查端点
  if curl -sf "$service_url/health" > /dev/null 2>&1; then
    log_success "$env_name 服务运行正常"
    return 0
  elif curl -sf "$service_url/" > /dev/null 2>&1; then
    log_success "$env_name 服务可访问"
    return 0
  else
    log_warning "$env_name 服务无法访问（可能未启动或端口不同）"
    return 1
  fi
}

# 检查代码是否使用hot表
check_code_usage() {
  log_info "检查代码中是否正确使用hot表..."
  
  cd "$SCRIPT_DIR/.."
  
  # 检查是否还有使用_default的代码
  local bad_usage=$(grep -r "INSERT INTO.*_default\|UPDATE.*_default" \
    --include="*.go" \
    --exclude-dir="_to-be-deprecated" \
    . 2>/dev/null || true)
  
  if [[ -z "$bad_usage" ]]; then
    log_success "代码中没有使用_default分区的INSERT/UPDATE"
  else
    log_error "代码中仍在使用_default分区:"
    echo "$bad_usage"
    return 1
  fi
  
  # 检查是否正确使用_hot表
  local hot_usage=$(grep -r "INSERT INTO.*_hot\|UPDATE.*_hot" \
    --include="*.go" \
    --exclude-dir="_to-be-deprecated" \
    . 2>/dev/null | wc -l)
  
  if [[ $hot_usage -gt 0 ]]; then
    log_success "代码中有 $hot_usage 处正确使用_hot表"
  else
    log_warning "代码中没有找到使用_hot表的地方"
  fi
}

# ============================================================
# 主流程
# ============================================================

main() {
  echo ""
  echo "=========================================="
  echo "  分区表架构验证"
  echo "  时间: $(date)"
  echo "=========================================="
  echo ""
  
  local local_ok=true
  local remote_ok=true
  
  # ====================================
  # 1. 检查本地环境
  # ====================================
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  1️⃣  本地环境检查"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  
  if check_db_connection "本地" "$LOCAL_PSQL"; then
    check_hot_tables "本地" "$LOCAL_PSQL" || local_ok=false
    check_views "本地" "$LOCAL_PSQL" || local_ok=false
    check_promote_functions "本地" "$LOCAL_PSQL" || local_ok=false
    check_default_partitions "本地" "$LOCAL_PSQL" || true
    echo ""
    check_hot_table_data "本地" "$LOCAL_PSQL" || true
  else
    log_error "无法连接本地数据库，跳过本地检查"
    local_ok=false
  fi
  
  echo ""
  check_service_health "本地" "$LOCAL_SERVICE_URL" || true
  
  # ====================================
  # 2. 检查184环境
  # ====================================
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  2️⃣  184环境检查"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  
  if check_db_connection "184" "$REMOTE_184_PSQL"; then
    check_hot_tables "184" "$REMOTE_184_PSQL" || remote_ok=false
    check_views "184" "$REMOTE_184_PSQL" || remote_ok=false
    check_promote_functions "184" "$REMOTE_184_PSQL" || remote_ok=false
    check_default_partitions "184" "$REMOTE_184_PSQL" || true
    echo ""
    check_hot_table_data "184" "$REMOTE_184_PSQL" || true
  else
    log_error "无法连接184数据库，跳过184检查"
    remote_ok=false
  fi
  
  echo ""
  check_service_health "184" "$REMOTE_184_SERVICE_URL" || true
  
  # ====================================
  # 3. 代码检查
  # ====================================
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  3️⃣  代码检查"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo ""
  
  check_code_usage || true
  
  # ====================================
  # 4. 总结
  # ====================================
  echo ""
  echo "=========================================="
  echo "  检查总结"
  echo "=========================================="
  echo ""
  
  if $local_ok; then
    log_success "本地环境: 架构完整 ✓"
  else
    log_warning "本地环境: 需要执行迁移或修复"
    echo "  运行: ./scripts/apply-hot-table-migrations.sh --env local"
  fi
  
  if $remote_ok; then
    log_success "184环境: 架构完整 ✓"
  else
    log_warning "184环境: 需要执行迁移或修复"
    echo "  运行: ./scripts/apply-hot-table-migrations.sh --env prod"
  fi
  
  echo ""
  
  if $local_ok && $remote_ok; then
    log_success "🎉 所有环境检查通过！"
    return 0
  else
    log_warning "⚠️  部分环境需要修复"
    return 1
  fi
}

# ============================================================
# 执行
# ============================================================

main "$@"
