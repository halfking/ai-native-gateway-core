#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/ensure-llm-gateway-pg-role.sh
# Purpose:       Idempotent role / database bootstrap for the local
#                llm-gateway-pg container (kx-citus-pg17:offline-arm64).
#                Safe to re-run on an already-initialized cluster:
#                  - CREATE ROLE only when missing (create-only policy)
#                  - CREATE DATABASE only when missing
#                  - never ALTER ROLE for an existing role's password
#                    (password rotation is manual; see recreate-llm-gateway-pg.sh
#                    policy guard at scripts/local-dev/recreate-llm-gateway-pg.sh:70-77)
#                  - never touches table data, schema, or extensions
#
# Usage:
#   bash scripts/local-dev/ensure-llm-gateway-pg-role.sh
#
# Env (auto-detected; override if needed):
#   LLM_GATEWAY_PG_CONTAINER  default llm-gateway-pg
#   LLM_GATEWAY_PG_USER       default llm_gateway
#   LLM_GATEWAY_PG_PASSWORD   default read from /Users/xutaohuang/kaixuan/postgres/run/llm-gateway-pg.env
#   LLM_GATEWAY_PG_DATABASE   default llm_gateway
#
# Exit codes:
#   0  no-op (everything already in place)
#   1  role / db created or repaired (informational; not an error)
#   2  required env var missing (e.g. password not in env file)
#   3  connection / docker exec error
# -----------------------------------------------------------------------------
set -euo pipefail

CONTAINER="${LLM_GATEWAY_PG_CONTAINER:-llm-gateway-pg}"
PG_USER="${LLM_GATEWAY_PG_USER:-llm_gateway}"
PG_DB="${LLM_GATEWAY_PG_DATABASE:-llm_gateway}"
PG_ENV_FILE="${LLM_GATEWAY_PG_ENV_FILE:-/Users/xutaohuang/kaixuan/postgres/run/llm-gateway-pg.env}"

# Resolve password: explicit env wins, else read from container env file.
PG_PASS="${LLM_GATEWAY_PG_PASSWORD:-}"
if [[ -z "$PG_PASS" && -f "$PG_ENV_FILE" ]]; then
  PG_PASS="$(grep -E '^POSTGRES_PASSWORD=' "$PG_ENV_FILE" | head -1 | cut -d= -f2- || true)"
fi
if [[ -z "$PG_PASS" ]]; then
  echo "ensure-llm-gateway-pg-role: error: LLM_GATEWAY_PG_PASSWORD unset and no POSTGRES_PASSWORD in $PG_ENV_FILE" >&2
  echo "  set LLM_GATEWAY_PG_PASSWORD in the env, or pass it via your shell" >&2
  exit 2
fi

if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "ensure-llm-gateway-pg-role: error: container '$CONTAINER' not present" >&2
  exit 3
fi

run_psql() {
  docker exec -e PGPASSWORD="$PG_PASS" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U "$PG_USER" -d "$PG_DB" -Atqc "$1" 2>&1
}

changed=0

# --- 1. Role --------------------------------------------------------------
# CREATE ROLE IF NOT EXISTS is supported on PG 16+; for older PG we use the
# canonical DO-block guard. The container runs PG 17 (PG_VERSION=17) so
# IF NOT EXISTS works directly; the DO form is kept as defense-in-depth.
role_state=$(run_psql "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$PG_USER') THEN 'present' ELSE 'missing' END;")
if [[ "$role_state" == "missing" ]]; then
  # Escape any single quotes in PG_PASS (defensive; the SSOT pass is alphanumeric+symbols).
  esc_pass="${PG_PASS//\'/\'\'}"
  run_psql "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$PG_USER') THEN CREATE ROLE \"$PG_USER\" WITH LOGIN SUPERUSER PASSWORD '$esc_pass'; END IF; END \$\$;" >/dev/null
  echo "ensure-llm-gateway-pg-role: created role '$PG_USER'"
  changed=1
else
  echo "ensure-llm-gateway-pg-role: role '$PG_USER' already present; no-op (create-only policy)"
fi

# --- 2. Database ----------------------------------------------------------
db_state=$(run_psql "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_database WHERE datname='$PG_DB') THEN 'present' ELSE 'missing' END;")
if [[ "$db_state" == "missing" ]]; then
  run_psql "CREATE DATABASE \"$PG_DB\" OWNER \"$PG_USER\";" >/dev/null
  echo "ensure-llm-gateway-pg-role: created database '$PG_DB' with owner '$PG_USER'"
  changed=1
else
  echo "ensure-llm-gateway-pg-role: database '$PG_DB' already present; no-op"
fi

# --- 3. Privilege sanity (grants are idempotent) --------------------------
run_psql "GRANT ALL PRIVILEGES ON DATABASE \"$PG_DB\" TO \"$PG_USER\";" >/dev/null 2>&1 || true

if (( changed == 0 )); then
  exit 0
fi
exit 1