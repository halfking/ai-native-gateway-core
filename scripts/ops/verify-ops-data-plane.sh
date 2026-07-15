#!/usr/bin/env bash
# Verify 252 shared data-plane tables and gateway_instances regions.
set -euo pipefail

TARGET="${1:-154}"
SSH_PORT="${LLM_GATEWAY_SSH_PORT:-25022}"
SSH_KEY_FILE="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  [[ -f "$k" ]] && SSH_KEY_FILE="$k" && break
done
SSH_OPTS=(-i "$SSH_KEY_FILE" -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=12)

case "$TARGET" in
  154) SSH_HOST="${LLM_GATEWAY_154_SSH:-root@47.97.111.154}"; ENV_FILE="/etc/llm-gateway-go/env" ;;
  245) SSH_HOST="${LLM_GATEWAY_245_SSH:-root@8.136.114.245}"; ENV_FILE="/opt/llm-gateway-go/.env" ;;
  *) echo "用法: $0 <154|245>" >&2; exit 1 ;;
esac

echo "[verify-ops-data-plane] 通过 $TARGET 查询 252 数据面..."
ssh "${SSH_OPTS[@]}" "$SSH_HOST" "ENV='$ENV_FILE'; DB=\$(grep '^LLM_GATEWAY_DATABASE_URL=' \"\$ENV\" | cut -d= -f2-); \
psql \"\$DB\" -v ON_ERROR_STOP=1 <<'SQL'
\\echo '--- gateway_instances by region ---'
SELECT COALESCE(NULLIF(TRIM(region), ''), 'unknown') AS region, status, COUNT(*)::int
FROM gateway_instances GROUP BY 1, 2 ORDER BY 1, 2;
\\echo '--- latest nodes ---'
SELECT region, hostname, status, last_heartbeat::text
FROM gateway_instances ORDER BY last_heartbeat DESC LIMIT 10;
\\echo '--- data-plane table counts ---'
SELECT 'gateway_instances' AS tbl, COUNT(*)::bigint FROM gateway_instances
UNION ALL SELECT 'instance_heartbeats', COUNT(*)::bigint FROM instance_heartbeats
UNION ALL SELECT 'download_events', COUNT(*)::bigint FROM download_events
UNION ALL SELECT 'offline_activation_requests', COUNT(*)::bigint FROM offline_activation_requests
UNION ALL SELECT 'license_devices', COUNT(*)::bigint FROM license_devices
UNION ALL SELECT 'licenses_active', COUNT(*)::bigint FROM licenses WHERE revoked_at IS NULL;
SQL"
