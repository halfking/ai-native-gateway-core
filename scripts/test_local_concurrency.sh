#!/bin/bash
# ===========================================================================
# scripts/test_local_concurrency.sh
# 高并发测试：多终端（不同 API key）+ 多供应商 + 不同模型同时请求
# ===========================================================================

set -e

GATEWAY_URL="http://localhost:8080"
API_KEY="test-api-key-12345"

# 清空数据
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
DELETE FROM request_logs WHERE ts > now() - interval '5 minutes';
DELETE FROM request_wal WHERE created_at > now() - interval '5 minutes';
" > /dev/null 2>&1

echo "=========================================="
echo "🔥 阶段 1.2: 高并发测试"
echo "=========================================="

echo ""
echo "📋 测试 1: 50 个并发请求（单模型 minimax-m2.7）"
echo "----------------------------------------"
START=$(date +%s%N)
for i in {1..50}; do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST $GATEWAY_URL/v1/chat/completions \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"minimax-m2.7","messages":[{"role":"user","content":"test '$i'"}],"max_tokens":5}' &
done
wait
END=$(date +%s%N)
ELAPSED=$(( (END - START) / 1000000 ))
echo "✅ 50 个并发请求完成，耗时: ${ELAPSED}ms"

echo ""
echo "📋 测试 2: 100 个并发请求（混合 4 个模型）"
echo "----------------------------------------"
START=$(date +%s%N)
MODELS=("gpt-4" "minimax-m2.7" "minimax-m3" "claude-3-5-sonnet")
for i in {1..100}; do
  MODEL=${MODELS[$((i % 4))]}
  curl -s -o /dev/null -w "%{http_code}\n" -X POST $GATEWAY_URL/v1/chat/completions \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"'$MODEL'","messages":[{"role":"user","content":"hi"}],"max_tokens":5}' &
done
wait
END=$(date +%s%N)
ELAPSED=$(( (END - START) / 1000000 ))
echo "✅ 100 个并发请求（4 个模型混合）完成，耗时: ${ELAPSED}ms"

echo ""
echo "📋 测试 3: 200 个并发请求（高压力）"
echo "----------------------------------------"
START=$(date +%s%N)
for i in {1..200}; do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST $GATEWAY_URL/v1/chat/completions \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"minimax-m2.7","messages":[{"role":"user","content":"hi"}],"max_tokens":5}' &
  # 限速防止连接爆炸
  if (( i % 50 == 0 )); then
    sleep 0.1
  fi
done
wait
END=$(date +%s%N)
ELAPSED=$(( (END - START) / 1000000 ))
echo "✅ 200 个并发请求完成，耗时: ${ELAPSED}ms"

echo ""
echo "📋 测试 4: 流式请求并发（10 个 SSE 并发）"
echo "----------------------------------------"
START=$(date +%s%N)
for i in {1..10}; do
  timeout 2 curl -s -o /dev/null -N -X POST $GATEWAY_URL/v1/chat/completions \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"model":"minimax-m2.7","stream":true,"messages":[{"role":"user","content":"stream '$i'"}],"max_tokens":5}' &
done
wait
END=$(date +%s%N)
ELAPSED=$(( (END - START) / 1000000 ))
echo "✅ 10 个 SSE 并发请求完成，耗时: ${ELAPSED}ms"

echo ""
echo "=========================================="
echo "⏳ 等待异步 flush（10 秒）"
echo "=========================================="
sleep 10

echo ""
echo "📊 数据库验证"
echo "=========================================="

echo ""
echo "🔍 request_logs 总数（最近 5 分钟）:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT count(*) AS total_logs FROM request_logs WHERE ts > now() - interval '5 minutes';
" 2>&1

echo ""
echo "🔍 各模型请求数（最近 5 分钟）:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT client_model, request_status, count(*) AS count
FROM request_logs
WHERE ts > now() - interval '5 minutes'
GROUP BY client_model, request_status
ORDER BY client_model;
" 2>&1

echo ""
echo "🔍 错误分布:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT error_kind, count(*) AS count
FROM request_logs
WHERE ts > now() - interval '5 minutes' AND success = false
GROUP BY error_kind
ORDER BY count DESC;
" 2>&1

echo ""
echo "🔍 延迟分布（ms）:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT
    count(*) AS total,
    min(latency_ms) AS min_lat,
    max(latency_ms) AS max_lat,
    round(avg(latency_ms)::numeric, 2) AS avg_lat
FROM request_logs
WHERE ts > now() - interval '5 minutes';
" 2>&1

echo ""
echo "🔍 唯一 request_id 数（验证无重复）:"
PGPASSWORD= psql -U xutaohuang -h localhost -d llm_gateway -c "
SELECT
    count(*) AS total_rows,
    count(DISTINCT request_id) AS unique_requests
FROM request_logs
WHERE ts > now() - interval '5 minutes';
" 2>&1

echo ""
echo "✅ 高并发测试完成"