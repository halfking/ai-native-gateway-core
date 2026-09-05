#!/usr/bin/env bash
# Reports or explicitly removes gateway keys that have lost their TTL.
#
# Required:
#   LLM_GATEWAY_REDIS_ADDR=host:port
#   LLM_GATEWAY_REDIS_PASSWORD=...
# Optional:
#   LLM_GATEWAY_REDIS_DB=2
#   APPLY=1 CONFIRM_DELETE_NO_TTL=delete-no-ttl-keys
#
# The default is report-only. Deletion is intentionally opt-in because some
# Redis prefixes contain persistent coordination keys outside this gateway.

set -euo pipefail

: "${LLM_GATEWAY_REDIS_ADDR:?LLM_GATEWAY_REDIS_ADDR is required}"
: "${LLM_GATEWAY_REDIS_PASSWORD:?LLM_GATEWAY_REDIS_PASSWORD is required}"

REDIS_DB="${LLM_GATEWAY_REDIS_DB:-2}"
APPLY="${APPLY:-0}"
REDIS_HOST="${LLM_GATEWAY_REDIS_ADDR%:*}"
REDIS_PORT="${LLM_GATEWAY_REDIS_ADDR##*:}"

if [[ -z "$REDIS_HOST" || -z "$REDIS_PORT" || "$REDIS_HOST" == "$REDIS_PORT" ]]; then
  printf 'LLM_GATEWAY_REDIS_ADDR must use host:port form\n' >&2
  exit 2
fi
if [[ "$APPLY" == "1" && "${CONFIRM_DELETE_NO_TTL:-}" != "delete-no-ttl-keys" ]]; then
  printf 'Refusing deletion: set CONFIRM_DELETE_NO_TTL=delete-no-ttl-keys with APPLY=1.\n' >&2
  exit 2
fi

export REDISCLI_AUTH="$LLM_GATEWAY_REDIS_PASSWORD"

run_redis() {
  redis-cli --no-auth-warning -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" "$@"
}

scan_keys() {
  local pattern="$1"
  run_redis --scan --pattern "$pattern"
}

report_or_remove_no_ttl() {
  local pattern="$1"
  local label="$2"
  local matched=0
  local no_ttl=0
  local removed=0

  printf '\n[%s] pattern=%s\n' "$label" "$pattern"
  while IFS= read -r key; do
    [[ -z "$key" ]] && continue
    matched=$((matched + 1))
    local ttl
    ttl="$(run_redis TTL "$key")"
    [[ "$ttl" == "-1" ]] || continue
    no_ttl=$((no_ttl + 1))
    if [[ "$APPLY" == "1" ]]; then
      run_redis UNLINK "$key" >/dev/null
      removed=$((removed + 1))
    else
      printf '  would unlink: %s\n' "$key"
    fi
  done < <(scan_keys "$pattern")
  if [[ "$APPLY" == "1" ]]; then
    printf '  matched=%d no_ttl=%d removed=%d\n' "$matched" "$no_ttl" "$removed"
  else
    printf '  matched=%d no_ttl=%d\n' "$matched" "$no_ttl"
  fi
}

printf 'Redis no-TTL audit\n'
printf 'target=%s db=%s mode=%s\n' "$LLM_GATEWAY_REDIS_ADDR" "$REDIS_DB" "$([[ "$APPLY" == "1" ]] && printf delete || printf report-only)"

report_or_remove_no_ttl 'session:gw_*' 'session hash'
report_or_remove_no_ttl 'session:key:*' 'session key mapping'
report_or_remove_no_ttl 'session_pref:*' 'session preferences'
report_or_remove_no_ttl 'pending_response:*' 'pending response'

printf '\nllmgw:live:* and llmgw:stats:* are intentionally excluded: their lifecycle is governed by application code and requires prefix-specific review.\n'
