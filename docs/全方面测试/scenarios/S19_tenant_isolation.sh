#!/usr/bin/env bash
# S19: 租户隔离验证 (Tenant Isolation for Pending Responses)
# 
# 验证 pending response 的租户隔离和权限验证机制
# 
# 背景: 2026-07-19 修复 fix(streaming): pending continuation audit P1/P2 repairs
# - 问题：pending replay 缺乏租户验证，可能泄露跨租户数据
# - 修复：公开端点要求精确租户匹配，管理端点租户作用域隔离

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S19_tenant_isolation"
GATEWAY="${GATEWAY_URL:-http://localhost:8781}"
RESULTS_DIR="$SCRIPT_DIR/../results"
OUTPUT_FILE="$RESULTS_DIR/${SCENARIO}.json"

log_info "Starting $SCENARIO: Tenant Isolation for Pending Responses"

# 确保输出目录存在
mkdir -p "$RESULTS_DIR"

# 测试参数
MODEL="loadtest-mini-alpha"
TENANT_A_KEY="${TENANT_A_API_KEY:-sk-stress-test-01-hash-xxx}"
TENANT_B_KEY="${TENANT_B_API_KEY:-sk-stress-test-02-hash-yyy}"
CLIENTS=10
ROUNDS=2

log_info "Configuration:"
log_info "  Gateway: $GATEWAY"
log_info "  Model: $MODEL"
log_info "  Tenant A Key: ${TENANT_A_KEY:0:20}..."
log_info "  Tenant B Key: ${TENANT_B_KEY:0:20}..."

# Step 1: Tenant A 创建 pending response
log_info "Step 1: Creating pending response for Tenant A..."

python3 "$SCRIPT_DIR/../tools/loadtest.py" \
  --gateway "$GATEWAY" \
  --api-keys "$TENANT_A_KEY" \
  --clients "$CLIENTS" \
  --rounds "$ROUNDS" \
  --models "$MODEL" \
  --stream \
  --disconnect-ratio 1.0 \
  --prompt-size short \
  --output "${RESULTS_DIR}/S19-tenant-a.json" || {
    log_error "Tenant A loadtest failed"
    exit 1
  }

# Step 2: 提取 session_id
SESSION_ID=$(jq -r '.requests[0].session_id // empty' "${RESULTS_DIR}/S19-tenant-a.json")

if [ -z "$SESSION_ID" ]; then
  log_error "Failed to extract session_id from Tenant A results"
  exit 1
fi

log_info "Session ID: $SESSION_ID"

# 验收标准
PASS=true

# Step 3: Tenant A 访问自己的 pending（应成功）
log_info "Step 3: Tenant A accessing own pending response..."

RESPONSE_A=$(curl -s -w "\n%{http_code}" \
  -H "Authorization: Bearer $TENANT_A_KEY" \
  "${GATEWAY}/v1/sessions/${SESSION_ID}/pending-response")

HTTP_CODE_A=$(echo "$RESPONSE_A" | tail -n 1)
BODY_A=$(echo "$RESPONSE_A" | head -n -1)

if [ "$HTTP_CODE_A" = "200" ]; then
  log_success "PASS: Tenant A can access own pending (HTTP 200)"
else
  log_error "FAIL: Tenant A cannot access own pending (HTTP ${HTTP_CODE_A})"
  PASS=false
fi

# Step 4: Tenant B 尝试访问 Tenant A 的 pending（应 404）
log_info "Step 4: Tenant B attempting to access Tenant A's pending..."

RESPONSE_B=$(curl -s -w "\n%{http_code}" \
  -H "Authorization: Bearer $TENANT_B_KEY" \
  "${GATEWAY}/v1/sessions/${SESSION_ID}/pending-response")

HTTP_CODE_B=$(echo "$RESPONSE_B" | tail -n 1)

if [ "$HTTP_CODE_B" = "404" ] || [ "$HTTP_CODE_B" = "403" ]; then
  log_success "PASS: Tenant B blocked from Tenant A's pending (HTTP ${HTTP_CODE_B})"
else
  log_error "FAIL: Tenant B accessed Tenant A's pending (HTTP ${HTTP_CODE_B})"
  log_error "Response: $RESPONSE_B"
  PASS=false
fi

# Step 5: 无 token 访问（应 401 或 404）
log_info "Step 5: Attempting access without token..."

RESPONSE_NOAUTH=$(curl -s -w "\n%{http_code}" \
  "${GATEWAY}/v1/sessions/${SESSION_ID}/pending-response")

HTTP_CODE_NOAUTH=$(echo "$RESPONSE_NOAUTH" | tail -n 1)

if [ "$HTTP_CODE_NOAUTH" = "401" ] || [ "$HTTP_CODE_NOAUTH" = "404" ]; then
  log_success "PASS: No auth blocked (HTTP ${HTTP_CODE_NOAUTH})"
else
  log_error "FAIL: No auth not blocked (HTTP ${HTTP_CODE_NOAUTH})"
  PASS=false
fi

# Step 6: 测试不存在的 session（应 404，不泄露存在性）
log_info "Step 6: Attempting access to non-existent session..."

FAKE_SESSION="00000000-0000-0000-0000-000000000000"
RESPONSE_FAKE=$(curl -s -w "\n%{http_code}" \
  -H "Authorization: Bearer $TENANT_A_KEY" \
  "${GATEWAY}/v1/sessions/${FAKE_SESSION}/pending-response")

HTTP_CODE_FAKE=$(echo "$RESPONSE_FAKE" | tail -n 1)

if [ "$HTTP_CODE_FAKE" = "404" ]; then
  log_success "PASS: Non-existent session returns 404"
else
  log_warn "WARN: Non-existent session returns HTTP ${HTTP_CODE_FAKE}"
fi

# 保存测试结果
cat > "$OUTPUT_FILE" <<EOF
{
  "scenario": "$SCENARIO",
  "timestamp": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "session_id": "$SESSION_ID",
  "tests": {
    "tenant_a_own_access": {
      "http_code": $HTTP_CODE_A,
      "expected": 200,
      "pass": $([ "$HTTP_CODE_A" = "200" ] && echo "true" || echo "false")
    },
    "tenant_b_cross_access": {
      "http_code": $HTTP_CODE_B,
      "expected": "404 or 403",
      "pass": $([ "$HTTP_CODE_B" = "404" ] || [ "$HTTP_CODE_B" = "403" ] && echo "true" || echo "false")
    },
    "no_auth_access": {
      "http_code": $HTTP_CODE_NOAUTH,
      "expected": "401 or 404",
      "pass": $([ "$HTTP_CODE_NOAUTH" = "401" ] || [ "$HTTP_CODE_NOAUTH" = "404" ] && echo "true" || echo "false")
    },
    "non_existent_session": {
      "http_code": $HTTP_CODE_FAKE,
      "expected": 404,
      "pass": $([ "$HTTP_CODE_FAKE" = "404" ] && echo "true" || echo "false")
    }
  },
  "overall_pass": $([ "$PASS" = true ] && echo "true" || echo "false")
}
EOF

# 输出最终结果
if [ "$PASS" = true ]; then
  log_success "$SCENARIO PASSED"
  exit 0
else
  log_error "$SCENARIO FAILED"
  exit 1
fi
