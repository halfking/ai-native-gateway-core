#!/bin/bash
# check_claude_opus_logs.sh - 检查 Claude Opus 工具调用问题的日志

echo "=========================================="
echo "Claude Opus Tool Calling Log Checker"
echo "=========================================="
echo ""

# 检查最近的 Claude Opus 请求
echo "=== 最近的 Claude Opus 请求 ==="
echo "SQL: Querying recent claude-opus requests..."
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT 
    request_id,
    client_model,
    provider_id,
    success,
    error_code,
    error_message,
    latency_ms,
    created_at,
    jsonb_array_length(request_body::jsonb->'tools') as has_tools,
    jsonb_array_length(request_body::jsonb->'messages') as msg_count
FROM request_logs
WHERE client_model LIKE '%opus%'
AND created_at > NOW() - INTERVAL '2 hours'
ORDER BY created_at DESC
LIMIT 20;
\"" 2>&1

echo ""
echo "=== 查找工具调用失败的请求 ==="
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT 
    request_id,
    client_model,
    success,
    error_code,
    error_message,
    request_body::jsonb->'messages'->-1->>'role' as last_role,
    LENGTH(request_body::jsonb->'messages'->-1->>'content') as last_content_len
FROM request_logs
WHERE client_model LIKE '%opus%'
AND request_body::jsonb->'tools' IS NOT NULL
AND created_at > NOW() - INTERVAL '2 hours'
ORDER BY created_at DESC
LIMIT 10;
\"" 2>&1

echo ""
echo "=== 查找包含 tool role 的消息 ==="
ssh llm-252 "psql -U postgres -d llm_gateway -c \"
SELECT 
    request_id,
    client_model,
    success,
    error_code,
    error_message,
    created_at
FROM request_logs
WHERE client_model LIKE '%opus%'
AND request_body::text LIKE '%\"role\":\"tool\"%'
AND created_at > NOW() - INTERVAL '2 hours'
ORDER BY created_at DESC
LIMIT 10;
\"" 2>&1

echo ""
echo "=== 检查网关日志中的 Claude Opus 错误 ==="
echo "Checking gateway logs on server 154..."

ssh 154 "sudo journalctl -u llm-gateway --since '2 hours ago' --no-pager | grep -i 'opus' | grep -iE 'error|warn|tool|fail' | tail -50" 2>&1 || echo "Cannot access server 154"

echo ""
echo "=== 检查 Anthropic bridge 相关日志 ==="
ssh 154 "sudo journalctl -u llm-gateway --since '2 hours ago' --no-pager | grep -E 'anthropic.*bridge|tool_use|tool_result|converting_tool' | tail -50" 2>&1 || echo "Cannot access server 154"

echo ""
echo "=========================================="
echo "Log check complete"
echo "=========================================="
