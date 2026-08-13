#!/bin/bash
# 实时监控 db_empty 错误的脚本
# 创建时间: 2026-08-13
# 用途: 实时监控 journalctl 日志，检测 db_empty 并发送告警

set -euo pipefail

LOG_FILE="/var/log/llm-gateway-db-empty.log"
ALERT_COOLDOWN=300  # 5 分钟冷却期，避免告警风暴

last_alert_time=0

echo "[$(date)] 开始监控 db_empty 错误..."

# 实时跟踪日志
journalctl -u llm-gateway-go -f --no-pager | while read -r line; do
  if echo "$line" | grep -q "db_empty"; then
    # 提取关键信息
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    model=$(echo "$line" | jq -r '.model' 2>/dev/null || echo "unknown")
    plan_count=$(echo "$line" | jq -r '.plan_count' 2>/dev/null || echo "0")
    
    # 记录到日志文件
    echo "[$timestamp] ⚠️  DB_EMPTY detected: model=$model, plan_count=$plan_count" | tee -a "$LOG_FILE"
    
    # 检查冷却期
    current_time=$(date +%s)
    time_since_last_alert=$((current_time - last_alert_time))
    
    if [ $time_since_last_alert -gt $ALERT_COOLDOWN ]; then
      # 发送告警（可配置多种告警渠道）
      
      # 1. 记录到系统日志
      logger -t llm-gateway-monitor "CRITICAL: db_empty detected for model=$model"
      
      # 2. 发送飞书告警（如果配置了 webhook）
      if [ -n "${FEISHU_WEBHOOK_URL:-}" ]; then
        curl -X POST "$FEISHU_WEBHOOK_URL" \
          -H 'Content-Type: application/json' \
          -d "{\"msg_type\":\"text\",\"content\":{\"text\":\"[LLM Gateway Alert] DB Empty detected\\nModel: $model\\nTime: $timestamp\\nPlan Count: $plan_count\"}}" \
          2>/dev/null || echo "Failed to send Feishu alert"
      fi
      
      # 3. 发送邮件告警（如果配置了 mail 命令）
      if command -v mail &> /dev/null && [ -n "${ALERT_EMAIL:-}" ]; then
        echo "DB Empty detected for model=$model at $timestamp. Plan count: $plan_count" | \
          mail -s "[LLM Gateway] DB Empty Alert" "$ALERT_EMAIL"
      fi
      
      last_alert_time=$current_time
    else
      echo "  (告警冷却中，距上次告警 ${time_since_last_alert}s / ${ALERT_COOLDOWN}s)"
    fi
  fi
done
