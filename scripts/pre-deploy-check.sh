#!/bin/bash
# LLM Gateway 部署前自动化健康检查脚本
# 用途：在部署新版本前自动验证系统健康状态
#
# 用法:
#   ./scripts/pre-deploy-check.sh                         # 标准检查
#   ./scripts/pre-deploy-check.sh --partition-archive     # 含分区归档检查
#   ./scripts/pre-deploy-check.sh --skip-db               # 跳过数据库检查

set -e

# 配置
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8781}"
DB_HOST="${DB_HOST:-172.31.0.3}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_PASSWORD="${DB_PASSWORD:-4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg}"

# 参数解析
CHECK_PARTITION_ARCHIVE=0
SKIP_DB=0
for arg in "$@"; do
  case "$arg" in
    --partition-archive) CHECK_PARTITION_ARCHIVE=1 ;;
    --skip-db)           SKIP_DB=1 ;;
  esac
done

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# 计数器
TOTAL_CHECKS=0
PASSED_CHECKS=0
FAILED_CHECKS=0

# 日志函数
check_start() {
    ((TOTAL_CHECKS++))
    echo -ne "${YELLOW}[CHECK $TOTAL_CHECKS]${NC} $1 ... "
}

check_pass() {
    echo -e "${GREEN}✓ PASS${NC}"
    ((PASSED_CHECKS++))
}

check_fail() {
    echo -e "${RED}✗ FAIL${NC}"
    echo "  原因: $1"
    ((FAILED_CHECKS++))
}

check_warn() {
    echo -e "${YELLOW}⚠ WARN${NC}"
    echo "  警告: $1"
}

# 数据库查询辅助函数
db_query() {
    PGPASSWORD="$DB_PASSWORD" psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -t -A -c "$1" 2>/dev/null
}

echo "=========================================="
echo "LLM Gateway 部署前健康检查"
echo "=========================================="
echo "时间: $(date)"
echo "网关: $GATEWAY_URL"
echo ""

# ============================================
# 1. 基础设施检查
# ============================================
echo "【第 1 部分：基础设施】"
echo ""

# 1.1 数据库连通性
check_start "数据库连通性"
if db_query "SELECT 1" | grep -q "1"; then
    check_pass
else
    check_fail "无法连接到数据库"
    exit 1
fi

# 1.2 数据库版本
check_start "数据库迁移版本"
DB_VERSION=$(db_query "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
if [ -n "$DB_VERSION" ]; then
    check_pass
    echo "  当前版本: $DB_VERSION"
else
    check_fail "无法获取数据库版本"
fi

# 1.3 Redis 连通性（可选）
check_start "Redis 连通性"
if redis-cli -h localhost ping 2>/dev/null | grep -q "PONG"; then
    check_pass
else
    check_warn "Redis 不可用（非阻塞）"
fi

# ============================================
# 2. 数据一致性检查
# ============================================
echo ""
echo "【第 2 部分：数据一致性】"
echo ""

# 2.1 计费模式一致性
check_start "计费模式一致性"
MISMATCH_COUNT=$(db_query "
SELECT COUNT(*) 
FROM credentials c
JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
WHERE (c.plan_type = 'token' AND cmb.billing_mode != 'per_token')
   OR (c.plan_type IN ('token_plan', 'code_plan', 'agent_plan') 
       AND cmb.billing_mode NOT IN ('token_plan', 'code_plan', 'agent_plan'));
")
if [ "$MISMATCH_COUNT" -eq 0 ]; then
    check_pass
else
    check_fail "发现 $MISMATCH_COUNT 条不一致数据"
    echo "  修复命令: 见 docs/billing-mode-standardization.md"
fi

# 2.2 必需列存在性
check_start "必需数据库列"
PLAN_TYPE_EXISTS=$(db_query "
SELECT COUNT(*) 
FROM information_schema.columns 
WHERE table_name = 'credentials' AND column_name = 'plan_type';
")
PLAN_TYPE_ORIGIN_EXISTS=$(db_query "
SELECT COUNT(*) 
FROM information_schema.columns 
WHERE table_name = 'credential_model_bindings' AND column_name = 'plan_type_origin';
")

if [ "$PLAN_TYPE_EXISTS" -eq 1 ] && [ "$PLAN_TYPE_ORIGIN_EXISTS" -eq 1 ]; then
    check_pass
else
    check_fail "缺少必需列: plan_type=$PLAN_TYPE_EXISTS, plan_type_origin=$PLAN_TYPE_ORIGIN_EXISTS"
    echo "  修复命令: 应用迁移 327"
fi

# 2.3 可用模型数量
check_start "可用模型绑定数"
AVAILABLE_MODELS=$(db_query "SELECT COUNT(*) FROM credential_model_bindings WHERE available = true;")
if [ "$AVAILABLE_MODELS" -gt 100 ]; then
    check_pass
    echo "  可用模型: $AVAILABLE_MODELS"
else
    check_fail "可用模型过少: $AVAILABLE_MODELS (期望 >100)"
fi

# ============================================
# 3. 凭据健康检查
# ============================================
echo ""
echo "【第 3 部分：凭据健康】"
echo ""

# 3.1 活跃凭据数量
check_start "活跃凭据数量"
ACTIVE_CREDS=$(db_query "SELECT COUNT(*) FROM credentials WHERE status = 'active' AND lifecycle_status = 'active';")
if [ "$ACTIVE_CREDS" -gt 5 ]; then
    check_pass
    echo "  活跃凭据: $ACTIVE_CREDS"
else
    check_warn "活跃凭据较少: $ACTIVE_CREDS"
fi

# 3.2 熔断器状态
check_start "熔断器状态"
CIRCUIT_OPEN=$(db_query "SELECT COUNT(*) FROM credentials WHERE circuit_state = 'open' AND status = 'active';")
if [ "$CIRCUIT_OPEN" -eq 0 ]; then
    check_pass
else
    check_warn "$CIRCUIT_OPEN 个凭据熔断器打开"
fi

# 3.3 配额状态
check_start "配额状态"
QUOTA_EXHAUSTED=$(db_query "SELECT COUNT(*) FROM credentials WHERE quota_state IN ('exhausted', 'periodic_exhausted') AND status = 'active';")
if [ "$QUOTA_EXHAUSTED" -eq 0 ]; then
    check_pass
else
    check_warn "$QUOTA_EXHAUSTED 个凭据配额耗尽"
fi

# ============================================
# 4. 服务健康检查
# ============================================
echo ""
echo "【第 4 部分：服务健康】"
echo ""

# 4.1 健康端点
check_start "健康端点响应"
HEALTH_RESPONSE=$(curl -s "$GATEWAY_URL/healthz" 2>/dev/null || echo "")
if echo "$HEALTH_RESPONSE" | jq -e '.status == "ok"' >/dev/null 2>&1; then
    check_pass
    VERSION=$(echo "$HEALTH_RESPONSE" | jq -r '.version')
    echo "  版本: $VERSION"
else
    check_fail "健康端点返回异常: $HEALTH_RESPONSE"
fi

# 4.2 模型列表端点
check_start "模型列表端点"
MODELS_RESPONSE=$(curl -s "$GATEWAY_URL/v1/models" 2>/dev/null || echo "")
MODEL_COUNT=$(echo "$MODELS_RESPONSE" | jq -r '.data | length' 2>/dev/null || echo "0")
if [ "$MODEL_COUNT" -gt 10 ]; then
    check_pass
    echo "  可用模型: $MODEL_COUNT"
else
    check_fail "模型数量异常: $MODEL_COUNT"
fi

# ============================================
# 5. 性能检查
# ============================================
echo ""
echo "【第 5 部分：性能】"
echo ""

# 5.1 数据库查询性能
check_start "数据库查询性能"
START_TIME=$(date +%s%N)
db_query "SELECT COUNT(*) FROM request_logs WHERE created_at > NOW() - INTERVAL '1 hour';" >/dev/null
END_TIME=$(date +%s%N)
QUERY_TIME=$(( ($END_TIME - $START_TIME) / 1000000 ))  # 转换为毫秒

if [ "$QUERY_TIME" -lt 1000 ]; then
    check_pass
    echo "  查询时间: ${QUERY_TIME}ms"
else
    check_warn "查询时间较长: ${QUERY_TIME}ms"
fi

# 5.2 数据库连接数
check_start "数据库活跃连接"
ACTIVE_CONNS=$(db_query "SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$DB_NAME' AND state = 'active';")
if [ "$ACTIVE_CONNS" -lt 50 ]; then
    check_pass
    echo "  活跃连接: $ACTIVE_CONNS"
else
    check_warn "活跃连接较多: $ACTIVE_CONNS"
fi

# ============================================
# 6. 最近请求统计
# ============================================
echo ""
echo "【第 6 部分：最近请求】"
echo ""

# 6.1 最近 1 小时请求数
check_start "最近 1 小时请求数"
REQUEST_COUNT=$(db_query "SELECT COUNT(*) FROM request_logs WHERE created_at > NOW() - INTERVAL '1 hour';")
check_pass
echo "  请求数: $REQUEST_COUNT"

# 6.2 最近 1 小时成功率
check_start "最近 1 小时成功率"
SUCCESS_RATE=$(db_query "
SELECT ROUND(
    COUNT(CASE WHEN status = 200 THEN 1 END)::numeric / 
    NULLIF(COUNT(*), 0) * 100, 
    2
) 
FROM request_logs 
WHERE created_at > NOW() - INTERVAL '1 hour';
")

if [ -n "$SUCCESS_RATE" ]; then
    SUCCESS_RATE_INT=${SUCCESS_RATE%.*}
    if [ "$SUCCESS_RATE_INT" -ge 95 ]; then
        check_pass
        echo "  成功率: ${SUCCESS_RATE}%"
    else
        check_fail "成功率过低: ${SUCCESS_RATE}%"
    fi
else
    check_warn "无最近请求数据"
fi

# ============================================
# 7. 分区归档检查（可选: --partition-archive）
# ============================================
if [ "$CHECK_PARTITION_ARCHIVE" = "1" ]; then
  echo ""
  echo "【第 7 部分：分区归档】"
  echo ""

  EXPECTED_FUNCS=("archive_request_logs" "archive_request_wal" "archive_routing_decision_log" "archive_credential_model_index")
  for fn in "${EXPECTED_FUNCS[@]}"; do
    check_start "归档函数 $fn"
    FUNC_COUNT=$(db_query "SELECT COUNT(*) FROM pg_proc WHERE proname = '$fn';" 2>/dev/null || echo "0")
    if [ "$FUNC_COUNT" -gt 0 ]; then
      check_pass
    else
      check_fail "$fn() 函数不存在"
    fi
  done

  EXPECTED_TABLES=("request_logs_archive" "request_wal_archive" "routing_decision_log_archive" "credential_model_index_archive")
  for tbl in "${EXPECTED_TABLES[@]}"; do
    check_start "归档表 $tbl"
    TABLE_COUNT=$(db_query "SELECT COUNT(*) FROM pg_class WHERE relname = '$tbl' AND relnamespace = 'public'::regnamespace;" 2>/dev/null || echo "0")
    if [ "$TABLE_COUNT" -gt 0 ]; then
      check_pass
    else
      check_fail "$tbl 表不存在"
    fi
  done

  check_start "credential_model_index 分区检查"
  RELKIND=$(db_query "SELECT relkind FROM pg_class WHERE relname = 'credential_model_index' AND relnamespace = 'public'::regnamespace;" 2>/dev/null | tr -d ' ')
  if [ "$RELKIND" = "p" ]; then
    check_pass
  else
    check_fail "credential_model_index 未分区（relkind=$RELKIND）"
  fi
fi

# ============================================
# 汇总结果
# ============================================
echo ""
echo "=========================================="
echo "检查结果汇总"
echo "=========================================="
echo "总检查项: $TOTAL_CHECKS"
echo -e "${GREEN}通过: $PASSED_CHECKS${NC}"
echo -e "${RED}失败: $FAILED_CHECKS${NC}"
echo "通过率: $(awk "BEGIN {printf \"%.1f\", ($PASSED_CHECKS/$TOTAL_CHECKS)*100}")%"
echo ""

if [ $FAILED_CHECKS -eq 0 ]; then
    echo -e "${GREEN}✅ 所有关键检查通过，可以部署！${NC}"
    exit 0
else
    echo -e "${RED}❌ 有 $FAILED_CHECKS 项检查失败，请修复后再部署${NC}"
    echo ""
    echo "常见问题修复："
    echo "1. 计费模式不一致 → 见 docs/billing-mode-standardization.md"
    echo "2. 数据库列缺失 → 应用迁移 327"
    echo "3. 可用模型过少 → 检查模型发现日志"
    echo "4. 健康端点异常 → 检查服务日志"
    exit 1
fi
