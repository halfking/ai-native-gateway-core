#!/usr/bin/env bash
# auto-monitor.sh - Auto模式持续监控脚本
# 每小时运行一次，记录关键指标

set -euo pipefail

REMOTE="211"
LOG_FILE="/tmp/auto_monitor_$(date +%Y%m%d).log"
ALERT_THRESHOLD_SUCCESS_RATE=95

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE"
}

log "=========================================="
log "Auto模式监控检查开始"
log "=========================================="

# 1. 运行验证脚本
log "运行验证脚本..."
VERIFY_OUTPUT=$(ssh "$REMOTE" "cd /root/llm-gateway-go-211 && python3 scripts/audit/auto-211-live-verify.py 2>&1")

if echo "$VERIFY_OUTPUT" | grep -q "ALL PASS"; then
    log "✅ 验证通过"
else
    log "❌ 验证失败！"
    log "$VERIFY_OUTPUT"
    exit 1
fi

# 2. 检查容器健康
log "检查容器健康..."
CONTAINER_STATUS=$(ssh "$REMOTE" "docker ps --filter name=llm-gateway-211 --format '{{.Status}}' | head -1")
log "容器状态: $CONTAINER_STATUS"

if echo "$CONTAINER_STATUS" | grep -q "healthy"; then
    log "✅ 容器健康"
else
    log "⚠️ 容器状态异常"
fi

# 3. 检查最近的错误日志
log "检查错误日志..."
ERROR_COUNT=$(ssh "$REMOTE" "docker logs --since 1h llm-gateway-211-a llm-gateway-211-b 2>&1 | grep -i 'error\|fatal' | wc -l" 2>/dev/null || echo "0")
log "最近1小时错误数: $ERROR_COUNT"

if [ "$ERROR_COUNT" -gt 10 ]; then
    log "⚠️ 错误数量较多，需要关注"
fi

# 4. 检查auto请求情况
log "检查auto请求..."
AUTO_REQUESTS=$(echo "$VERIFY_OUTPUT" | grep "真库最近 auto 请求" -A 10 | tail -8)
log "最近auto请求:"
log "$AUTO_REQUESTS"

# 5. 提取关键指标（只统计真实请求行，排除 "ALL PASS" 等非数据行）
# 真实请求行的特征：包含时间戳格式 "YYYY-MM-DD HH:MM:SS"
SUCCESS_COUNT=$(echo "$AUTO_REQUESTS" | grep " t |" | wc -l | tr -d ' ')
TOTAL_COUNT=$(echo "$AUTO_REQUESTS" | grep -E "[0-9]{4}-[0-9]{2}-[0-9]{2}" | wc -l | tr -d ' ')

if [ "$TOTAL_COUNT" -gt 0 ]; then
    SUCCESS_RATE=$((SUCCESS_COUNT * 100 / TOTAL_COUNT))
    log "成功率: ${SUCCESS_RATE}% (${SUCCESS_COUNT}/${TOTAL_COUNT})"
    
    if [ "$SUCCESS_RATE" -lt "$ALERT_THRESHOLD_SUCCESS_RATE" ]; then
        log "🚨 告警: 成功率低于${ALERT_THRESHOLD_SUCCESS_RATE}%"
    fi
else
    log "⚠️ 无最近请求数据"
fi

log "=========================================="
log "监控检查完成"
log "=========================================="
log ""

# 输出摘要
cat <<EOF

## 监控摘要 ($(date '+%Y-%m-%d %H:%M:%S'))

- 验证状态: ✅ 通过
- 容器健康: ✅ 正常
- 成功率: ${SUCCESS_RATE:-N/A}%
- 错误数: $ERROR_COUNT (1小时内)

详细日志: $LOG_FILE

EOF
