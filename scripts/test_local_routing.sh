#!/bin/bash
# ===========================================================================
# scripts/test_local_routing.sh
# 阶段1本地测试：路由稳定性测试
# - 测试多个模型（minimax 系列 + gpt + claude）
# - 测试不存在的模型（应当返回 no_candidate）
# - 测试未授权（应当返回 401）
# - 测试错误 API key
# - 验证每次请求都被记录到 request_logs / candidate_failure_logs
# ===========================================================================

set -e

GATEWAY_URL="http://localhost:8080"
API_KEY="test-api-key-12345"

echo "=========================================="
echo "📡 阶段 1: 本地路由稳定性测试"
echo "=========================================="
echo ""

# 清理之前的测试数据
echo "🧹 清理之前的测试数据..."
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
DELETE FROM request_logs WHERE ts > now() - interval '5 minutes';
DELETE FROM request_wal WHERE created_at > now() - interval '5 minutes';
DELETE FROM candidate_failure_logs WHERE ts > now() - interval '5 minutes';
" > /dev/null 2>&1
echo ""

echo "=========================================="
echo "📋 测试 1: 不存在的模型（应当返回 503 no_candidate）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"nonexistent-fake-model","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
RESP_TIME=$(echo "$RESP" | tail -1 | cut -d'|' -f2)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE, Time: ${RESP_TIME}s"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 2: minimax-m2.7（路由到 mock minimax provider）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax-m2.7","messages":[{"role":"user","content":"Say hello"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
RESP_TIME=$(echo "$RESP" | tail -1 | cut -d'|' -f2)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE, Time: ${RESP_TIME}s"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 3: minimax-m3（路由到 minimax provider）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax-m3","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
RESP_TIME=$(echo "$RESP" | tail -1 | cut -d'|' -f2)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE, Time: ${RESP_TIME}s"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 4: gpt-4（路由到多个 mock provider）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
RESP_TIME=$(echo "$RESP" | tail -1 | cut -d'|' -f2)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE, Time: ${RESP_TIME}s"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 5: gpt-4o（路由到多个 mock provider）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
RESP_TIME=$(echo "$RESP" | tail -1 | cut -d'|' -f2)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE, Time: ${RESP_TIME}s"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 6: claude-3-5-sonnet（路由到 mock provider）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
RESP_TIME=$(echo "$RESP" | tail -1 | cut -d'|' -f2)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE, Time: ${RESP_TIME}s"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 7: 无 API key（应当返回 401）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 8: 错误 API key（应当返回 401）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer wrong-key-12345" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hi"}],"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📋 测试 9: 流式请求（SSE）"
echo "=========================================="
RESP=$(timeout 3 curl -s -w "\n%{http_code}" -N -X POST $GATEWAY_URL/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax-m2.7","messages":[{"role":"user","content":"Stream test"}],"stream":true,"max_tokens":10}')
HTTP_CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | head -c 300)
echo "HTTP: $HTTP_CODE"
echo "Body (前 300 字节): $BODY"
echo ""

echo "=========================================="
echo "📋 测试 10: Anthropic Messages API（minimax-m2.7）"
echo "=========================================="
RESP=$(curl -s -w "\n%{http_code}|%{time_total}" -X POST $GATEWAY_URL/v1/messages \
  -H "Authorization: Bearer $API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax-m2.7","max_tokens":10,"messages":[{"role":"user","content":"Hi"}]}')
HTTP_CODE=$(echo "$RESP" | tail -1 | cut -d'|' -f1)
BODY=$(echo "$RESP" | head -1)
echo "HTTP: $HTTP_CODE"
echo "Body: $BODY"
echo ""

echo "=========================================="
echo "📊 数据库验证"
echo "=========================================="
sleep 2  # 等异步批量 flush
echo ""
echo "🔍 candidate_failure_logs（前 20）:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT request_id, provider_id, credential_id, raw_model_name, failure_stage, failure_status_code, per_attempt_latency_ms
FROM candidate_failure_logs
ORDER BY ts DESC LIMIT 20;
"
echo ""
echo "🔍 request_logs 失败计数（最近 5 分钟）:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT client_model, request_status, success, error_kind, count(*)
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY client_model, request_status, success, error_kind
ORDER BY client_model;
"
echo ""
echo "✅ 测试完成"