#!/usr/bin/env bash
# Drive the real rollback dispatcher; retired units must never be verified.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
eval "$(sed -n '/^do_rollback()/,/^}$/p' "$ROOT/scripts/deploy-seamless.sh")"

log() { :; }
warn() { :; }
ok() { :; }
err() { printf '%s\n' "$*" >&2; }
host_select_rollback_target() { echo previous-verified; }
target_field() {
  case "$2" in
    active_port) echo 8781 ;;
    candidate_unit) echo "$UNIT_TEMPLATE" ;;
    *) return 1 ;;
  esac
}
fake_ssh() {
  case "$1" in
    *"readlink "*) echo current-release ;;
    *"cat '/fake/run/active-port'"*) echo "$CURRENT_PORT" ;;
    *"cat '/fake/run/active-service'"*) echo "$CURRENT_SERVICE" ;;
    *) return 0 ;;
  esac
}
remote_probe() { return 0; }
zd_switch_upstream() { return 0; }
_env_file_for_target() { echo /fake/env; }
deploy_preflight_pg_from_remote_env() { return 0; }
upgrade_hide_all() { return 0; }
_verify_running_release() {
  printf '%s\n' "${2:-$SERVICE_NAME}" > "$TMP/verified-service"
  [[ "$1" == previous-verified && "${2:-$SERVICE_NAME}" == "$EXPECTED_SERVICE" && "$REJECT_VERIFY" == 0 ]]
}

REMOTE_ROOT=/fake
BIN_NAME=gateway
SSH_CMD=fake_ssh
REJECT_VERIFY=0
for TARGET in 245 154; do
  if [[ "$TARGET" == 245 ]]; then
    SERVICE_NAME=llmgo-245.service
    UNIT_TEMPLATE=llmgo-245-canary@.service
  else
    SERVICE_NAME=llm-gateway-go.service
    UNIT_TEMPLATE=llm-gateway-go-canary@.service
  fi
  EXPECTED_SERVICE="${UNIT_TEMPLATE%@.service}@8781.service"
  for CURRENT_PORT in 8781 8782; do
    CURRENT_SERVICE="${UNIT_TEMPLATE%@.service}@${CURRENT_PORT}.service"
    rm -f "$TMP/verified-service"
    ( do_rollback ) || { echo "FAIL: $TARGET:$CURRENT_PORT rollback verifier used retired service" >&2; exit 1; }
    [[ "$(cat "$TMP/verified-service")" == "$EXPECTED_SERVICE" ]]
    echo "PASS: $TARGET:$CURRENT_PORT verifies actual rollback unit"
  done
done
REJECT_VERIFY=1
if ( do_rollback ) >/dev/null 2>&1; then
  echo 'FAIL: rejected running-release verification was swallowed' >&2
  exit 1
fi
echo 'PASS: running-release verification failures still stop rollback'
