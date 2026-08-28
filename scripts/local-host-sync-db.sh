#!/usr/bin/env bash
# ============================================================================
# scripts/local-host-sync-db.sh — 同步线上数据库到本地 Docker PG
#
# 用法:
#   bash scripts/local-host-sync-db.sh            # full (schema + cold data)
#   bash scripts/local-host-sync-db.sh --schema-only
#   bash scripts/local-host-sync-db.sh --backup-only
#   bash scripts/local-host-sync-db.sh --verify   # 仅校验 252 vs local 表清单
#
# 前置: env-injector (~/workspace/ai-native-tools/envs/loader.sh) + SSH tunnel
# 参考: docs/06-deployment/02-database/local-pg-sync-from-252.md
#       .agents/skills/db-sync-252-local/SKILL.md
#
# 输出: 所有 PG 数据已容器外, 路径为 ~/Downloads/llm-gateway-files/postgres/data
#       (由 docker run -v .../postgres/data:/var/lib/postgresql/data 绑定)
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# shellcheck source=local-host-layout-helper.sh
source "$SCRIPT_DIR/local-host-layout-helper.sh"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
BLUE=$'\033[0;34m'; NC=$'\033[0m'
log()  { echo -e "${BLUE}[lh-sync]${NC} $*"; }
ok()   { echo -e "${GREEN}  ✓${NC} $*"; }
warn() { echo -e "${YELLOW}  ⚠${NC} $*"; }
err()  { echo -e "${RED}  ✗${NC} $*" >&2; }
head() { echo -e "\n${BLUE}━━━ $* ━━━${NC}"; }

MODE="full"
BACKUP_ONLY=false
VERIFY_ONLY=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --schema-only) MODE="schema"; shift ;;
    --backup-only) BACKUP_ONLY=true; shift ;;
    --verify)      VERIFY_ONLY=true; shift ;;
    --root) LLM_GATEWAY_FILES_ROOT="$2"; export LLM_GATEWAY_FILES_ROOT; shift 2 ;;
    -h|--help)
      sed -n '2,18p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) err "unknown arg: $1"; exit 1 ;;
  esac
done

lh_require_root >/dev/null

# ── 1. Load credentials ──────────────────────────────────────────────────
head "load env-injector credentials"
if ! source ~/workspace/ai-native-tools/envs/loader.sh --project llm-gateway-go --server 115.29.212.252 2>/dev/null; then
  err "failed to load envs (need ~/workspace/ai-native-tools/envs/loader.sh)"
  exit 1
fi
export PG_PASS_252="$COMMON_PG_SUPERUSER_PASS"
export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
export PG_PASS_LOCAL="$COMMON_PG_SUPERUSER_PASS"
ok "credentials loaded (COMMON_PG_SUPERUSER_PASS, COMMON_REDIS_PASSWORD_252)"

# ── 2. Verify local Docker PG is up ───────────────────────────────────────
head "verify local PG container"
if ! docker ps --format '{{.Names}}' | grep -q '^llm-gateway-pg$'; then
  err "PG container llm-gateway-pg is not running"
  echo "  start it with:"
  echo "    bash scripts/local-host-sync-db.sh (will recreate from bind-mount)"
  exit 1
fi

DATA_DIR=$(lh_layout_vars | sed -n 's/^postgres_data_dir=//p')
if ! docker inspect llm-gateway-pg --format '{{range .Mounts}}{{.Source}}{{end}}' | grep -q "$DATA_DIR"; then
  warn "PG container is not bound to $DATA_DIR — consider recreating:"
  echo "    bash scripts/local-dev/recreate-llm-gateway-pg.sh"
fi

LOCAL_TABLE_COUNT=$(docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg \
  psql -U llm_gateway -d llm_gateway -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null | tail -1 || echo 0)
ok "local llm_gateway has $LOCAL_TABLE_COUNT tables"

# ── 3. Setup SSH tunnel ──────────────────────────────────────────────────
TUNNEL_LOCAL_PORT="${TUNNEL_LOCAL_PORT:-15432}"
TUNNEL_REMOTE_TARGET="${TUNNEL_REMOTE_TARGET:-172.16.2.210:5432}"

head "SSH tunnel local:$TUNNEL_LOCAL_PORT → 252 → $TUNNEL_REMOTE_TARGET"
if ! lsof -tiTCP:${TUNNEL_LOCAL_PORT} -sTCP:LISTEN >/dev/null 2>&1; then
  log "starting tunnel..."
  ssh -f -N -L "${TUNNEL_LOCAL_PORT}:${TUNNEL_REMOTE_TARGET}" \
    -p 25022 root@115.29.212.252 \
    -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ExitOnForwardFailure=yes
  sleep 2
fi
if lsof -tiTCP:${TUNNEL_LOCAL_PORT} -sTCP:LISTEN >/dev/null 2>&1; then
  ok "tunnel up on :$TUNNEL_LOCAL_PORT"
else
  err "tunnel failed to start"
  exit 1
fi
trap 'ssh_pid=$(lsof -tiTCP:'"${TUNNEL_LOCAL_PORT}"' -sTCP:LISTEN 2>/dev/null); [[ -n "$ssh_pid" ]] && kill "$ssh_pid" 2>/dev/null || true' EXIT

# ── 4. Verify source reachable ───────────────────────────────────────────
SRC_TABLES=$(PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -h 127.0.0.1 -p "${TUNNEL_LOCAL_PORT}" -U llm_gateway -d llm_gateway -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null | tail -1 || echo 0)
ok "source (252 → llm_gateway) has $SRC_TABLES tables"

# ── 5. (optional) Verify-only mode ────────────────────────────────────────
if $VERIFY_ONLY; then
  head "verify table inventory (252 vs local)"
  PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" psql -h 127.0.0.1 -p "${TUNNEL_LOCAL_PORT}" -U llm_gateway -d llm_gateway -tAc \
    "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename" > /tmp/252.txt
  docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg \
    psql -U llm_gateway -d llm_gateway -tAc \
    "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename" > /tmp/local.txt
  log "tables in 252 but missing locally:"
  comm -23 /tmp/252.txt /tmp/local.txt
  log "local-only tables:"
  comm -13 /tmp/252.txt /tmp/local.txt
  exit 0
fi

# ── 6. Pre-sync pg_dump backup ────────────────────────────────────────────
BACKUP_DIR=$(lh_layout_vars | sed -n 's/^backups_dir=//p')
BACKUP_FILE="$BACKUP_DIR/local-pre-sync-$(date +%Y%m%d-%H%M%S).sql.gz"
if ! $BACKUP_ONLY; then
  head "pre-sync backup → $BACKUP_FILE"
  docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg \
    pg_dump -U llm_gateway -d llm_gateway --no-owner --no-privileges 2>/dev/null \
    | gzip -c > "$BACKUP_FILE"
  ok "local backup written ($(du -h "$BACKUP_FILE" | cut -f1))"
fi

# ── 7. Run pg-table-copy ─────────────────────────────────────────────────
head "pg-table-copy 252 → local (mode=$MODE)"
export PGOPTIONS='-c statement_timeout=0'

PG_ARGS=(--source configs/env-252.sh --target configs/env-local.sh)
case "$MODE" in
  schema) PG_ARGS+=(--schema-only) ;;
esac

if bash scripts/pg-table-copy.sh "${PG_ARGS[@]}" 2>&1 | tail -40; then
  ok "sync complete"
else
  err "sync failed (see tail above); local backup at $BACKUP_FILE"
  exit 1
fi

# ── 8. Final verification ─────────────────────────────────────────────────
head "post-sync verification"
LOCAL_AFTER=$(docker exec -e PGPASSWORD="$COMMON_PG_SUPERUSER_PASS" llm-gateway-pg \
  psql -U llm_gateway -d llm_gateway -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public';" 2>/dev/null | tail -1 || echo 0)
log "tables before: $LOCAL_TABLE_COUNT, after: $LOCAL_AFTER (source: $SRC_TABLES)"

if [[ "$LOCAL_AFTER" -ge "$SRC_TABLES" ]]; then
  ok "local table count matches or exceeds source"
else
  warn "local has FEWER tables than source — check missed migrations"
fi

log "next step: bash scripts/local-host-deploy.sh deploy"
