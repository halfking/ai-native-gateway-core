#!/bin/bash
# scripts/audit-36h.sh — 重放 36h 请求错误审计
#
# 用法（先按 rule 11 §6 注入环境）：
#   env-injector inject aliyun-gateway-154
#   env-injector inject aliyun-edge-252
#   ./scripts/audit-36h.sh
#
# 输出：reports/<date>-36h-audit/ 目录
#   - log-154-36h.txt / log-252-36h.txt：原始日志
#   - 252-db-*.txt：数据库透视结果
#   - ANALYSIS_REPORT.md：综合分析（最新版会在此覆盖）
#
# 假设：
#   - 154 wrapper 变量和 252/PG 变量均由 env-injector export
#   - 当前主机 macOS/Linux + sshpass 可用

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
: "${SSH_WRAPPER_HOP_KEY:?run env-injector inject aliyun-gateway-154 first}"
: "${SSH_WRAPPER_HOP_HOST:?run env-injector inject aliyun-gateway-154 first}"
: "${SSH_WRAPPER_TARGET_IP:?run env-injector inject aliyun-gateway-154 first}"
: "${SSH_WRAPPER_TARGET_HOST:?run env-injector inject aliyun-gateway-154 first}"
if [[ -z "${SSH_WRAPPER_TARGET_KEY:-}" && -z "${SSHPASS_154:-}" && -z "${DEPLOY_SSH_PASS:-}" ]]; then
  echo "run env-injector inject aliyun-gateway-154 or provide an explicit 154 password" >&2
  exit 64
fi

HOST_154="$SSH_WRAPPER_TARGET_HOST"
HOST_252="${HOST_252:-115.29.212.252}"
SSH_PORT_252="${SSH_PORT_252:-25022}"
DB_CONTAINER="${DB_CONTAINER:-pg-252-pg17}"
DB_NAME="${DB_NAME:-llm_gateway}"
DB_USER="${DB_USER:-llm_gateway}"

PATH="$SCRIPT_DIR:$PATH"
SSH_BASE_154="ssh -o ConnectTimeout=8 root@$HOST_154"
SSH_BASE_252="sshpass -e ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -p $SSH_PORT_252 root@$HOST_252"
SCP_BASE_252="sshpass -e scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -P $SSH_PORT_252"

REPORT_DIR="reports/$(date +%Y%m%d)-36h-audit"
mkdir -p "$REPORT_DIR"

echo "[1/5] 154 status ..."
$SSH_BASE_154 'systemctl status llm-gateway-go --no-pager -n 3 2>&1; cat /opt/llm-gateway-go/VERSION 2>/dev/null' > "$REPORT_DIR/154-status.txt"

echo "[2/5] 154 36h log pull ..."
$SSH_BASE_154 'journalctl -u llm-gateway-go --since "36 hours ago" --no-pager 2>&1' > "$REPORT_DIR/log-154-36h.txt"

echo "[3/5] 252 36h log pull ..."
$SSH_BASE_252 'journalctl -u llm-gateway-go --since "36 hours ago" --no-pager > /tmp/log-252-36h.txt 2>&1'
scp -P "$SSH_PORT_252" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    root@$HOST_252:/tmp/log-252-36h.txt "$REPORT_DIR/log-252-36h.txt"

echo "[4/5] 252 DB perspective queries ..."
for q in overview status errkind failstage detailcode bytime model cred; do
  echo "  q-$q ..."
  cat > "/tmp/q-$q.sql" <<EOF
SELECT 'audit window' AS scope, NOW() - INTERVAL '36 hours' AS from_ts, NOW() AS to_ts;
EOF
  case $q in
    overview) cat >> /tmp/q-overview.sql <<'EOF'
SELECT COUNT(*) total, COUNT(*) FILTER (WHERE success) succ,
       COUNT(*) FILTER (WHERE NOT success) fail,
       ROUND(100.0*COUNT(*) FILTER (WHERE success)/NULLIF(COUNT(*),0),2) succ_pct
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours';
SELECT 'hot_size' AS m, COUNT(*), MAX(ts), MIN(ts) FROM request_logs_hot;
SELECT 'partition_2026_07' AS m, COUNT(*), MAX(ts), MIN(ts) FROM request_logs_2026_07;
SELECT 'partition_2026_08' AS m, COUNT(*), MAX(ts), MIN(ts) FROM request_logs_2026_08;
SELECT 'partition_default' AS m, COUNT(*), MAX(ts), MIN(ts) FROM request_logs_default;
SELECT 'handoff_logs' AS m, COUNT(*), MAX(created_at) FROM handoff_logs;
SELECT 'routing_decision_log_hot' AS m, COUNT(*), MAX(ts) FROM routing_decision_log_hot;
EOF
       ;;
    errkind) cat >> /tmp/q-errkind.sql <<'EOF'
SELECT COALESCE(error_kind,'<null>') ek, COUNT(*) c
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours'
GROUP BY ek ORDER BY c DESC LIMIT 20;
EOF
       ;;
    status) cat >> /tmp/q-status.sql <<'EOF'
SELECT COALESCE(request_status,'<null>') rs, COUNT(*) c
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours'
GROUP BY rs ORDER BY c DESC;
EOF
       ;;
    failstage) cat >> /tmp/q-failstage.sql <<'EOF'
SELECT COALESCE(failure_stage,'<null>') fs, COUNT(*) c
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours' AND NOT success
GROUP BY fs ORDER BY c DESC;
EOF
       ;;
    detailcode) cat >> /tmp/q-detailcode.sql <<'EOF'
SELECT COALESCE(failure_detail_code,'<null>') dc, COUNT(*) c
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours' AND NOT success
GROUP BY dc ORDER BY c DESC LIMIT 30;
EOF
       ;;
    bytime) cat >> /tmp/q-bytime.sql <<'EOF'
SELECT date_trunc('hour', ts) hr, COUNT(*),
       COUNT(*) FILTER (WHERE NOT success) fail
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours'
GROUP BY hr ORDER BY hr;
EOF
       ;;
    model) cat >> /tmp/q-model.sql <<'EOF'
SELECT client_model, COUNT(*), COUNT(*) FILTER (WHERE NOT success) fail,
       ROUND(100.0*COUNT(*) FILTER (WHERE NOT success)/NULLIF(COUNT(*),0),1) fail_pct
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours'
GROUP BY client_model HAVING COUNT(*) > 30 ORDER BY fail DESC LIMIT 15;
EOF
       ;;
    cred) cat >> /tmp/q-cred.sql <<'EOF'
SELECT credential_id, COUNT(*), COUNT(*) FILTER (WHERE NOT success) fail,
       ROUND(100.0*COUNT(*) FILTER (WHERE NOT success)/NULLIF(COUNT(*),0),1) fail_pct
FROM request_logs_hot WHERE ts > NOW() - INTERVAL '36 hours' AND credential_id IS NOT NULL
GROUP BY credential_id HAVING COUNT(*) > 30 ORDER BY fail DESC LIMIT 15;
EOF
       ;;
  esac
done

for q in overview status errkind failstage detailcode bytime model cred; do
  $SSH_BASE_252 "docker exec -i $DB_CONTAINER psql -U $DB_USER -d $DB_NAME < /dev/stdin" < /tmp/q-$q.sql >> "$REPORT_DIR/252-db-$q.txt" 2>&1
done

echo "[5/5] Done. Artifacts:"
ls -la "$REPORT_DIR"
echo ""
echo "Next: review $REPORT_DIR/ANALYSIS_REPORT.md (or regenerate via 当前模板)"
