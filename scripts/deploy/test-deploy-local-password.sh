#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/scripts"
ENV_FILE="$(mktemp /tmp/llmgw-password-env.XXXXXX)"
trap 'rm -f "$ENV_FILE" "$ENV_FILE.bak" "$ENV_FILE".tmp.*' EXIT
chmod 600 "$ENV_FILE"
cat >"$ENV_FILE" <<'EOF'
export LLM_GATEWAY_ADMIN_PASSWORD=old
export LLM_GATEWAY_SEED_ADMIN_PASSWORD=old
export LLM_GATEWAY_ADMIN_USER=admin
EOF

LLM_GATEWAY_ENV_FILE="$ENV_FILE" bash -c '
  source "$1/deploy-local.sh"
  persist_admin_password "A&B"
' _ "$SCRIPT_DIR"

values="$(env -i bash -c 'source "$1"; printf "%s|%s" "$LLM_GATEWAY_ADMIN_PASSWORD" "$LLM_GATEWAY_SEED_ADMIN_PASSWORD"' _ "$ENV_FILE")"
[[ "$values" == 'A&B|A&B' ]]
[[ "$(stat -f '%Lp' "$ENV_FILE")" == 600 ]]

if grep -qF 'Admin password changed for first login: ' "$SCRIPT_DIR/deploy-local.sh"; then
  printf 'password output regression detected\n' >&2
  exit 1
fi

printf 'deploy-local password escaping test passed\n'
