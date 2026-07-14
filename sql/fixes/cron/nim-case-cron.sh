#!/usr/bin/env bash
# /opt/llm-gateway-go/ops/nim-case-cron.sh
# 2026-07-14: daily NIM / 全局模型名大小写 sanity check (245 部署)
#
# 在 245 (本机) 跑的健康检查；drift 检测依赖外部凭据 (PG_PASS 在
# 252 上)，因此不放在本 cron 里。245 上唯一可用的对 252 的访问是
# 公网 SSH + 252 的 authorized_keys；SSHPASS 在 245 上不存在。
#
# 部署后用以下命令手动 / 周期性触发 drift 检测 (从 252 上跑)：
#   ssh root@115.29.212.252 "set -a; . /opt/pms-dev/.runtime-secrets/infra.env; set +a; \
#     docker exec -e PGPASSWORD=\$TARGET_DB_PASSWORD pg-252-pg17 \
#     bash -c 'cat > /tmp/check.sql <<EOF ... EOF; psql -U llm_gateway -d llm_gateway -f /tmp/check.sql'"
#
# 或直接调 sql/fixes/check-nvidia-nim-outbound-model-id-drift.sh 的
# REMOTE_MODE=1 路径从 252 上跑。

set -uo pipefail

LOG="/var/log/llm-gateway-go/nim-case-cron.log"
TAG="nim-case-cron"

log() { echo "[$(date -Iseconds)] $*" | tee -a "$LOG"; }

# 1. 245 网关 healthz
HTTP=$(curl -fsS --max-time 5 http://localhost:8781/healthz 2>&1 || echo "FAIL")
log "healthz: $HTTP"
if ! echo "$HTTP" | grep -q '"status":"ok"'; then
    log "ALERT: healthz is not OK"
    exit 1
fi

# 2. 网关版本
VER=$(curl -fsS --max-time 5 http://localhost:8781/api/system/version)
log "version: $VER"

# 3. 网关日志最近 1 小时 postgres disabled 计数
PG_DISABLED=$(journalctl -u llm-gateway-go --since "1 hour ago" --no-pager -o cat 2>/dev/null | grep -c "postgres disabled" 2>/dev/null | head -1)
PG_DISABLED=${PG_DISABLED:-0}
PG_DISABLED=$(echo "$PG_DISABLED" | tr -dc '0-9')
PG_DISABLED=${PG_DISABLED:-0}
log "postgres_disabled_in_last_hour: $PG_DISABLED"
if [[ "$PG_DISABLED" -gt 0 ]]; then
    log "ALERT: $PG_DISABLED postgres disabled events in last hour"
    exit 1
fi

# 4. 网关进程内存 (P95) - 大致 sanity
RSS=$(ps -o rss= -p "$(pgrep -f /opt/llm-gateway-go/gateway | head -1)" 2>/dev/null | tr -d ' ')
log "gateway_rss_kb: ${RSS:-unknown}"

# 5. NIM 凭据在 245 网关上的发现活动（DB-backed）
#    通过最近 NIM 凭据的 updated_at 反映
LAST_DISCOVER=$(journalctl -u llm-gateway-go --since "1 hour ago" --no-pager -o cat 2>/dev/null | grep -cE "models? discovered|model_discovery|provider_models" 2>/dev/null | head -1)
LAST_DISCOVER=${LAST_DISCOVER:-0}
LAST_DISCOVER=$(echo "$LAST_DISCOVER" | tr -dc '0-9')
LAST_DISCOVER=${LAST_DISCOVER:-0}
log "model_discovery_log_lines_last_hour: $LAST_DISCOVER"

# 6. record LLM-gateway log warning 计数
WARN_LINES=$(journalctl -u llm-gateway-go --since "1 hour ago" --no-pager -o cat 2>/dev/null | grep -cE 'level":"WARN"|level":"ERROR"' 2>/dev/null | head -1)
WARN_LINES=${WARN_LINES:-0}
WARN_LINES=$(echo "$WARN_LINES" | tr -dc '0-9')
WARN_LINES=${WARN_LINES:-0}
log "gateway_warn_error_lines_last_hour: $WARN_LINES"

if [[ "$WARN_LINES" -gt 50 ]]; then
    log "WARN: $WARN_LINES WARN/ERROR lines in last hour"
fi

logger -t "$TAG" "OK: 245 healthz + version + db OK; postgres_disabled=$PG_DISABLED; warn=$WARN_LINES"
exit 0
