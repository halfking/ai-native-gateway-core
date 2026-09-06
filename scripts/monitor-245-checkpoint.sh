#!/usr/bin/env bash
# 245 环境监控检查脚本
# 用途: 每 12 小时执行一次，监控 SSE 验证层运行状态
set -euo pipefail

CHECKPOINT_NUM=${1:-unknown}
[[ "$CHECKPOINT_NUM" =~ ^[A-Za-z0-9._-]+$ ]] || {
  echo "checkpoint 标识只能包含字母、数字、点、下划线和连字符" >&2
  exit 2
}

: "${LLM_GATEWAY_245_DB_PASSWORD:?LLM_GATEWAY_245_DB_PASSWORD must be set for database checks}"

SSH_HOST="${LLM_GATEWAY_245_SSH_HOST:-root@8.136.114.245}"
SSH_PORT="${LLM_GATEWAY_245_SSH_PORT:-25022}"
DB_HOST="${LLM_GATEWAY_245_DB_HOST:-172.16.2.210}"
DB_PORT="${LLM_GATEWAY_245_DB_PORT:-5432}"
DB_USER="${LLM_GATEWAY_245_DB_USER:-llm_gateway}"
DB_NAME="${LLM_GATEWAY_245_DB_NAME:-llm_gateway}"
SERVICE_NAME="${LLM_GATEWAY_245_SERVICE_NAME:-llmgo-245.service}"
REPORT_DIR="${LLM_GATEWAY_245_REPORT_DIR:-$PWD/.handoff}"
TIMESTAMP=$(date +"%Y-%m-%d %H:%M:%S")
REPORT_FILE="${REPORT_DIR}/monitoring-checkpoint-${CHECKPOINT_NUM}.md"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -p "$SSH_PORT")
if [[ -n "${LLM_GATEWAY_245_SSH_IDENTITY_FILE:-}" ]]; then
  SSH_OPTS+=(-i "$LLM_GATEWAY_245_SSH_IDENTITY_FILE")
fi

remote_ssh() {
  ssh "${SSH_OPTS[@]}" "$SSH_HOST" "$@"
}

remote_psql() {
  local query=$1
  printf '%s\n%s\n' "$LLM_GATEWAY_245_DB_PASSWORD" "$query" |
    ssh "${SSH_OPTS[@]}" "$SSH_HOST" '
      IFS= read -r PGPASSWORD
      IFS= read -r QUERY
      export PGPASSWORD
      exec psql -X -v ON_ERROR_STOP=1 -h "'"$DB_HOST"'" -p "'"$DB_PORT"'" -U "'"$DB_USER"'" -d "'"$DB_NAME"'" -t -A -c "$QUERY"
    '
}

echo "========================================="
echo "245 环境监控检查 - Checkpoint #${CHECKPOINT_NUM}"
echo "时间: ${TIMESTAMP}"
echo "========================================="

echo ""
echo "1. 检查服务状态..."
SERVICE_STATUS=$(remote_ssh systemctl is-active "$SERVICE_NAME" || echo inactive)
SERVICE_UPTIME=$(remote_ssh "systemctl status '$SERVICE_NAME' --no-pager | grep 'Active:' | awk '{print \$3, \$4, \$5}'" || true)
echo "   状态: ${SERVICE_STATUS}"
echo "   运行时长: ${SERVICE_UPTIME:-N/A}"

echo ""
echo "2. 查询请求统计（最近 12 小时）..."
REQUEST_STATS=$(remote_psql "SELECT COUNT(*) AS total, COUNT(*) FILTER (WHERE success = true) AS success, COUNT(*) FILTER (WHERE success = false) AS failed, COALESCE(ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / NULLIF(COUNT(*), 0), 2), 0) AS rate FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '12 hours';")
echo "   ${REQUEST_STATS}"

echo ""
echo "3. 检查 malformed_sse_frame 错误..."
MALFORMED_COUNT=$(remote_psql "SELECT COUNT(*) FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '12 hours' AND error_kind = 'malformed_sse_frame';")
echo "   malformed_sse_frame: ${MALFORMED_COUNT}"

echo ""
echo "4. 检查失败错误 Top 5..."
remote_psql "SELECT error_kind, COUNT(*) AS count FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '12 hours' AND success = false GROUP BY error_kind ORDER BY count DESC LIMIT 5;"

echo ""
echo "5. 检查 MiniMax 模型调用..."
remote_psql "SELECT COALESCE(outbound_model, client_model) AS model, COUNT(*) AS requests, COUNT(*) FILTER (WHERE success = true) AS success FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '12 hours' AND (outbound_model LIKE '%minimax%' OR client_model LIKE '%minimax%') GROUP BY COALESCE(outbound_model, client_model) ORDER BY requests DESC;"

echo ""
echo "6. 内存使用情况..."
MEMORY_INFO=$(remote_ssh "systemctl status '$SERVICE_NAME' --no-pager | grep 'Memory:'" || true)
echo "   ${MEMORY_INFO:-N/A}"

echo ""
echo "========================================="
echo "监控检查完成 - Checkpoint #${CHECKPOINT_NUM}"
echo "========================================="

if [[ "$MALFORMED_COUNT" =~ ^[0-9]+$ ]] && (( MALFORMED_COUNT == 0 )); then
  echo "✅ 状态: 健康 (0 个 malformed 错误)"
else
  echo "⚠️  警告: malformed 检查结果为 ${MALFORMED_COUNT}"
fi

echo "报告路径预留: ${REPORT_FILE}"
