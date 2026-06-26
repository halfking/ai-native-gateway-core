#!/bin/bash
# 测试 minimax-m3 的并发路由

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-test-api-key-12345}"
CONCURRENT="${CONCURRENT:-10}"

echo "========================================="
echo "🔀 minimax-m3 并发路由测试"
echo "========================================="
echo "Gateway: $GATEWAY_URL"
echo "并发数: $CONCURRENT"
echo ""

# 先获取路由解析，看看有哪些候选
echo "1️⃣  查看 minimax-m3 的候选凭据..."
curl -s "$GATEWAY_URL/api/routing/resolve?model=minimax-m3" \
  -H "Authorization: Bearer $API_KEY" | jq .

echo ""
echo "2️⃣  发送 $CONCURRENT 个并发请求..."
START=$(date +%s)

for i in $(seq 1 $CONCURRENT); do
  (
    RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$GATEWAY_URL/v1/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer $API_KEY" \
      -d "{\"model\":\"minimax-m3\",\"messages\":[{\"role\":\"user\",\"content\":\"路由测试 $i\"}],\"max_tokens\":20}")
    
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | head -n -1)
    
    if [ "$HTTP_CODE" = "200" ]; then
      echo "✅ 请求 $i: 成功"
    else
      echo "❌ 请求 $i: HTTP $HTTP_CODE - $(echo "$BODY" | jq -r '.error.message // .error.type // "unknown"' 2>/dev/null)"
    fi
  ) &
done

wait
END=$(date +%s)
echo ""
echo "⏱️  总耗时: $((END - START)) 秒"

echo ""
echo "3️⃣  查看实际路由分布..."
curl -s "$GATEWAY_URL/api/routing/resolve?model=minimax-m3" \
  -H "Authorization: Bearer $API_KEY" | jq .

echo ""
echo "4️⃣  检查各凭据的成功率..."
psql "$LLM_GATEWAY_DATABASE_URL" -c "
SELECT 
    c.id as credential_id,
    p.display_name as provider,
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE rl.success = TRUE) as success,
    ROUND(COUNT(*) FILTER (WHERE rl.success = TRUE)::numeric / COUNT(*)::numeric * 100, 1) as success_rate
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE rl.model = 'minimax-m3'
AND rl.ts > NOW() - INTERVAL '10 minutes'
GROUP BY c.id, p.display_name
ORDER BY c.id;"

echo ""
echo "========================================="
echo "✅ 测试完成"
echo "========================================="
