#!/usr/bin/env bash
# Contract tests for the host-first resource discovery introduced 2026-10-10.
#
# The deploy entry point must prefer a service running natively on the host
# over a docker container, and must be able to install redis + postgresql when
# neither exists. These tests exercise the ordering with mocked binaries so
# they run anywhere (no docker, no live services, no network).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/deploy-local-lib.sh" || { printf 'FATAL: 无法 source %s\n' ""$ROOT/scripts/deploy-local-lib.sh"" >&2; exit 1; }

TMP=$(mktemp -d -t kx-host-first.XXXXXX)
trap 'rm -rf "$TMP"' EXIT

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS %s\n' "$*"; }

# ---------------------------------------------------------------------------
# 1. dl_host_pg_candidate_ports dedupes and honors caller overrides.
# ---------------------------------------------------------------------------
(
  LLM_GATEWAY_PG_PORT=6000 PGPORT=6001
  export LLM_GATEWAY_PG_PORT PGPORT
  ports=$(dl_host_pg_candidate_ports)
  [[ "$ports" == *6000* ]] || fail 'caller LLM_GATEWAY_PG_PORT not probed'
  [[ "$ports" != *6000*' '*'6000'* ]] || fail 'duplicate ports emitted'
  # 5432 (standard) and 54318 (this host's pg18 instance) must be defaults.
  [[ "$ports" == *5432* && "$ports" == *54318* ]] || fail 'default host pg ports missing'
)
pass 'host pg candidate ports honor overrides and dedupe'

# ---------------------------------------------------------------------------
# 2. A healthy host PG is claimed as DL_DB_MODE=host and yields a DSN.
# ---------------------------------------------------------------------------
mkdir -p "$TMP/bin"
cat >"$TMP/bin/psql" <<'EOF'
#!/usr/bin/env bash
# Mirrors the real probe: the health check connects to the maintenance
# database `postgres`, so a cluster without the app database is still claimed.
# Only 54318 answers, so the assertion also proves the probe walked the
# candidate port list rather than accepting whatever came first.
[[ "$*" == *"SELECT 1"* && "$*" == *"-p 54318"* && "$*" == *"-d postgres"* ]] && exit 0
exit 1
EOF
chmod +x "$TMP/bin/psql"

export MOCK_BIN="$TMP/bin"
# Run the probe in a subshell and capture the values it would export, so the
# assertions below do not depend on set -u seeing an unset variable.
if result=$( ( unset LLM_GATEWAY_DATABASE_URL DATABASE_URL LLM_GATEWAY_PG_USER LLM_GATEWAY_PG_DATABASE
  dl_host_bin_prefixes() { printf '%s\n' "$MOCK_BIN"; }
  dl_pg_try_host
  printf '%s\n' "${DL_HOST_PG_PORT:-}" ) ); then
  port=$(printf '%s' "$result" | tail -1)
  [[ "$port" == 54318 ]] || fail "expected host port 54318, got '$port'"
  pass 'dl_pg_try_host claims a healthy host instance and records its port'
else
  fail 'dl_pg_try_host failed to claim a healthy host instance'
fi

# ---------------------------------------------------------------------------
# 3. A port that answers TCP but fails SELECT 1 is NOT claimed.
#    (mock psql always fails => the probe must fall through to the next port)
# ---------------------------------------------------------------------------
mkdir -p "$TMP/bin2"
cat >"$TMP/bin2/psql" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$TMP/bin2/psql"
export MOCK_BIN2="$TMP/bin2"
# Every SQL probe fails, so nothing may be claimed through the SQL path.
dead_port=$( ( unset LLM_GATEWAY_DATABASE_URL DATABASE_URL
  dl_host_bin_prefixes() { printf '%s\n' "$MOCK_BIN2"; }
  dl_pg_try_host >/dev/null 2>&1 || true
  printf '%s\n' "${DL_HOST_PG_PORT:-}" ) | tail -1 )
[[ "$dead_port" != 54318 ]] || fail 'claimed a port whose SQL probe failed'
pass 'unhealthy host pg is not claimed via the SQL probe'

# ---------------------------------------------------------------------------
# 3b. Regression: a live cluster whose app database does not exist yet must
#     still be claimed. Probing the app DB directly reported "database does
#     not exist" and refused a healthy server, pushing docker-less hosts into
#     a pointless install path.
# ---------------------------------------------------------------------------
mkdir -p "$TMP/bin4"
cat >"$TMP/bin4/psql" <<'EOF'
#!/usr/bin/env bash
# Answers only the maintenance-database probe, and only on the port this
# fixture pretends to be listening on. Exactly like a freshly initialized
# cluster that does not have the application database yet.
[[ "$*" == *"-d postgres"* && "$*" == *"-p 54318"* ]] && exit 0
exit 1
EOF
chmod +x "$TMP/bin4/psql"
export MOCK_BIN4="$TMP/bin4"
fresh_port=$( ( unset LLM_GATEWAY_DATABASE_URL DATABASE_URL
  dl_host_bin_prefixes() { printf '%s\n' "$MOCK_BIN4"; }
  dl_pg_try_host >/dev/null 2>&1 || true
  printf '%s\n' "${DL_HOST_PG_PORT:-}" ) | tail -1 )
[[ "$fresh_port" == 54318 ]] || fail "fresh cluster (no app db) must still be claimed, got '$fresh_port'"
pass 'host pg is claimed via the maintenance database even without the app db'

# ---------------------------------------------------------------------------
# 4. Host redis is claimed before docker, and a healthy PONG wins.
# ---------------------------------------------------------------------------
mkdir -p "$TMP/bin3"
cat >"$TMP/bin3/redis-cli" <<'EOF'
#!/usr/bin/env bash
[[ "$*" == *"ping"* && "$*" == *"-p 6379"* ]] && { printf 'PONG\n'; exit 0; }
exit 1
EOF
chmod +x "$TMP/bin3/redis-cli"
probe_redis() (
  unset LLM_GATEWAY_REDIS_ADDR
  dl_host_bin_prefixes() { printf '%s\n' "$MOCK_BIN3"; }
  dl_host_have() { [[ -x "$MOCK_BIN3/$1" ]] || command -v "$1" >/dev/null 2>&1; }
  dl_redis_host_listen_ports() { printf '6379\n'; }
  dl_redis_try_host
)
export MOCK_BIN3="$TMP/bin3"
redis_result=$( ( unset LLM_GATEWAY_REDIS_ADDR
  dl_host_bin_prefixes() { printf '%s\n' "$MOCK_BIN3"; }
  dl_host_have() { [[ -x "$MOCK_BIN3/$1" ]] || command -v "$1" >/dev/null 2>&1; }
  dl_redis_host_listen_ports() { printf '6379\n'; }
  dl_redis_try_host >/dev/null 2>&1 || true
  printf '%s|%s\n' "${LLM_GATEWAY_REDIS_ADDR:-}" "${DL_REDIS_MODE:-}" ) | tail -1 )
addr="${redis_result%%|*}"; mode="${redis_result##*|}"
[[ "$addr" == "127.0.0.1:6379" ]] || fail "redis addr not set: '$addr'"
[[ "$mode" == host ]] || fail "expected host mode, got '$mode'"
pass 'healthy host redis is claimed as DL_REDIS_MODE=host'

# ---------------------------------------------------------------------------
# 5. Static contract: detection order is host-before-docker in the source.
#    Guards against a future refactor silently restoring docker-first.
# ---------------------------------------------------------------------------
lib="$ROOT/scripts/deploy-local-lib.sh"
# `|| true` matters: under `set -e` a grep with no match aborts the whole
# suite before the assertion below can report which pattern went missing.
host_line=$(grep -n 'dl_pg_try_host' "$lib" | head -1 | cut -d: -f1 || true)
docker_line=$(grep -n '_dl_have docker && _dl_timeout 5 docker info' "$lib" | head -1 | cut -d: -f1 || true)
[[ -n "$host_line" && -n "$docker_line" ]] || fail "could not locate detection tiers (host='$host_line' docker='$docker_line')"
[[ "$host_line" -lt "$docker_line" ]] || fail "host probe (line $host_line) must precede docker probe (line $docker_line)"
pass 'host probe precedes docker probe in dl_detect_resources'

# Ordering alone is not enough: the docker tier must also be forbidden from
# OVERWRITING a slot the host already claimed. Restoring the unconditional
# assignment (the pre-2026-10-10 behavior) keeps the ordering intact while
# silently downgrading a healthy host PG to a container, so guard the guard.
grep -q 'DL_PG_CONTAINER" && "$DL_DB_MODE" == none' "$lib" \
  || fail 'docker tier may overwrite a host-claimed PG mode'
grep -q 'DL_REDIS_CONTAINER" && "$DL_REDIS_MODE" == none' "$lib" \
  || fail 'docker tier may overwrite a host-claimed Redis mode'
pass 'docker tier cannot overwrite a host-claimed service'

# The redis host probe must actually be wired into dl_detect_resources, not
# merely defined. A dead helper would keep every other assertion green.
grep -q '&& dl_redis_try_host; then' "$lib" \
  || fail 'dl_redis_try_host is defined but never invoked by dl_detect_resources'
grep -q '&& dl_pg_try_host; then' "$lib" \
  || fail 'dl_pg_try_host is defined but never invoked by dl_detect_resources'
pass 'host probe helpers are invoked from dl_detect_resources'

# ---------------------------------------------------------------------------
# 6. deploy-local.sh wires the auto-install path and never dies before it.
# ---------------------------------------------------------------------------
entry="$ROOT/scripts/deploy-local.sh"
grep -q 'install_host_services' "$entry" || fail 'auto-install not wired into ensure_resources'
grep -q 'DL_HOST_INSTALL' "$entry" || fail 'DL_HOST_INSTALL opt-out missing'
# The docker-unavailable die() must come after the auto-install attempt.
install_line=$(grep -n 'install_host_services "\$need_pg"' "$entry" | head -1 | cut -d: -f1 || true)
die_line=$(grep -n 'no usable PostgreSQL/Redis' "$entry" | head -1 | cut -d: -f1 || true)
[[ -n "$install_line" && -n "$die_line" ]] || fail "could not locate install/die ordering (install='$install_line' die='$die_line')"
[[ "$install_line" -lt "$die_line" ]] || fail "auto-install (line $install_line) must precede the docker die (line $die_line)"
pass 'auto-install runs before the docker-unavailable failure'

# ---------------------------------------------------------------------------
# 7. A host claim exports the host bin dirs into PATH so the deploy body can
#    keep using bare `psql` / `redis-cli` lookups (mock dirs prove the
#    prepend actually happens, dedup included).
# ---------------------------------------------------------------------------
mkdir -p "$TMP/hostbin"
probe_path() (
  PATH="/usr/bin:/bin"
  dl_host_bin_prefixes() { printf '%s\n' "$MOCK_HOSTBIN" "$MOCK_HOSTBIN"; }
  dl_export_host_bin_path
  printf '%s\n' "$PATH"
)
export MOCK_HOSTBIN="$TMP/hostbin"
new_path=$(probe_path | tail -1)
[[ "$new_path" == "$MOCK_HOSTBIN:"* ]] || fail "host bin dir not prepended to PATH: '$new_path'"
[[ "$new_path" != *"$MOCK_HOSTBIN:*$MOCK_HOSTBIN"* ]] || fail 'host bin dir duplicated in PATH'
grep -q 'dl_export_host_bin_path' "$lib" || fail 'dl_export_host_bin_path missing from lib'
grep -q 'dl_export_host_bin_path$' "$lib" || fail 'dl_export_host_bin_path never invoked from dl_detect_resources'
pass 'host claim prepends host bin dirs to PATH (deduped)'

# ---------------------------------------------------------------------------
# 8. The auto-install path really delivers PG17 + Citus columnar + Redis:
#    pgcolumnar must be built before first start (preload dependency), the
#    extension must be activated in the app database, and the PG tarball URL
#    must carry a full minor version.
# ---------------------------------------------------------------------------
grep -q 'dl_install_pgcolumnar "\$pg_prefix" || return 1' "$entry" \
  || fail 'pgcolumnar build is not a hard prerequisite of the host pg install'
preload_line=$(grep -n "shared_preload_libraries = 'pg_stat_statements,pgcolumnar'" "$entry" | head -1 | cut -d: -f1 || true)
[[ -n "$preload_line" ]] || fail 'host cluster config must preload pg_stat_statements,pgcolumnar'
grep -q "CREATE EXTENSION IF NOT EXISTS pgcolumnar" "$entry" \
  || fail 'columnar AM never activated in the application database'
grep -q 'dl_resolve_pg_full_version' "$entry" \
  || fail 'PG tarball version is not resolved to a full minor (postgresql-17.tar.gz does not exist upstream)'
# The preload must be appended BEFORE first start, i.e. the initdb block
# precedes the pg_ctl start call in the source.
start_line=$(grep -n 'pg_ctl" -D "\$datadir" -l' "$entry" | head -1 | cut -d: -f1 || true)
[[ -n "$preload_line" && -n "$start_line" && "$preload_line" -lt "$start_line" ]] \
  || fail "pgcolumnar preload (line $preload_line) must be written before pg_ctl start (line $start_line)"
pass 'auto-install builds PG + pgcolumnar and activates the columnar AM'

printf 'ALL HOST-FIRST CONTRACT TESTS PASSED\n'
