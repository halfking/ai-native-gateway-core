#!/usr/bin/env bash
# apihub 22P02 修复后持续监控脚本（1 小时）
# 用途：每分钟检查 245 stderr 日志 + 主动探测 apihub sync

set -euo pipefail

SSH_HOST="root@8.136.114.245"
SSH_KEY="$HOME/.ssh/id_ed25519"
SSH_PORT="25022"
LOG_FILE="/var/log/llm-gateway-go/gateway.stderr.log"
DURATION_MIN=60
CHECK_INTERVAL=60  # 秒

START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION_MIN * 60))

echo "=== apihub 健康监控启动 ==="
echo "开始时间: $(date)"
echo "监控时长: ${DURATION_MIN} 分钟"
echo "检查间隔: ${CHECK_INTERVAL} 秒"
echo ""

ITERATION=0

while [ $(date +%s) -lt $END_TIME ]; do
    ITERATION=$((ITERATION + 1))
    CURRENT_TIME=$(date "+%Y-%m-%d %H:%M:%S")
    
    echo "[$ITERATION] [$CURRENT_TIME] 检查中..."
    
    # 1. 检查最近 2 分钟的 22P02 错误
    ERROR_COUNT=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_HOST" \
        "tail -500 $LOG_FILE | grep -c '22P02' || true")
    
    # 2. 检查最近 2 分钟的 apihub watcher sync complete
    SYNC_LOG=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_HOST" \
        "tail -200 $LOG_FILE | grep 'apihub watcher: sync complete' | tail -1 || echo 'NO_SYNC'")
    
    # 3. 检查 register failed 日志
    FAIL_COUNT=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_HOST" \
        "tail -500 $LOG_FILE | grep -c 'apihub watcher: register LLM asset failed' || true")
    
    # 4. 主动触发一次 sync（通过 admin API）
    PROBE_RESULT=$(ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_HOST" \
        "curl -s -X POST http://localhost:8781/api/admin/apihub/sync -H 'X-Admin-Token: \$(grep ADMIN_SECRET /opt/llm-gateway-go/.env | cut -d= -f2)' || echo '{\"error\":\"probe_failed\"}'")
    
    echo "  22P02 错误数: $ERROR_COUNT"
    echo "  register 失败数: $FAIL_COUNT"
    echo "  最近 sync: $SYNC_LOG"
    echo "  主动探测: $PROBE_RESULT"
    
    if [ "$ERROR_COUNT" -gt 0 ]; then
        echo "  ⚠️  检测到 22P02 错误！"
    fi
    
    if [ "$FAIL_COUNT" -gt 0 ]; then
        echo "  ⚠️  检测到 register 失败！"
    fi
    
    echo ""
    
    # 等待下次检查
    REMAINING=$((END_TIME - $(date +%s)))
    if [ $REMAINING -lt $CHECK_INTERVAL ]; then
        sleep $REMAINING
        break
    else
        sleep $CHECK_INTERVAL
    fi
done

echo "=== 监控结束 ==="
echo "结束时间: $(date)"
echo ""

# 最终汇总
echo "=== 最终汇总（最近 1 小时） ==="
ssh -i "$SSH_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no "$SSH_HOST" <<'EOSSH'
echo "22P02 总数:"
grep -c '22P02' /var/log/llm-gateway-go/gateway.stderr.log || echo "0"

echo ""
echo "register LLM asset failed 总数:"
grep -c 'apihub watcher: register LLM asset failed' /var/log/llm-gateway-go/gateway.stderr.log || echo "0"

echo ""
echo "最近 10 次 sync complete:"
grep 'apihub watcher: sync complete' /var/log/llm-gateway-go/gateway.stderr.log | tail -10

echo ""
echo "apihub watcher 启动时间:"
grep 'apihub watcher initialized' /var/log/llm-gateway-go/gateway.stderr.log | tail -1
EOSSH
