#!/bin/bash
# verify-columnar-fix.sh - 验证 columnar 分区修复是否生效
# 测试 request_logs 和 usage_ledger 的 INSERT 和 UPDATE 是否正常工作

set -euo pipefail

# 颜色输出
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[✓]${NC} $1"
}

log_error() {
    echo -e "${RED}[✗]${NC} $1"
}

log_step() {
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}$1${NC}"
    echo -e "${GREEN}========================================${NC}"
}

# 检查数据库连接
if [ -z "${LLM_GATEWAY_DATABASE_URL:-}" ]; then
    log_error "LLM_GATEWAY_DATABASE_URL 环境变量未设置"
    echo "请设置数据库连接字符串，例如："
    echo "export LLM_GATEWAY_DATABASE_URL='postgres://user:pass@host:port/dbname'"
    exit 1
fi

DB_URL="$LLM_GATEWAY_DATABASE_URL"

log_step "步骤 1: 检查 default 分区是否存在"

# 检查 usage_ledger_default
if psql "$DB_URL" -tAc "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'usage_ledger_default')" | grep -q 't'; then
    log_success "usage_ledger_default 表存在"
else
    log_error "usage_ledger_default 表不存在"
    exit 1
fi

# 检查 request_logs_default
if psql "$DB_URL" -tAc "SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'request_logs_default')" | grep -q 't'; then
    log_success "request_logs_default 表存在"
else
    log_error "request_logs_default 表不存在"
    exit 1
fi

log_step "步骤 2: 测试 INSERT 操作"

TEST_REQUEST_ID="test-columnar-fix-$(date +%s)"
log_info "使用测试 request_id: $TEST_REQUEST_ID"

# 测试 usage_ledger INSERT
log_info "测试 usage_ledger INSERT..."
psql "$DB_URL" <<EOF
INSERT INTO usage_ledger (
    request_id, ts, tenant_id, application_id, api_key_id,
    end_user_id, credential_id, provider_id, canonical_id,
    raw_model_name, prompt_tokens, completion_tokens,
    total_tokens, cost_usd, latency_ms, success, error_kind
) VALUES (
    '$TEST_REQUEST_ID', now(), 'test-tenant', 1, 1,
    'test-user', 1, 1, 1,
    'gpt-4', 100, 50,
    150, 0.001, 1000, false, 'test_error'
);
EOF

if [ $? -eq 0 ]; then
    log_success "usage_ledger INSERT 成功"
else
    log_error "usage_ledger INSERT 失败"
    exit 1
fi

# 测试 request_logs INSERT
log_info "测试 request_logs INSERT..."
psql "$DB_URL" <<EOF
INSERT INTO request_logs (
    request_id, ts, tenant_id, application_id, api_key_id,
    end_user_id, client_model, outbound_model,
    credential_id, provider_id, canonical_id,
    client_profile, request_mode,
    prompt_tokens, completion_tokens, total_tokens,
    cost_usd, latency_ms, success, request_status, error_kind
) VALUES (
    '$TEST_REQUEST_ID', now(), 'test-tenant', 1, 1,
    'test-user', 'gpt-4', 'gpt-4',
    1, 1, 1,
    'default', 'sync',
    100, 50, 150,
    0.001, 1000, false, 'in_progress', 'test_error'
);
EOF

if [ $? -eq 0 ]; then
    log_success "request_logs INSERT 成功"
else
    log_error "request_logs INSERT 失败"
    exit 1
fi

log_step "步骤 3: 测试 UPDATE 操作（关键测试）"

# 测试 usage_ledger_default UPDATE
log_info "测试 usage_ledger_default UPDATE..."
psql "$DB_URL" <<EOF
UPDATE usage_ledger_default
   SET success = true,
       error_kind = NULL,
       latency_ms = 500
 WHERE request_id = '$TEST_REQUEST_ID';
EOF

if [ $? -eq 0 ]; then
    log_success "usage_ledger_default UPDATE 成功"
else
    log_error "usage_ledger_default UPDATE 失败"
    exit 1
fi

# 验证 UPDATE 结果
SUCCESS=$(psql "$DB_URL" -tAc "SELECT success FROM usage_ledger WHERE request_id = '$TEST_REQUEST_ID'")
if [ "$SUCCESS" = "t" ]; then
    log_success "usage_ledger UPDATE 数据验证成功 (success=true)"
else
    log_error "usage_ledger UPDATE 数据验证失败 (success=$SUCCESS)"
    exit 1
fi

# 测试 request_logs_default UPDATE
log_info "测试 request_logs_default UPDATE..."
psql "$DB_URL" <<EOF
UPDATE request_logs_default
   SET success = true,
       request_status = 'success',
       error_kind = NULL,
       latency_ms = 500
 WHERE request_id = '$TEST_REQUEST_ID';
EOF

if [ $? -eq 0 ]; then
    log_success "request_logs_default UPDATE 成功"
else
    log_error "request_logs_default UPDATE 失败"
    exit 1
fi

# 验证 UPDATE 结果
SUCCESS=$(psql "$DB_URL" -tAc "SELECT success FROM request_logs WHERE request_id = '$TEST_REQUEST_ID'")
STATUS=$(psql "$DB_URL" -tAc "SELECT request_status FROM request_logs WHERE request_id = '$TEST_REQUEST_ID'")

if [ "$SUCCESS" = "t" ] && [ "$STATUS" = "success" ]; then
    log_success "request_logs UPDATE 数据验证成功 (success=true, status=success)"
else
    log_error "request_logs UPDATE 数据验证失败 (success=$SUCCESS, status=$STATUS)"
    exit 1
fi

log_step "步骤 4: 清理测试数据"

log_info "删除测试数据..."
psql "$DB_URL" <<EOF
DELETE FROM usage_ledger WHERE request_id = '$TEST_REQUEST_ID';
DELETE FROM request_logs WHERE request_id = '$TEST_REQUEST_ID';
EOF

log_success "测试数据已清理"

log_step "验证完成"
echo ""
log_success "所有测试通过！columnar 分区修复已生效"
log_info "修复内容："
echo "  - UPDATE usage_ledger → UPDATE usage_ledger_default"
echo "  - UPDATE request_logs → UPDATE request_logs_default"
echo "  - 避免扫描 columnar 分区，防止 CTID 扫描错误"
echo ""
