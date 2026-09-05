#!/bin/bash
# 245 环境监控检查脚本
# 用途: 每 12 小时执行一次,监控 SSE 验证层运行状态

set -e

CHECKPOINT_NUM=${1:-"unknown"}
REPORT_DIR="/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go/.handoff"
TIMESTAMP=$(date +"%Y-%m-%d %H:%M:%S")
REPORT_FILE="${REPORT_DIR}/2026-08-29-monitoring-checkpoint-${CHECKPOINT_NUM}.md"

echo "========================================="
echo "245 环境监控检查 - Checkpoint #${CHECKPOINT_NUM}"
echo "时间: ${TIMESTAMP}"
echo "========================================="

# 1. 检查服务状态
echo ""
echo "1. 检查服务状态..."
SERVICE_STATUS=$(ssh -p 25022 root@8.136.114.245 "systemctl is-active llmgo-245.service" || echo "inactive")
SERVICE_UPTIME=$(ssh -p 25022 root@8.136.114.245 "systemctl status llmgo-245.service --no-pager | grep 'Active:' | awk '{print \$3, \$4, \$5}'")
echo "   状态: ${SERVICE_STATUS}"
echo "   运行时长: ${SERVICE_UPTIME}"

# 2. 查询请求统计
echo ""
echo "2. 查询请求统计（最近 12 小时）..."
REQUEST_STATS=$(ssh -p 25022 root@8.136.114.245 'PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -t -c "SELECT COUNT(*) AS total, COUNT(*) FILTER (WHERE success = true) AS success, COUNT(*) FILTER (WHERE success = false) AS failed, ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS rate FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '\''12 hours'\'';"')
echo "   ${REQUEST_STATS}"

# 3. 检查 malformed 错误
echo ""
echo "3. 检查 malformed_sse_frame 错误..."
MALFORMED_COUNT=$(ssh -p 25022 root@8.136.114.245 "journalctl -u llmgo-245.service --since '12 hours ago' --no-pager | grep -i 'malformed' | wc -l")
echo "   Malformed 日志数量: ${MALFORMED_COUNT}"

# 4. 检查错误分布
echo ""
echo "4. 错误类型分布（Top 5）..."
ssh -p 25022 root@8.136.114.245 'PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c "SELECT error_kind, COUNT(*) AS count FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '\''12 hours'\'' AND success = false GROUP BY error_kind ORDER BY count DESC LIMIT 5;"'

# 5. MiniMax 模型统计
echo ""
echo "5. MiniMax 模型请求统计..."
ssh -p 25022 root@8.136.114.245 'PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg" psql -h 172.16.2.210 -p 5432 -U llm_gateway -d llm_gateway -c "SELECT COALESCE(outbound_model, client_model) AS model, COUNT(*) AS requests, COUNT(*) FILTER (WHERE success = true) AS success FROM request_logs_hot WHERE ts >= NOW() - INTERVAL '\''12 hours'\'' AND (outbound_model LIKE '\''%minimax%'\'' OR client_model LIKE '\''%minimax%'\'') GROUP BY COALESCE(outbound_model, client_model) ORDER BY requests DESC;"'

# 6. 内存使用
echo ""
echo "6. 内存使用情况..."
MEMORY_INFO=$(ssh -p 25022 root@8.136.114.245 "systemctl status llmgo-245.service --no-pager | grep 'Memory:'" || echo "N/A")
echo "   ${MEMORY_INFO}"

echo ""
echo "========================================="
echo "监控检查完成 - Checkpoint #${CHECKPOINT_NUM}"
echo "========================================="

# 评估结果
if [ "${MALFORMED_COUNT}" -eq 0 ]; then
    echo "✅ 状态: 健康 (0 个 malformed 错误)"
else
    echo "⚠️  警告: 发现 ${MALFORMED_COUNT} 个 malformed 错误"
fi

echo ""
echo "详细报告将保存到: ${REPORT_FILE}"
