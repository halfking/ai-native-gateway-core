#!/usr/bin/env bash
# analyze-partition-stats.sh — 154/245 经 SSH 对 252 llm_gateway 执行分区 ANALYZE
#
# 用法:
#   bash scripts/ops/analyze-partition-stats.sh 154
#   bash scripts/ops/analyze-partition-stats.sh 245
#   bash scripts/ops/analyze-partition-stats.sh 154 --months 3
#
set -euo pipefail

TARGET=${1:-154}
MONTHS=2
shift || true
while [[ $# -gt 0 ]]; do
  case "$1" in
    --months) MONTHS=$2; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

case "$TARGET" in
  154)
    SSH_HOST=${LLM_GATEWAY_154_SSH:-root@47.97.111.154}
    ENV_FILE=/etc/llm-gateway-go/env
    ;;
  245)
    SSH_HOST=${LLM_GATEWAY_245_SSH:-root@8.136.114.245}
    ENV_FILE=/opt/llm-gateway-go/.env
    ;;
  *) echo "target must be 154 or 245" >&2; exit 1 ;;
esac

SSH_PORT=${LLM_GATEWAY_SSH_PORT:-25022}
SSH_KEY="${SSH_KEY_FILE:-}"
for k in ~/.ssh/id_ed25519 ~/.ssh/56_id_rsa ~/.ssh/71_id_rsa; do
  [[ -f "$k" ]] && SSH_KEY="$k" && break
done

SSH=(ssh -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
[[ -n "$SSH_KEY" ]] && SSH+=(-i "$SSH_KEY")
SSH+=("$SSH_HOST")

echo "[analyze] target=$TARGET months=$MONTHS env=$ENV_FILE"

"${SSH[@]}" "set -euo pipefail
ENV='$ENV_FILE'
DB=\$(grep '^LLM_GATEWAY_DATABASE_URL=' \"\$ENV\" | cut -d= -f2-)
test -n \"\$DB\"
psql \"\$DB\" -v ON_ERROR_STOP=1 -c \"SELECT apply_llm_gateway_autovacuum_settings() AS autovacuum_tables_set;\"
psql \"\$DB\" -v ON_ERROR_STOP=1 -c \"SELECT analyze_llm_gateway_table_stats($MONTHS) AS tables_analyzed;\"
psql \"\$DB\" -c \"
SELECT relname, last_analyze, last_autoanalyze, n_live_tup
FROM pg_stat_user_tables
WHERE relname ~ '^(credential_model_index|model_probe_runs|request_logs)_(202[0-9]_[0-9]{2}|hot)\$'
ORDER BY relname;
\""

echo "[analyze] done"
