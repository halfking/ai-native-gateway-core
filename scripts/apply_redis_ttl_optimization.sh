#!/usr/bin/env bash
# Applies the gateway's TTL caps using bounded client-side SCAN iterations.
#
# Required:
#   LLM_GATEWAY_REDIS_ADDR=host:port
#   LLM_GATEWAY_REDIS_PASSWORD=...
# Optional:
#   LLM_GATEWAY_REDIS_DB=2
#   APPLY=1                     # required before any EXPIRE command is issued
#
# The default is report-only. This script never deletes keys.

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

export REDISCLI_AUTH="$LLM_GATEWAY_REDIS_PASSWORD"

run_redis() {
  redis-cli --no-auth-warning -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" "$@"
}

scan_keys() {
  local pattern="$1"
  run_redis --scan --pattern "$pattern"
}

apply_ttl_cap() {
  local pattern="$1"
  local cap_seconds="$2"
  local label="$3"
  local matched=0
  local changed=0
  local skipped=0

  printf '\n[%s] pattern=%s cap=%ss\n' "$label" "$pattern" "$cap_seconds"
  while IFS= read -r key; do
    [[ -z "$key" ]] && continue
    matched=$((matched + 1))
    local ttl
    ttl="$(run_redis TTL "$key")"
    if [[ "$ttl" == "-1" || ( "$ttl" =~ ^[0-9]+$ && "$ttl" -gt "$cap_seconds" ) ]]; then
      if [[ "$APPLY" == "1" ]]; then
        run_redis EXPIRE "$key" "$cap_seconds" >/dev/null
        changed=$((changed + 1))
      else
        printf '  would expire: %s (current TTL=%s)\n' "$key" "$ttl"
        changed=$((changed + 1))
      fi
    else
      skipped=$((skipped + 1))
    fi
  done < <(scan_keys "$pattern")
  printf '  matched=%d %s=%d unchanged=%d\n' \
    "$matched" "$([[ "$APPLY" == "1" ]] && printf changed || printf would_change)" "$changed" "$skipped"
}

printf 'Redis TTL optimization\n'
printf 'target=%s db=%s mode=%s\n' "$LLM_GATEWAY_REDIS_ADDR" "$REDIS_DB" "$([[ "$APPLY" == "1" ]] && printf apply || printf report-only)"
printf 'dbsize=%s\n' "$(run_redis DBSIZE)"
printf 'memory=%s\n' "$(run_redis INFO memory | awk -F: '/^used_memory_human:/{gsub(/\r/, "", $2); print $2}')"

apply_ttl_cap 'llmgw:stats:delta:tenant:*' 86400 'tenant stats delta'
apply_ttl_cap 'llmgw:stats:delta:global:*' 86400 'global stats delta'
apply_ttl_cap 'llmgw:stats:baseline:tenant:*' 86400 'tenant stats baseline'
apply_ttl_cap 'llmgw:stats:board:tenant:*' 86400 'tenant stats board'
apply_ttl_cap 'llmgw:stats:board:global:*' 86400 'global stats board'
apply_ttl_cap 'session:gw_*' 259200 'session hash'
apply_ttl_cap 'session:key:*' 259200 'session key mapping'
apply_ttl_cap 'session_pref:*' 259200 'session preferences'
apply_ttl_cap 'session:apiKey:*:active' 259200 'active session index'
apply_ttl_cap 'pending_response:*' 3600 'pending response'
apply_ttl_cap 'pending_response:index:*' 3600 'pending response index'

printf '\n[session:stopped:*] report only; this script intentionally does not delete stopped indexes.\n'
stopped_count=0
while IFS= read -r key; do
  [[ -n "$key" ]] && stopped_count=$((stopped_count + 1))
done < <(scan_keys 'session:stopped:*')
printf '  matched=%d\n' "$stopped_count"
