#!/bin/bash
# 监控 request_logs 表中 request_body/response_body 的实际大小分布
# 用于评估 512KB 限制是否合理
#
# 使用: ./monitor-body-size.sh [hours]
# 默认查看最近 24 小时

set -e

HOURS=${1:-24}
DB_HOST="154"
DB_USER="llmgateway"
DB_NAME="llmgateway"

echo "=========================================="
echo "Body Size 监控 (最近 ${HOURS} 小时)"
echo "=========================================="
echo ""

# 1. RequestBody 大小分布
echo "1. RequestBody 大小分布 (单位: KB):"
echo "-------------------------------------------"
PGPASSWORD=llmgateway psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "
SELECT
    CASE
        WHEN octet_length(request_body) < 64*1024 THEN '0-64KB'
        WHEN octet_length(request_body) < 128*1024 THEN '64-128KB'
        WHEN octet_length(request_body) < 256*1024 THEN '128-256KB'
        WHEN octet_length(request_body) < 512*1024 THEN '256-512KB'
        WHEN octet_length(request_body) < 1024*1024 THEN '512KB-1MB'
        ELSE '> 1MB'
    END AS size_range,
    COUNT(*) AS count,
    ROUND(AVG(octet_length(request_body))/1024.0, 1) AS avg_kb,
    MAX(octet_length(request_body))/1024 AS max_kb
FROM request_logs
WHERE request_body IS NOT NULL
  AND created_at > now() - interval '${HOURS} hours'
GROUP BY size_range
ORDER BY MIN(octet_length(request_body));
" 2>/dev/null

echo ""

# 2. ResponseBody 大小分布
echo "2. ResponseBody 大小分布 (单位: KB):"
echo "-------------------------------------------"
PGPASSWORD=llmgateway psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "
SELECT
    CASE
        WHEN octet_length(response_body) < 64*1024 THEN '0-64KB'
        WHEN octet_length(response_body) < 128*1024 THEN '64-128KB'
        WHEN octet_length(response_body) < 256*1024 THEN '128-256KB'
        WHEN octet_length(response_body) < 512*1024 THEN '256-512KB'
        WHEN octet_length(response_body) < 1024*1024 THEN '512KB-1MB'
        ELSE '> 1MB'
    END AS size_range,
    COUNT(*) AS count,
    ROUND(AVG(octet_length(response_body))/1024.0, 1) AS avg_kb,
    MAX(octet_length(response_body))/1024 AS max_kb
FROM request_logs
WHERE response_body IS NOT NULL
  AND created_at > now() - interval '${HOURS} hours'
GROUP BY size_range
ORDER BY MIN(octet_length(response_body));
" 2>/dev/null

echo ""

# 3. 截断的请求体统计 (新 512KB 限制生效后)
echo "3. 截断的请求体统计 (含 truncated 标记):"
echo "-------------------------------------------"
TRUNCATED=$(PGPASSWORD=llmgateway psql -h $DB_HOST -U $DB_USER -d $DB_NAME -t -c "
SELECT COUNT(*)
FROM request_logs
WHERE request_body LIKE '%...[truncated%'
  AND created_at > now() - interval '${HOURS} hours';
" 2>/dev/null | tr -d ' ')

TOTAL=$(PGPASSWORD=llmgateway psql -h $DB_HOST -U $DB_USER -d $DB_NAME -t -c "
SELECT COUNT(*)
FROM request_logs
WHERE request_body IS NOT NULL
  AND created_at > now() - interval '${HOURS} hours';
" 2>/dev/null | tr -d ' ')

if [ -z "$TRUNCATED" ] || [ -z "$TOTAL" ]; then
    echo "⚠ 无法连接数据库"
else
    PERCENT=$(echo "scale=2; $TRUNCATED * 100 / $TOTAL" | bc)
    echo "总请求数: $TOTAL"
    echo "截断数: $TRUNCATED ($PERCENT%)"
    if [ "$TRUNCATED" -gt 0 ]; then
        echo ""
        echo "⚠ 有请求被截断，详细信息:"
        PGPASSWORD=llmgateway psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "
        SELECT
            request_id,
            octet_length(request_body)/1024 AS body_kb,
            SUBSTRING(request_body FROM 'original=\\d+bytes') AS marker
        FROM request_logs
        WHERE request_body LIKE '%...[truncated%'
          AND created_at > now() - interval '${HOURS} hours'
        ORDER BY created_at DESC
        LIMIT 5;
        " 2>/dev/null
    fi
fi

echo ""
echo "=========================================="
echo "建议"
echo "=========================================="
echo "- 如果截断率 > 5%: 考虑将限制提升到 1MB"
echo "- 如果 > 1MB 请求很多: 实现动态配置或外部存储"
echo "- 如果 ResponseBody 平均 > 100KB: 需要考虑同样限制"