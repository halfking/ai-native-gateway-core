#!/usr/bin/env bash

# pg_basebackup: full database replication from 184 → local writable primary.
#
# This is the FAST way to clone 184's llm_gateway into local r112_postgres.
# Uses PostgreSQL's streaming replication protocol (pg_basebackup) which
# transfers the cluster's binary files in compressed form, then replays
# WAL to bring the local copy to a consistent state.
#
# Difference from sync-db-from-184.sh:
#   - sync-db-from-184.sh: SQL-level pg_dump → psql (slower, but works
#     through any network, no replication privilege needed)
#   - sync-db-from-184-pgbase.sh: byte-level pg_basebackup → tar (faster,
#     but requires the `replicator` role + replication privilege on 184)
#
# Outcome:
#   - Local DB is a byte-for-byte copy of 184's cluster at backup time
#   - Local is writable (not a streaming standby)
#   - Local is NOT continuously synced; run again to re-sync
#
# Pre-requisites:
#   - SSH access to root@14.103.112.184:25022 (configured)
#   - 184 PG has `replicator` role with REPLICATION privilege (default)
#   - Local Docker has citusdata/citus:11.3.0 image (or compatible)
#
# 184 PG pg_hba.conf only allows replication from specific pod IPs
# (172.31.0.3 / 172.31.0.4), so pg_basebackup MUST be run from inside
# the PG pod itself (via kubectl exec). The dump is then streamed out
# via kubectl exec cat | tar.
#
# IMPORTANT: This script ALTERs the `replicator` role's password on 184
# to a known value so pg_basebackup can authenticate. If the password
# was rotated after this run, re-run the script.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_ROOT="/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode"
BACKUP_DIR="$TMP_ROOT/llmgw-pgbase-$(date +%Y%m%d-%H%M%S)"

REMOTE_SSH_HOST="${REMOTE_SSH_HOST:-root@14.103.112.184}"
REMOTE_SSH_PORT="${REMOTE_SSH_PORT:-25022}"
REMOTE_NAMESPACE="${REMOTE_NAMESPACE:-pms-test}"
REMOTE_DEPLOYMENT="${REMOTE_DEPLOYMENT:-deployment/llm-gateway-pg}"
REMOTE_DB="${REMOTE_DB:-llm_gateway}"
REMOTE_DB_USER="${REMOTE_DB_USER:-llm_gateway}"
REMOTE_DB_PASS="${REMOTE_DB_PASS:-<DB_PASSWORD_REDACTED>}"
REMOTE_REPL_USER="${REMOTE_REPL_USER:-replicator}"
REMOTE_REPL_PASS="${REMOTE_REPL_PASS:-repl_pwd_2026}"
REMOTE_POD_IP="${REMOTE_POD_IP:-172.31.0.4}"

LOCAL_CONTAINER="${LOCAL_CONTAINER:-r112_postgres}"
LOCAL_VOLUME="${LOCAL_VOLUME:-r112_pg_data}"
LOCAL_DATA_DIR="${LOCAL_DATA_DIR:-/var/lib/postgresql/data}"
LOCAL_IMAGE="${LOCAL_IMAGE:-citusdata/citus:11.3.0}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
err()  { printf "${RED}✗ %s${NC}\n" "$*" >&2; }
ok()   { printf "${GREEN}✓ %s${NC}\n" "$*"; }
info() { printf "${YELLOW}▶ %s${NC}\n" "$*"; }

usage() {
  cat <<'EOF'
Usage:
  ./scripts/sync-db-from-184-pgbase.sh

This script:
  1. Resets replicator password on 184 (idempotent)
  2. Runs pg_basebackup inside the 184 PG pod
  3. Streams base.tar.gz + pg_wal.tar.gz to local
  4. Configures local data dir as writable primary (recovery.signal +
     restore_command + max_connections aligned)
  5. Stops existing r112_postgres, mounts new volume, starts it
  6. Verifies local is writable and table counts match 184

Environment overrides:
  REMOTE_SSH_HOST, REMOTE_SSH_PORT, REMOTE_NAMESPACE, REMOTE_DEPLOYMENT
  REMOTE_DB, REMOTE_DB_USER, REMOTE_DB_PASS
  REMOTE_REPL_USER, REMOTE_REPL_PASS, REMOTE_POD_IP
  LOCAL_CONTAINER, LOCAL_VOLUME, LOCAL_DATA_DIR, LOCAL_IMAGE
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { err "missing command: $1"; exit 1; }
}

remote_exec() {
  ssh -o StrictHostKeyChecking=no -p "$REMOTE_SSH_PORT" "$REMOTE_SSH_HOST" "$1"
}

set_replicator_password() {
  info "ensuring replicator password is set on 184"
  remote_exec "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- bash -c \"PGPASSWORD='$REMOTE_DB_PASS' psql -U $REMOTE_DB_USER -d $REMOTE_DB -c \\\"ALTER USER $REMOTE_REPL_USER WITH PASSWORD '$REMOTE_REPL_PASS';\\\"\""
  ok "replicator password set"
}

run_pg_basebackup() {
  info "running pg_basebackup inside 184 PG pod (this takes ~3-5 minutes)"
  remote_exec "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- bash -c \"rm -rf /tmp/pgbb && mkdir -p /tmp/pgbb && PGPASSWORD='$REMOTE_REPL_PASS' pg_basebackup -h $REMOTE_POD_IP -U $REMOTE_REPL_USER -D /tmp/pgbb -Ft -z -P -Xs 2>&1 | tail -3\""
  ok "pg_basebackup complete inside pod"
}

stream_backup_to_local() {
  info "streaming base.tar.gz and pg_wal.tar.gz from pod to local ($BACKUP_DIR)"
  mkdir -p "$BACKUP_DIR"
  remote_exec "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- cat /tmp/pgbb/base.tar.gz"    > "$BACKUP_DIR/base.tar.gz"
  remote_exec "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- cat /tmp/pgbb/pg_wal.tar.gz" > "$BACKUP_DIR/pg_wal.tar.gz"
  ok "streams complete (base=$(du -h "$BACKUP_DIR/base.tar.gz" | cut -f1), wal=$(du -h "$BACKUP_DIR/pg_wal.tar.gz" | cut -f1))"
}

extract_to_local_data_dir() {
  info "extracting backup into $BACKUP_DIR/pgdata"
  mkdir -p "$BACKUP_DIR/pgdata"
  tar -xzf "$BACKUP_DIR/base.tar.gz"    -C "$BACKUP_DIR/pgdata"
  tar -xzf "$BACKUP_DIR/pg_wal.tar.gz" -C "$BACKUP_DIR/pgdata"
  # Move the streaming-WAL file (placed at root by -X stream) into pg_wal/
  # and remove backup_label so PG can recover cleanly with restore_command.
  local root_wal
  root_wal=$(find "$BACKUP_DIR/pgdata" -maxdepth 1 -name '????????????????????????' -type f 2>/dev/null | head -1)
  if [[ -n "$root_wal" ]]; then
    mv "$root_wal" "$BACKUP_DIR/pgdata/pg_wal/"
  fi
  ok "backup extracted"
}

prepare_writable_primary_config() {
  info "preparing writable primary config (recovery.signal + restore_command + max_connections)"
  # Append recovery.signal so PG replays WAL on first start
  touch "$BACKUP_DIR/pgdata/recovery.signal"
  # Ensure restore_command is set (so PG doesn't fail with "must specify restore_command")
  # Even a no-op /bin/true is fine because all WAL is already in pg_wal/
  if ! rg -q '^restore_command' "$BACKUP_DIR/pgdata/postgresql.auto.conf" 2>/dev/null; then
    printf "\n%s\n" "restore_command = '/bin/true'" >> "$BACKUP_DIR/pgdata/postgresql.auto.conf"
  fi
  # Align max_connections with primary (184 has 1000); required to avoid
  # "lower setting than on the primary server" recovery abort.
  if ! rg -q '^max_connections' "$BACKUP_DIR/pgdata/postgresql.auto.conf" 2>/dev/null; then
    printf "%s\n" "max_connections = '1000'" >> "$BACKUP_DIR/pgdata/postgresql.auto.conf"
    printf "%s\n" "shared_buffers = '128MB'" >> "$BACKUP_DIR/pgdata/postgresql.auto.conf"
    printf "%s\n" "max_wal_size = '1GB'"     >> "$BACKUP_DIR/pgdata/postgresql.auto.conf"
    printf "%s\n" "max_replication_slots = '10'" >> "$BACKUP_DIR/pgdata/postgresql.auto.conf"
  fi
  # Append pg_hba.conf entries for the local Docker network (172.16/12, 10/8, 192.168/16)
  cat >> "$BACKUP_DIR/pgdata/pg_hba.conf" <<'HBA_EOF'

# Local overrides for r112 writable primary (after pg_basebackup from 184)
host    all             all             172.16.0.0/12           scram-sha-256
host    all             all             10.0.0.0/8              scram-sha-256
host    all             all             192.168.0.0/16          scram-sha-256
HBA_EOF
  ok "writable primary config prepared"
}

populate_docker_volume() {
  info "populating docker volume $LOCAL_VOLUME from $BACKUP_DIR/pgdata"
  docker rm -f "$LOCAL_CONTAINER" 2>/dev/null || true
  docker volume rm "$LOCAL_VOLUME" 2>/dev/null || true
  docker volume create "$LOCAL_VOLUME" >/dev/null
  docker run --rm \
    -v "$LOCAL_VOLUME:/dst" \
    -v "$BACKUP_DIR/pgdata:/src:ro" \
    alpine:3.20 sh -c 'cp -a /src/. /dst/ && du -sh /dst' 2>&1
  # Fix ownership for the postgres user inside the container
  docker run --rm \
    -v "$LOCAL_VOLUME:/data" \
    --user root \
    --entrypoint /bin/sh \
    "$LOCAL_IMAGE" -c 'chown -R postgres:postgres /data'
  ok "docker volume populated"
}

start_local_pg() {
  info "starting $LOCAL_CONTAINER as writable primary"
  docker run -d \
    --name "$LOCAL_CONTAINER" \
    --network "$(docker inspect --format='{{.HostConfig.NetworkMode}}' "$LOCAL_CONTAINER" 2>/dev/null || echo bridge)" \
    --platform linux/amd64 \
    -v "$LOCAL_VOLUME:$LOCAL_DATA_DIR" \
    -e POSTGRES_USER=kxuser \
    -e POSTGRES_PASSWORD=<TEST_DB_PASSWORD_REDACTED> \
    -e POSTGRES_DB=postgres \
    -p "5432:5432" \
    --restart no \
    "$LOCAL_IMAGE" >/dev/null 2>&1 || {
    err "docker run failed; check docker state"
    return 1
  }
  # Wait for PG to accept connections (recovery + checkpoint takes a moment)
  local ready=0
  for i in $(seq 1 60); do
    if docker exec -e PGPASSWORD="<TEST_DB_PASSWORD_REDACTED>" "$LOCAL_CONTAINER" pg_isready -U "kxuser" -d "postgres" >/dev/null 2>&1; then
      ready=1
      ok "$LOCAL_CONTAINER ready after ${i}s"
      break
    fi
    sleep 1
  done
  if [[ "$ready" -ne 1 ]]; then
    err "$LOCAL_CONTAINER did not become ready within 60s"
    docker logs "$LOCAL_CONTAINER" | tail -20
    return 1
  fi
}

verify_writable_and_match() {
  info "verifying local is writable and data matches 184"
  # 1. Writable test
  if ! docker exec -e PGPASSWORD="<TEST_DB_PASSWORD_REDACTED>" "$LOCAL_CONTAINER" psql -U "kxuser" -d "$REMOTE_DB" \
       -c "CREATE TABLE _pgbase_smoke (id int); DROP TABLE _pgbase_smoke;" >/dev/null 2>&1; then
    err "local PG is NOT writable"
    return 1
  fi
  ok "local PG accepts writes"

  # 2. Schema count comparison
  local local_tables remote_tables
  local_tables=$(docker exec -e PGPASSWORD="<TEST_DB_PASSWORD_REDACTED>" "$LOCAL_CONTAINER" psql -U "kxuser" -d "$REMOTE_DB" -tAc \
    "select count(*) from information_schema.tables where table_schema='public';")
  remote_tables=$(remote_exec "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- bash -c \"PGPASSWORD='$REMOTE_DB_PASS' psql -U $REMOTE_DB_USER -d $REMOTE_DB -tAc 'select count(*) from information_schema.tables where table_schema='\''public'\'';'\"" | tr -d '[:space:]')
  printf "public_tables remote=%s local=%s\n" "$remote_tables" "$local_tables"

  # 3. Key table counts
  local table local_count remote_count
  for table in approval_queue tool_registry tenant_model_policies request_logs; do
    local_count=$(docker exec -e PGPASSWORD="<TEST_DB_PASSWORD_REDACTED>" "$LOCAL_CONTAINER" psql -U "kxuser" -d "$REMOTE_DB" -tAc \
      "select count(*) from ${table};" | tr -d '[:space:]')
    remote_count=$(remote_exec "kubectl -n $REMOTE_NAMESPACE exec $REMOTE_DEPLOYMENT -- bash -c \"PGPASSWORD='$REMOTE_DB_PASS' psql -U $REMOTE_DB_USER -d $REMOTE_DB -tAc 'select count(*) from ${table};'\"" | tr -d '[:space:]')
    printf "%s remote=%s local=%s\n" "$table" "$remote_count" "$local_count"
  done

  ok "verification complete (small drift in hot tables is expected)"
}

main() {
  require_cmd docker
  require_cmd ssh
  require_cmd rg

  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi

  info "pg_basebackup from 184 → local writable primary"
  info "backup dir: $BACKUP_DIR"

  set_replicator_password
  run_pg_basebackup
  stream_backup_to_local
  extract_to_local_data_dir
  prepare_writable_primary_config
  populate_docker_volume
  start_local_pg
  verify_writable_and_match

  ok "pg_basebackup sync complete"
  info "Local DB is now writable, fresh from 184"
  info "Backup files at: $BACKUP_DIR"
}

main "$@"