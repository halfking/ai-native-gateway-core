#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/redis-cli" <<'REDIS'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$REDIS_MOCK_CALLS"
args="$*"
case "$args" in
  *' DBSIZE') printf '1\n' ;;
  *' INFO memory') printf 'used_memory_human:1M\n' ;;
  *'--scan --pattern session:gw_*') printf 'session:gw_one\n' ;;
  *'--scan --pattern session:stopped:*') printf 'session:stopped:tenant-a\n' ;;
  *' TTL session:gw_one') printf '%s\n' '-1' ;;
  *' TTL session:stopped:tenant-a') printf '%s\n' '-1' ;;
  *) ;;
esac
REDIS
chmod +x "$tmpdir/redis-cli"

calls="$tmpdir/calls"
common_env=(
  "PATH=$tmpdir:$PATH"
  "REDIS_MOCK_CALLS=$calls"
  'LLM_GATEWAY_REDIS_ADDR=127.0.0.1:6379'
  'LLM_GATEWAY_REDIS_PASSWORD=test-password'
)

: >"$calls"
env "${common_env[@]}" bash "$repo_root/scripts/apply_redis_ttl_optimization.sh" >/dev/null
grep -q -- ' -n 2 ' "$calls"
if grep -Eq ' (EXPIRE|UNLINK) ' "$calls"; then
  printf 'report-only TTL script issued a mutating Redis command\n' >&2
  exit 1
fi

: >"$calls"
env "${common_env[@]}" bash "$repo_root/scripts/clean_redis_leaks.sh" >/dev/null
grep -q -- ' -n 2 ' "$calls"
if grep -Eq ' (EXPIRE|UNLINK) ' "$calls"; then
  printf 'report-only cleanup script issued a mutating Redis command\n' >&2
  exit 1
fi

: >"$calls"
env "${common_env[@]}" APPLY=1 bash "$repo_root/scripts/apply_redis_ttl_optimization.sh" >/dev/null
if ! grep -Eq ' EXPIRE session:gw_one 259200$' "$calls"; then
  printf 'APPLY=1 TTL script did not expire the matching key\n' >&2
  exit 1
fi

: >"$calls"
env "${common_env[@]}" APPLY=1 CONFIRM_DELETE_NO_TTL=delete-no-ttl-keys \
  bash "$repo_root/scripts/clean_redis_leaks.sh" >/dev/null
if ! grep -Eq ' UNLINK session:gw_one$' "$calls"; then
  printf 'explicit cleanup did not unlink the matching no-TTL key\n' >&2
  exit 1
fi
