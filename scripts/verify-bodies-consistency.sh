#!/usr/bin/env bash
# 验证 request_logs_hot 和 request_logs_bodies_hot 的 ts 一致性
# 用于验证修复后是否还有 ts 不匹配的问题

set -euo pipefail

SERVER="${1:-115.29.212.252}"
PORT="${2:-25022}"

echo "=== 验证 request_logs 和 request_logs_bodies 的 ts 一致性 ==="
echo "服务器: $SERVER:$PORT"
echo ""

# 检查最近 1 小时的记录
echo "1. 检查最近 1 小时的 ts 不一致记录数..."
ssh "root@${SERVER}" -p "$PORT" "docker exec pg-252-pg17 psql -U stockuser -h 172.16.2.210 -d llm_gateway -t -c \"
SELECT COUNT(*) as mismatch_count
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
WHERE rl.ts > NOW() - INTERVAL '1 hour'
  AND (rl.request_body IS NOT NULL OR rl.response_body IS NOT NULL)
  AND rb.request_id IS NULL;
\""

echo ""
echo "2. 检查 minimax 最近 10 条记录的详情..."
ssh "root@${SERVER}" -p "$PORT" "docker exec pg-252-pg17 psql -U stockuser -h 172.16.2.210 -d llm_gateway -c \"
SELECT 
  rl.request_id,
  rl.ts as main_ts,
  rb.ts as body_ts,
  CASE WHEN rl.ts = rb.ts THEN '✓' ELSE '✗' END as ts_match,
  CASE WHEN rb.request_body IS NOT NULL THEN '✓' ELSE '✗' END as has_req_body,
  CASE WHEN rb.response_body IS NOT NULL THEN '✓' ELSE '✗' END as has_resp_body,
  rl.client_model
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
WHERE rl.provider = 'minimax'
  AND rl.ts > NOW() - INTERVAL '30 minutes'
ORDER BY rl.ts DESC
LIMIT 10;
\""

echo ""
echo "3. 统计最近 1 小时各模型的消息完整性..."
ssh "root@${SERVER}" -p "$PORT" "docker exec pg-252-pg17 psql -U stockuser -h 172.16.2.210 -d llm_gateway -c \"
SELECT 
  rl.client_model,
  COUNT(*) as total_requests,
  COUNT(rb.request_id) as with_bodies,
  COUNT(*) - COUNT(rb.request_id) as missing_bodies,
  ROUND(100.0 * COUNT(rb.request_id) / COUNT(*), 2) as coverage_pct
FROM request_logs_hot rl
LEFT JOIN request_logs_bodies_hot rb 
  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
WHERE rl.ts > NOW() - INTERVAL '1 hour'
GROUP BY rl.client_model
ORDER BY total_requests DESC
LIMIT 20;
\""

echo ""
echo "=== 验证完成 ==="
echo "如果 mismatch_count = 0 且 coverage_pct 接近 100%，说明修复成功"
