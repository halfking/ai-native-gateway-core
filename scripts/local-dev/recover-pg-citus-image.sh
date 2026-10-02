#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/recover-pg-citus-image.sh
# Purpose:       Detect and repair the "stale citus extension but missing
#                $libdir/citus" condition on llm-gateway-pg. Caused when the
#                container is started with a plain postgres:<ver> image while
#                the data dir was initdb'd by kx-citus-pg17 — every DROP
#                POLICY/INDEX fires citus_drop_trigger() event trigger and
#                crashes the statement with
#                `could not access file "$libdir/citus": No such file or
#                directory`. Symptom seen in pg log; migration startup
#                (e.g. 552_request_journey_durable_outbox.sql) fails with
#                DROP POLICY IF EXISTS <...>_tenant_isolation ... ERROR.
# Recovery:      1. Load kx-citus-pg17 image from local tarball cache
#                   (kx-citus-pg17-13.3.0-vector-arm64.tar.gz); tag as
#                   kx-citus-pg17:offline-arm64 (matches recreate script).
#                2. Stop + remove the broken container (data dir preserved).
#                3. Start a new container with the same bind/env/network.
#                4. Set shared_preload_libraries via postgresql.auto.conf
#                   (must contain citus + citus_columnar; ALTER SYSTEM
#                   quoting is fragile for string lists — write directly).
#                5. Restart so postmaster reloads shared_preload_libraries.
#                6. Verify: extensions loaded, event-trigger DROP POLICY
#                   path works, USING columnar table can be created.
# Policy:        CREATE-ONLY for users/passwords (inherited 2026-08-31).
#                Does NOT alter any user or password; data dir is preserved
#                untouched apart from the auto.conf fix and a pg_ctl restart.
# Status:        active (introduced after 2026-09-23 incident)
# Changelog:
#   2026-09-23  v1.0  Initial version. Symptom triggered by 2026-09-23
#                     morning when llm-gateway-pg was observed running
#                     postgres:17-alpine instead of kx-citus-pg17. Root
#                     cause was a previous (operator) `docker run` on top of
#                     an existing data dir without verifying
#                     shared_preload_libraries. The fix here is deliberately
#                     minimal (image swap + postgresql.auto.conf patch) —
#                     never an `initdb` or volume wipe.
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/local-dev/recover-pg-citus-image.sh
#   bash scripts/local-dev/recover-pg-citus-image.sh --dry-run
#   bash scripts/local-dev/recover-pg-citus-image.sh --tarball <abs-path>
# -----------------------------------------------------------------------------
# Preconditions:
#   - llm-gateway-pg is the container currently running with the data dir at
#     $HOME/kaixuan/postgres (override via LLM_GATEWAY_PG_DATA_DIR). The
#     script reads the *actual* bind from `docker inspect .Mounts`, so a
#     mis-pointed container is detected before any destructive action.
#   - One of these tarballs is present locally:
#       ~/work/docker-base-images/database/kx-citus-pg17-13.3.0-vector-arm64.tar.gz
#       ~/work/docker-base-images/database/kx-citus-pg17-13.3.0-arm64.tar.gz
#     Override with --tarball.
# -----------------------------------------------------------------------------

set -euo pipefail

# ── Args ────────────────────────────────────────────────────────────────────
DRY_RUN=0
TARBALL_OVERRIDE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)  DRY_RUN=1; shift ;;
    --tarball)  TARBALL_OVERRIDE="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,42p' "$0"
      exit 0
      ;;
    *)
      echo "ERROR: unknown arg: $1" >&2
      exit 64
      ;;
  esac
done

# ── Constants ────────────────────────────────────────────────────────────────
CONTAINER_NAME="llm-gateway-pg"
EXPECTED_IMAGE="kx-citus-pg17:offline-arm64"
EXPECTED_IMAGE_REAL_TAG="registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-arm64"
NETWORK="shared-infra"
# data dir resolution order: override env → current container bind → ~/kaixuan/postgres
DATA_DIR="${LLM_GATEWAY_PG_DATA_DIR:-}"
TZ_VAL="Asia/Shanghai"
NO_PROXY="localhost,127.0.0.1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,*.local,ghcr.io,14.103.169.56,registry.kxpms.cn"

CANDIDATE_TARBALLS=(
  "$TARBALL_OVERRIDE"
  "$HOME/work/docker-base-images/database/kx-citus-pg17-13.3.0-vector-arm64.tar.gz"
  "$HOME/work/docker-base-images/database/kx-citus-pg17-13.3.0-arm64.tar.gz"
)

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "[DRY-RUN] $*"
  else
    eval "$@"
  fi
}

# ── 1. Detect broken state ──────────────────────────────────────────────────
echo "==> Step 1/6: detecting broken-image state on $CONTAINER_NAME"

if ! docker ps -a --format '{{.Names}}' 2>/dev/null | grep -q "^${CONTAINER_NAME}\$"; then
  echo "ERROR: container '$CONTAINER_NAME' does not exist; nothing to recover."
  echo "       Use scripts/local-dev/recreate-llm-gateway-pg.sh instead."
  exit 1
fi

CURRENT_IMAGE=$(docker inspect "$CONTAINER_NAME" --format '{{.Config.Image}}' 2>/dev/null || echo "<unknown>")
CURRENT_BIND=$(docker inspect "$CONTAINER_NAME" --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Source}}{{end}}{{end}}' 2>/dev/null)
echo "    container image : $CURRENT_IMAGE"
echo "    bind source     : ${CURRENT_BIND:-<none>}"
echo "    expected image  : $EXPECTED_IMAGE  (real: $EXPECTED_IMAGE_REAL_TAG)"

# Resolve data dir: container bind (truth) > override > default
if [[ -n "$CURRENT_BIND" ]]; then
  DATA_DIR="$CURRENT_BIND"
elif [[ -z "$DATA_DIR" ]]; then
  DATA_DIR="$HOME/kaixuan/postgres"
fi
echo "    using DATA_DIR  : $DATA_DIR"

# Probe inside container (initial — password-independent facts only)
PROBE_OK=1
PROBE_OUTPUT=$(docker exec "$CONTAINER_NAME" \
  bash -c '
    echo "PG_VERSION: $(cat /var/lib/postgresql/data/PG_VERSION 2>/dev/null || echo missing)"
    echo "libdir:     $(pg_config --pkglibdir 2>/dev/null || echo none)"
    echo "citus.so:   $(ls /usr/lib/postgresql/17/lib/citus.so 2>/dev/null || echo MISSING)"
    echo "columnar.so:$(ls /usr/lib/postgresql/17/lib/citus_columnar.so 2>/dev/null || echo MISSING)"
    echo "shared_preload: (deferred — needs PG auth)"
    echo "ext_citus:      (deferred — needs PG auth)"
    echo "ext_col:        (deferred — needs PG auth)"
    echo "evt_drop:       (deferred — needs PG auth)"
  ' 2>&1) || PROBE_OK=0
echo "$PROBE_OUTPUT" | sed 's/^/      /'

# Determine broken: image != expected AND (citus.so MISSING OR ext_citus present without shared_preload citus)
IS_BROKEN=0
if [[ "$CURRENT_IMAGE" != "$EXPECTED_IMAGE" ]]; then
  IS_BROKEN=1
  echo "    DIAG: container image != expected kx-citus-pg17:offline-arm64"
fi
if echo "$PROBE_OUTPUT" | grep -q "citus.so: *MISSING"; then
  IS_BROKEN=1
  echo "    DIAG: citus.so not present in /usr/lib/postgresql/17/lib/"
fi
if echo "$PROBE_OUTPUT" | grep -q "shared_preload: *$"; then
  echo "    DIAG: shared_preload_libraries is empty (citus not preloaded)"
fi

if [[ "$IS_BROKEN" -eq 0 ]]; then
  echo "    OK: no broken-image state detected; nothing to do."
  exit 0
fi

# ── 2. Locate tarball ────────────────────────────────────────────────────────
echo
echo "==> Step 2/6: locating kx-citus-pg17 image tarball"
SELECTED_TARBALL=""
for t in "${CANDIDATE_TARBALLS[@]}"; do
  [[ -z "$t" ]] && continue
  if [[ -f "$t" ]]; then
    SHA_FILE="${t}.sha256"
    if [[ -f "$SHA_FILE" ]]; then
      EXPECTED_SHA=$(awk '{print $1}' "$SHA_FILE")
      ACTUAL_SHA=$(shasum -a 256 "$t" 2>/dev/null | awk '{print $1}')
      if [[ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]]; then
        echo "    WARN: sha256 mismatch for $t — skipping"
        continue
      fi
      echo "    OK: $t (sha256 verified)"
    else
      echo "    OK: $t (no sidecar sha256 file)"
    fi
    SELECTED_TARBALL="$t"
    break
  fi
done

if [[ -z "$SELECTED_TARBALL" ]]; then
  echo "ERROR: no tarball found. Looked in:"
  printf '       %s\n' "${CANDIDATE_TARBALLS[@]}"
  exit 1
fi

# ── 3. Load image + tag ─────────────────────────────────────────────────────
echo
echo "==> Step 3/6: docker load + tag"
run "docker load -i '$SELECTED_TARBALL'"
run "docker tag '$EXPECTED_IMAGE_REAL_TAG' '$EXPECTED_IMAGE'"
run "docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^$EXPECTED_IMAGE\$'"

# ── 4. Stop + remove broken container (data dir preserved) ──────────────────
echo
echo "==> Step 4/6: stop + remove $CONTAINER_NAME (data dir preserved)"
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  run "docker stop '$CONTAINER_NAME'"
  run "docker rm '$CONTAINER_NAME'"
fi

# ── 5. Start fresh container with proper image ──────────────────────────────
echo
echo "==> Step 5/6: docker run with kx-citus-pg17:offline-arm64"
PORT_BIND="${LLM_GATEWAY_PG_PORT_BIND:-127.0.0.1:5432:5432}"
# Pull envs from existing container if any (preserve POSTGRES_PASSWORD from
# the broken container — recreated container uses entrypoint initdb logic
# which is no-op on existing data dir).
SRC_ENV=$(docker inspect "$CONTAINER_NAME" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null || true)
PG_USER=$(echo "$SRC_ENV" | sed -n 's/^POSTGRES_USER=//p' | head -1)
[[ -z "$PG_USER" ]] && PG_USER="llm_gateway"
PG_PASS=$(echo "$SRC_ENV" | sed -n 's/^POSTGRES_PASSWORD=//p' | head -1)
[[ -z "$PG_PASS" ]] && { echo "ERROR: cannot determine POSTGRES_PASSWORD"; exit 1; }
PG_DB=$(echo "$SRC_ENV" | sed -n 's/^POSTGRES_DB=//p' | head -1)
[[ -z "$PG_DB" ]] && PG_DB="llm_gateway"

# Re-run probe now that we know PG_USER/PG_PASS
PROBE_OUTPUT=$(docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" \
  bash -c "
    echo 'PG_VERSION: \$(cat /var/lib/postgresql/data/PG_VERSION 2>/dev/null || echo missing)'
    echo 'libdir:     \$(pg_config --pkglibdir 2>/dev/null || echo none)'
    echo 'citus.so:   \$(ls /usr/lib/postgresql/17/lib/citus.so 2>/dev/null || echo MISSING)'
    echo 'columnar.so:\$(ls /usr/lib/postgresql/17/lib/citus_columnar.so 2>/dev/null || echo MISSING)'
    echo 'shared_preload: \$(psql -U $PG_USER -d $PG_DB -tAc 'SHOW shared_preload_libraries' 2>/dev/null || echo unknown)'
    echo 'ext_citus:      \$(psql -U $PG_USER -d $PG_DB -tAc \"SELECT extversion FROM pg_extension WHERE extname='citus'\" 2>/dev/null || echo unknown)'
    echo 'ext_col:        \$(psql -U $PG_USER -d $PG_DB -tAc \"SELECT extversion FROM pg_extension WHERE extname='citus_columnar'\" 2>/dev/null || echo unknown)'
    echo 'evt_drop:       \$(psql -U $PG_USER -d $PG_DB -tAc \"SELECT count(*) FROM pg_event_trigger WHERE evtfoid::text LIKE '%citus%'\" 2>/dev/null || echo unknown)'
  " 2>&1) || PROBE_OK=0
echo "$PROBE_OUTPUT" | sed 's/^/      /'

run "docker run -d \
  --name '$CONTAINER_NAME' \
  --restart unless-stopped \
  --network '$NETWORK' \
  -p '$PORT_BIND' \
  -v '$DATA_DIR:/var/lib/postgresql/data' \
  -e POSTGRES_USER='$PG_USER' \
  -e POSTGRES_PASSWORD='$PG_PASS' \
  -e POSTGRES_DB='$PG_DB' \
  -e POSTGRES_INITDB_ARGS='--encoding=UTF-8 --lc-collate=C.UTF-8 --lc-ctype=C.UTF-8' \
  -e TZ='$TZ_VAL' \
  -e PGTZ='$TZ_VAL' \
  -e 'NO_PROXY=$NO_PROXY' \
  -e 'no_proxy=$NO_PROXY' \
  '$EXPECTED_IMAGE'"

# Wait for ready
for i in $(seq 1 30); do
  if docker exec "$CONTAINER_NAME" pg_isready -U "$PG_USER" -d "$PG_DB" 2>/dev/null | grep -q accepting; then
    echo "    container ready after ${i}s"
    break
  fi
  sleep 1
done

# ── 5b. Patch postgresql.auto.conf for shared_preload_libraries ──────────────
# (Citus requires being loaded at postmaster start, not via CREATE EXTENSION.)
echo
echo "==> Step 5b/6: patch postgresql.auto.conf (shared_preload_libraries)"
AUTO_CONF="$DATA_DIR/postgresql.auto.conf"
if [[ ! -f "$AUTO_CONF" ]]; then
  echo "ERROR: $AUTO_CONF missing — cannot patch"
  exit 1
fi
if grep -q "^shared_preload_libraries" "$AUTO_CONF"; then
  # Rewrite to the canonical form (ALTER SYSTEM's quoted-string form is a
  # known footgun for comma-separated lists).
  sed -i.bak \
    -e "s|^shared_preload_libraries.*|shared_preload_libraries = 'citus,citus_columnar,pg_stat_statements'|" \
    "$AUTO_CONF"
  echo "    rewrote shared_preload_libraries line in $AUTO_CONF"
else
  echo "shared_preload_libraries = 'citus,citus_columnar,pg_stat_statements'" >> "$AUTO_CONF"
  echo "    appended shared_preload_libraries to $AUTO_CONF"
fi

# Restart so postmaster reloads the preload list (ALTER SYSTEM applies, but
# the parameter is postmaster-only — requires full restart).
echo "    restarting container to apply shared_preload_libraries ..."
run "docker stop '$CONTAINER_NAME'"
run "docker start '$CONTAINER_NAME'"
for i in $(seq 1 30); do
  if docker exec "$CONTAINER_NAME" pg_isready -U "$PG_USER" -d "$PG_DB" 2>/dev/null | grep -q accepting; then
    echo "    container ready after ${i}s"
    break
  fi
  sleep 1
done

# ── 6. Verify ────────────────────────────────────────────────────────────────
echo
echo "==> Step 6/6: verify"
PASS=1

check() {
  local label="$1"; local query="$2"; local want="$3"
  local got
  got=$(docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" \
    psql -U "$PG_USER" -d "$PG_DB" -tAc "$query" 2>/dev/null | tr -d '[:space:]' || echo "<err>")
  if [[ "$got" == "$want" ]]; then
    echo "    PASS  $label  (got: $got)"
  else
    echo "    FAIL  $label  (want: $want / got: $got)"
    PASS=0
  fi
}

docker exec "$CONTAINER_NAME" ls /usr/lib/postgresql/17/lib/citus.so >/dev/null 2>&1 \
  && echo "    PASS  citus.so present" \
  || { echo "    FAIL  citus.so missing"; PASS=0; }
docker exec "$CONTAINER_NAME" ls /usr/lib/postgresql/17/lib/citus_columnar.so >/dev/null 2>&1 \
  && echo "    PASS  citus_columnar.so present" \
  || { echo "    FAIL  citus_columnar.so missing"; PASS=0; }

check "shared_preload_libraries set" \
  "SHOW shared_preload_libraries" \
  "citus,citus_columnar,pg_stat_statements"
check "citus extension" \
  "SELECT extversion FROM pg_extension WHERE extname='citus'" "13.3-1"
check "citus_columnar extension" \
  "SELECT extversion FROM pg_extension WHERE extname='citus_columnar'" "13.3-1"

# Event-trigger DROP POLICY path — the original failure mode
EVENT_TEST=$(docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" \
  psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 <<'SQL' 2>&1
BEGIN;
CREATE TABLE _pg_recover_evt (id INT, tenant TEXT);
ALTER TABLE _pg_recover_evt ENABLE ROW LEVEL SECURITY;
ALTER TABLE _pg_recover_evt FORCE ROW LEVEL SECURITY;
CREATE POLICY _pg_recover_evt_p ON _pg_recover_evt USING (true);
DROP POLICY _pg_recover_evt_p ON _pg_recover_evt;
ROLLBACK;
SQL
)
if echo "$EVENT_TEST" | grep -q "DROP POLICY" && ! echo "$EVENT_TEST" | grep -q 'could not access file "\$libdir/citus"'; then
  echo "    PASS  DROP POLICY event-trigger path"
else
  echo "    FAIL  DROP POLICY event-trigger path:"
  echo "$EVENT_TEST" | sed 's/^/           /'
  PASS=0
fi

# Columnar storage — create + drop a USING columnar table
COL_TEST=$(docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER_NAME" \
  psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 <<'SQL' 2>&1
BEGIN;
CREATE TABLE _pg_recover_col (id BIGSERIAL PRIMARY KEY, payload JSONB NOT NULL) USING columnar;
INSERT INTO _pg_recover_col (payload) SELECT jsonb_build_object('k',g) FROM generate_series(1,50) g;
DROP TABLE _pg_recover_col;
ROLLBACK;
SQL
)
if echo "$COL_TEST" | grep -q "INSERT 0" && echo "$COL_TEST" | grep -q "DROP TABLE"; then
  echo "    PASS  USING columnar create+insert+drop"
else
  echo "    FAIL  USING columnar:"
  echo "$COL_TEST" | sed 's/^/           /'
  PASS=0
fi

echo
if [[ "$PASS" -eq 1 ]]; then
  echo "==> All checks passed. llm-gateway-pg recovered."
  echo "    Next: re-run any startup migrations that aborted earlier"
  echo "    (e.g. sql/migrations/startup/552_request_journey_durable_outbox.sql)."
else
  echo "==> FAIL: one or more checks did not pass. Inspect logs."
  exit 2
fi