#!/usr/bin/env bash
# audit-incomplete-tool-calls.sh — 检查 request_logs_hot 中不完整的 tool_use/tool_result
#
# 用法:
#   bash scripts/audit-incomplete-tool-calls.sh [--server 252|245|154]
#
# 检查项:
#   1. tool_use 块存在但缺少对应的 tool_result
#   2. tool_result 存在但缺少对应的 tool_use
#   3. 流被中断时的 tool call 状态

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# 加载环境变量
TARGET_SERVER="${1:-245}"
if [[ "$TARGET_SERVER" == "--server" ]]; then
    TARGET_SERVER="${2:-245}"
fi

case "$TARGET_SERVER" in
    252)
        DB_HOST="${LLM_GATEWAY_252_HOST:-172.16.2.210}"
        DB_PORT="5432"
        ;;
    245)
        DB_HOST="8.136.114.245"
        DB_PORT="5432"
        ;;
    154)
        DB_HOST="${LLM_GATEWAY_154_HOST:-47.97.111.154}"
        DB_PORT="5432"
        ;;
    *)
        echo "错误: 不支持的服务器 $TARGET_SERVER (仅 252|245|154)" >&2
        exit 1
        ;;
esac

DB_NAME="llm_gateway"
DB_USER="${PG_LLM_GATEWAY_USER:-llm_gateway}"
DB_PASS="${PG_LLM_GATEWAY_PASS}"

if [[ -z "${DB_PASS:-}" ]]; then
    echo "错误: 缺少 PG_LLM_GATEWAY_PASS 环境变量" >&2
    exit 1
fi

export PGPASSWORD="$DB_PASS"

echo "=== 检查不完整的 tool calls ($TARGET_SERVER) ==="
echo ""

# 检查最近 24 小时内包含 tool_use 的请求
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 <<'SQL'
-- 1. 检查有 tool_use 但缺少 tool_result 的请求
WITH tool_use_requests AS (
    SELECT 
        request_id,
        response_body,
        interrupted,
        reason,
        created_at
    FROM request_logs_hot
    WHERE created_at >= NOW() - INTERVAL '24 hours'
      AND response_body::text LIKE '%"type":"tool_use"%'
      AND response_body IS NOT NULL
),
incomplete_tools AS (
    SELECT 
        request_id,
        interrupted,
        reason,
        created_at,
        (response_body::jsonb->'content')::text AS content_json
    FROM tool_use_requests
    WHERE (response_body::jsonb->'content')::text LIKE '%"type":"tool_use"%'
      AND (response_body::jsonb->'content')::text NOT LIKE '%"type":"tool_result"%'
)
SELECT 
    COUNT(*) AS incomplete_tool_call_count,
    COUNT(*) FILTER (WHERE interrupted = true) AS interrupted_count,
    COUNT(*) FILTER (WHERE interrupted = false) AS completed_count
FROM incomplete_tools;

-- 2. 按中断原因分组统计
WITH tool_use_requests AS (
    SELECT 
        request_id,
        response_body,
        interrupted,
        reason,
        created_at
    FROM request_logs_hot
    WHERE created_at >= NOW() - INTERVAL '24 hours'
      AND response_body::text LIKE '%"type":"tool_use"%'
      AND response_body IS NOT NULL
),
incomplete_tools AS (
    SELECT 
        request_id,
        interrupted,
        reason,
        created_at
    FROM tool_use_requests
    WHERE (response_body::jsonb->'content')::text LIKE '%"type":"tool_use"%'
      AND (response_body::jsonb->'content')::text NOT LIKE '%"type":"tool_result"%'
      AND interrupted = true
)
SELECT 
    reason,
    COUNT(*) AS count
FROM incomplete_tools
GROUP BY reason
ORDER BY count DESC
LIMIT 10;

-- 3. 最近 10 个不完整的 tool call 请求
WITH tool_use_requests AS (
    SELECT 
        request_id,
        response_body,
        interrupted,
        reason,
        created_at,
        client_model
    FROM request_logs_hot
    WHERE created_at >= NOW() - INTERVAL '24 hours'
      AND response_body::text LIKE '%"type":"tool_use"%'
      AND response_body IS NOT NULL
),
incomplete_tools AS (
    SELECT 
        request_id,
        interrupted,
        reason,
        created_at,
        client_model
    FROM tool_use_requests
    WHERE (response_body::jsonb->'content')::text LIKE '%"type":"tool_use"%'
      AND (response_body::jsonb->'content')::text NOT LIKE '%"type":"tool_result"%'
)
SELECT 
    request_id,
    interrupted,
    reason,
    client_model,
    created_at
FROM incomplete_tools
ORDER BY created_at DESC
LIMIT 10;
SQL

echo ""
echo "=== 审计完成 ==="
