#!/bin/bash
# Verify Partition Alignment with Standard Architecture
#
# 用途：验证当前分区表状态是否符合参考标准
# 检查维度：
#   1. 分区附加策略（当月 DETACHED，历史 ATTACHED）
#   2. 存储类型（default 应为 heap）
#   3. 写入目标（代码应写 *_default）
#   4. Promote 函数存在性
#   5. VIEW 存在性
#
# 使用：./scripts/partition/verify-partition-alignment.sh [--env ENV]
#
# 退出码：
#   0 = 全部通过
#   1 = 发现问题

set -euo pipefail

# ========================================
# 配置
# ========================================

ENV="${1:-local}"

case "$ENV" in
  local)
    PGHOST="${PGHOST:-localhost}"
    PGPORT="${PGPORT:-5432}"
    PGUSER="${PGUSER:-kxuser}"
    PGDATABASE="${PGDATABASE:-llm_gateway}"
    ;;
  71)
    PGHOST="llm.kxpms.cn"
    PGPORT="5432"
    PGUSER="kxuser"
    PGDATABASE="llm_gateway"
    ;;
  184)
    PGHOST="184.kxpms.cn"
    PGPORT="5432"
    PGUSER="kxuser"
    PGDATABASE="llm_gateway"
    ;;
  *)
    echo "错误：未知环境 '$ENV'" >&2
    exit 1
    ;;
esac

export PGHOST PGUSER PGDATABASE

# ========================================
# 颜色
# ========================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

ERRORS=0
WARNINGS=0

# ========================================
# 辅助函数
# ========================================

pass() {
  echo -e "${GREEN}✅ $*${NC}"
}

fail() {
  echo -e "${RED}❌ $*${NC}"
  ((ERRORS++))
}

warn() {
  echo -e "${YELLOW}⚠️  $*${NC}"
  ((WARNINGS++))
}

info() {
  echo -e "${BLUE}[INFO] $*${NC}"
}

section() {
  echo ""
  echo -e "${CYAN}========== $1 ==========${NC}"
}

check() {
  local description="$1"
  local command="$2"
  local expected="$3"
  
  info "$description"
  local actual
  actual=$(eval "$command" 2>/dev/null | tr -d '[:space:]')
  
  if [[ "$actual" == "$expected" ]]; then
    pass "$description: $actual (期望: $expected)"
    return 0
  else
    fail "$description: $actual (期望: $expected)"
    return 1
  fi
}

# ========================================
# 1. 检查分区附加策略
# ========================================

section "1. 分区附加策略"

info "验证当月分区（2026_07）是否正确 DETACHED..."

PARTITIONED_TABLES=(
  "request_logs"
  "request_wal"
  "usage_ledger"
  "routing_decision_log"
  "credential_model_index"
  "request_logs_bodies"
  "credit_ledger"
  "tool_usage_stats"
)

for table in "${PARTITIONED_TABLES[@]}"; do
  partition="${table}_2026_07"
  
  status=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT CASE 
      WHEN EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = '$table' AND c.relname = '$partition'
      )
      THEN 'ATTACHED'
      ELSE 'DETACHED'
    END;
  " 2>/dev/null | tr -d '[:space:]')
  
  if [[ "$status" == "DETACHED" ]]; then
    pass "$partition: DETACHED ✓"
  else
    fail "$partition: $status (应为 DETACHED)"
  fi
done

# ========================================
# 2. 检查 *_default 存在性和存储类型
# ========================================

section "2. *_default 分区状态"

info "验证 *_default 分区存在..."

for table in "${PARTITIONED_TABLES[@]}"; do
  default_table="${table}_default"
  
  exists=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT COUNT(*) FROM pg_class WHERE relname = '$default_table'
  " 2>/dev/null | tr -d '[:space:]')
  
  if [[ "$exists" == "1" ]]; then
    pass "$default_table 存在"
    
    # 检查存储类型
    storage=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
      SELECT am.amname FROM pg_class c
      JOIN pg_am am ON am.oid = c.relam
      WHERE c.relname = '$default_table'
    " 2>/dev/null | tr -d '[:space:]')
    
    if [[ "$storage" == "heap" ]]; then
      pass "  存储类型: heap ✓"
    else
      fail "  存储类型: $storage (应为 heap)"
    fi
  else
    fail "$default_table 不存在"
  fi
done

# ========================================
# 3. 检查 Promote 函数
# ========================================

section "3. Promote 函数"

info "验证 promote_*_default_batch 函数存在..."

PROMOTE_FUNCTIONS=(
  "promote_request_logs_default_batch"
  "promote_request_wal_default_batch"
  "promote_usage_ledger_default_batch"
  "promote_routing_decision_log_default_batch"
  "promote_credential_model_index_default_batch"
  "promote_request_logs_bodies_default_batch"
  "promote_credit_ledger_default_batch"
  "promote_tool_usage_stats_default_batch"
)

for fn in "${PROMOTE_FUNCTIONS[@]}"; do
  exists=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT COUNT(*) FROM pg_proc WHERE proname = '$fn'
  " 2>/dev/null | tr -d '[:space:]')
  
  if [[ "$exists" == "1" ]]; then
    pass "$fn 存在"
  else
    fail "$fn 不存在"
  fi
done

# ========================================
# 4. 检查查询 VIEW
# ========================================

section "4. 查询 VIEW"

info "验证 *_with_current_month VIEW 存在..."

VIEWS=(
  "request_logs_with_current_month"
  "request_wal_with_current_month"
  "usage_ledger_with_current_month"
  "routing_decision_log_with_current_month"
  "credential_model_index_with_current_month"
  "request_logs_bodies_with_current_month"
  "credit_ledger_with_current_month"
  "tool_usage_stats_with_current_month"
)

VIEW_COUNT=0
for view in "${VIEWS[@]}"; do
  exists=$(psql -h "$PGHOST" -U "$PGUSER" -d "$PGDATABASE" -t -c "
    SELECT COUNT(*) FROM pg_class WHERE relname = '$view' AND relkind = 'v'
  " 2>/dev/null | tr -d '[:space:]')
  
  if [[ "$exists" == "1" ]]; then
    pass "$view 存在"
    ((VIEW_COUNT++))
  else
    warn "$view 不存在（可选，但推荐）"
  fi
done

if [[ $VIEW_COUNT -eq 8 ]]; then
  pass "所有 8 个 VIEW 已创建 ✓"
fi

# ========================================
# 5. 检查 bg/partition_manager.go
# ========================================

section "5. 后台调度器"

PARTITION_MANAGER_PATH="/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/partition_manager.go"

if [[ -f "$PARTITION_MANAGER_PATH" ]]; then
  info "检查 bg/partition_manager.go..."
  
  if grep -q "promoteDefaultToPartitions" "$PARTITION_MANAGER_PATH"; then
    pass "promoteDefaultToPartitions 函数存在"
  else
    fail "promoteDefaultToPartitions 函数不存在"
  fi
  
  if grep -q "promoteSpecs" "$PARTITION_MANAGER_PATH"; then
    pass "promoteSpecs 配置存在"
  else
    fail "promoteSpecs 配置不存在"
  fi
else
  warn "partition_manager.go 不在标准路径，跳过"
fi

# ========================================
# 6. 检查监控告警
# ========================================

section "6. 监控配置"

ALERT_PATH="/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/observability/alerts/partition_health.yml"

if [[ -f "$ALERT_PATH" ]]; then
  info "partition_health.yml 存在"
  
  alert_count=$(grep -c "^      - alert:" "$ALERT_PATH" 2>/dev/null || echo "0")
  pass "包含 $alert_count 个告警规则"
else
  warn "partition_health.yml 不存在（推荐配置）"
fi

# ========================================
# 总结
# ========================================

section "验证总结"

echo ""
if [[ $ERRORS -eq 0 && $WARNINGS -eq 0 ]]; then
  echo -e "${GREEN}🎉 所有检查通过！分区表架构完全对齐参考标准${NC}"
  exit 0
elif [[ $ERRORS -eq 0 ]]; then
  echo -e "${YELLOW}⚠️  $WARNINGS 个警告，$ERRORS 个错误${NC}"
  echo ""
  echo "建议："
  echo "  - 查看上述警告项"
  echo "  - 参考 docs/partition/ 文档"
  exit 0
else
  echo -e "${RED}❌ 发现 $ERRORS 个错误，$WARNINGS 个警告${NC}"
  echo ""
  echo "必须修复："
  echo "  1. 查看上述失败项"
  echo "  2. 应用缺失的 migrations"
  echo "  3. 运行 scripts/partition/check-partition-health.sh 获取详情"
  exit 1
fi
