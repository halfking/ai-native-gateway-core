#!/usr/bin/env bash
# recover-empty-response-misclassification.sh
#
# Reverse the credential / probe damage caused by the opus-4-8 false
# empty_response classification that was fixed in commit 2b2fc911.
#
# What the script does
# --------------------
# 1. Scan request_logs in [SINCE, UNTIL) for rows matching the bug fingerprint:
#      error_kind = 'empty_response'
#      AND failure_detail_code = 'zero_tokens_few_chunks'
#      AND upstream_finish_reason IS NULL
#    and group by (credential_id, outbound_model).
#
# 2. For every affected (credential_id, outbound_model) pair:
#      a. credential_model_bindings: clear available=false / unavailable_*=*
#         when unavailable_reason IN ('continuous_failure', 'auto_*')
#         (manual disable / model_probe_broken is PRESERVED).
#      b. model_probe_state: reset state from 'unavailable'/'suspicious'
#         back to 'available' and clear last_unavailable_reason / last_err_code
#         when last_err_code matches 'zero_tokens_few_chunks'.
#      c. request_logs: NOT TOUCHED. Audit integrity is sacred.
#
# 3. Every destructive change is preceded by a backup table
#    (_backup_*_YYYYMMDDHHMMSS) and a row in recovery_audit.
#
# Usage
# -----
#   bash scripts/recover-empty-response-misclassification.sh \
#     --since "2026-06-20" --until "2026-06-30" \
#     --pg-container r112_postgres \
#     --pg-user kxuser --pg-pass kxpass \
#     --pg-db llm_gateway \
#     --dry-run
#
#   # After review, drop --dry-run and re-run.
#   bash scripts/recover-empty-response-misclassification.sh \
#     --since "2026-06-20" --until "2026-06-30" \
#     --ticket HOTFIX-2b2fc911 \
#     --operator opencode-agent \
#     --reason "reverse opus-4-8 false empty_response classification"
#
# Safety properties
# -----------------
#   - Default mode is --dry-run.
#   - manual_disable / model_probe_broken / available=TRUE rows are never touched.
#   - request_logs is read-only; we do not UPDATE it.
#   - Re-running is idempotent: after a successful run, no rows match.
#   - Every write is recorded in recovery_audit with operator + ticket + reason.

set -euo pipefail

# ── Defaults ──
PG_CONTAINER="${PG_CONTAINER:-r112_postgres}"
PG_USER="${PG_USER:-kxuser}"
PG_PASS="${PG_PASS:-kxpass}"
PG_DB="${PG_DB:-llm_gateway}"
SINCE_DEFAULT="NOW() - INTERVAL '7 days'"
UNTIL_DEFAULT="NOW() + INTERVAL '1 day'"
DRY_RUN=1
TICKET=""
OPERATOR="${USER:-unknown}"
REASON=""

# ── Colors ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'
err()  { echo -e "${RED}✗ $*${NC}" >&2; }
ok()   { echo -e "${GREEN}✓ $*${NC}"; }
info() { echo -e "${BLUE}▶ $*${NC}"; }
warn() { echo -e "${YELLOW}! $*${NC}"; }

# ── Parse args ──
while [[ $# -gt 0 ]]; do
  case "$1" in
    --since)          SINCE="$2"; shift 2 ;;
    --until)          UNTIL="$2"; shift 2 ;;
    --pg-container)   PG_CONTAINER="$2"; shift 2 ;;
    --pg-user)        PG_USER="$2"; shift 2 ;;
    --pg-pass)        PG_PASS="$2"; shift 2 ;;
    --pg-db)          PG_DB="$2"; shift 2 ;;
    --dry-run)        DRY_RUN=1; shift ;;
    --execute)        DRY_RUN=0; shift ;;
    --ticket)         TICKET="$2"; shift 2 ;;
    --operator)       OPERATOR="$2"; shift 2 ;;
    --reason)         REASON="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,55p' "$0"; exit 0 ;;
    *)
      err "未知参数: $1"; exit 1 ;;
  esac
done

SINCE="${SINCE:-$SINCE_DEFAULT}"
UNTIL="${UNTIL:-$UNTIL_DEFAULT}"

if [[ "$DRY_RUN" -eq 0 ]]; then
  if [[ -z "$TICKET" || -z "$OPERATOR" || -z "$REASON" ]]; then
    err "--execute 模式必须同时给 --ticket --operator --reason"
    err "  （这与 configs/deployment/database-governance.yaml 政策一致）"
    exit 1
  fi
fi

# ── Helpers ──
run_sql() {
  PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
    "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAc "$1"
}

run_sql_file() {
  PGPASSWORD="$PG_PASS" docker exec -e PGPASSWORD="$PG_PASS" \
    -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -f -
}

# ── Pre-flight ──
info "目标: container=$PG_CONTAINER db=$PG_DB window=[$SINCE, $UNTIL)"
info "模式: $([[ $DRY_RUN -eq 1 ]] && echo "DRY-RUN（不改库）" || echo "EXECUTE（写库）")"
echo

if ! docker ps --format '{{.Names}}' | grep -q "^${PG_CONTAINER}\$"; then
  err "容器 $PG_CONTAINER 未运行"; exit 1
fi
if ! docker exec "$PG_CONTAINER" pg_isready -U "$PG_USER" -d "$PG_DB" >/dev/null 2>&1; then
  err "postgres $PG_DB 不可达"; exit 1
fi

# ── Schema sanity ──
for t in request_logs credential_model_bindings model_probe_state; do
  if ! run_sql "SELECT 1 FROM information_schema.tables WHERE table_name='$t' AND table_schema='public';" | grep -q 1; then
    err "缺少表: public.$t — 请先跑 migrations"
    exit 1
  fi
done
ok "schema 验证通过"

# ── Step 1: identify affected pairs ──
info "step 1: 扫描受污染的 (credential_id, outbound_model) 对"

AFFECTED_QUERY=$(cat <<SQL
SELECT
    rl.credential_id,
    rl.outbound_model,
    COUNT(*) AS empty_hits,
    MIN(rl.ts) AS first_hit,
    MAX(rl.ts) AS last_hit
FROM request_logs rl
WHERE rl.error_kind = 'empty_response'
  AND rl.failure_detail_code = 'zero_tokens_few_chunks'
  AND rl.upstream_finish_reason IS NULL
  AND rl.credential_id IS NOT NULL
  AND rl.ts >= $SINCE
  AND rl.ts <  $UNTIL
GROUP BY rl.credential_id, rl.outbound_model
ORDER BY empty_hits DESC;
SQL
)

echo "  affected pairs:"
run_sql "$AFFECTED_QUERY" | sed 's/^/    /'
PAIR_COUNT=$(run_sql "$AFFECTED_QUERY" | grep -c '|' || true)
echo "  → 共 $PAIR_COUNT 对"
echo

if [[ "$PAIR_COUNT" -eq 0 ]]; then
  ok "无受影响 (credential, model) 对，结束"
  exit 0
fi

# ── Step 2: preview cmb changes ──
info "step 2: 预览 credential_model_bindings 改动"

CMB_PREVIEW=$(cat <<SQL
SELECT id, credential_id, available, unavailable_reason, unavailable_at,
       unavailable_recover_at, consecutive_failures
FROM credential_model_bindings
WHERE (credential_id, provider_model_id) IN (
    SELECT rl.credential_id, pm.id
    FROM request_logs rl
    JOIN provider_models pm ON pm.raw_model_name = rl.outbound_model
                             AND pm.provider_id = (SELECT provider_id FROM credentials WHERE id = rl.credential_id)
    WHERE rl.error_kind = 'empty_response'
      AND rl.failure_detail_code = 'zero_tokens_few_chunks'
      AND rl.upstream_finish_reason IS NULL
      AND rl.credential_id IS NOT NULL
      AND rl.ts >= $SINCE
      AND rl.ts <  $UNTIL
)
  AND available = FALSE
  AND COALESCE(unavailable_reason, '') NOT LIKE 'manual%'
  AND unavailable_reason <> 'model_probe_broken';
SQL
)
run_sql "$CMB_PREVIEW" | sed 's/^/    /'
CMB_HITS=$(run_sql "$CMB_PREVIEW" | grep -c '|' || true)
echo "  → 将恢复 $CMB_HITS 个 cmb 行"
echo

info "step 2b: 预览 model_probe_state 改动"

PROBE_PREVIEW=$(cat <<SQL
SELECT credential_id, raw_model_name, state, last_unavailable_reason, last_err_code
FROM model_probe_state
WHERE (credential_id, raw_model_name) IN (
    SELECT rl.credential_id, rl.outbound_model
    FROM request_logs rl
    WHERE rl.error_kind = 'empty_response'
      AND rl.failure_detail_code = 'zero_tokens_few_chunks'
      AND rl.upstream_finish_reason IS NULL
      AND rl.credential_id IS NOT NULL
      AND rl.ts >= $SINCE
      AND rl.ts <  $UNTIL
)
  AND state IN ('unavailable', 'suspicious')
  AND (last_err_code = 'zero_tokens_few_chunks'
       OR last_unavailable_reason = 'zero_tokens_few_chunks');
SQL
)
run_sql "$PROBE_PREVIEW" | sed 's/^/    /'
PROBE_HITS=$(run_sql "$PROBE_PREVIEW" | grep -c '|' || true)
echo "  → 将恢复 $PROBE_HITS 个 probe 行"
echo

if [[ "$CMB_HITS" -eq 0 && "$PROBE_HITS" -eq 0 ]]; then
  ok "无 cmb/probe 行需要恢复（仅 request_logs 留有误判，审计不可改），结束"
  exit 0
fi

# ── Dry-run exit ──
if [[ "$DRY_RUN" -eq 1 ]]; then
  warn "DRY-RUN：上面只是预览，未做任何改动"
  warn "确认无误后：去掉 --dry-run，加 --ticket --operator --reason 重跑"
  exit 0
fi

# ── Step 3: backups ──
TS="$(date -u +%Y%m%d%H%M%S)"
CMB_BACKUP="_backup_cmb_${TS}"
PROBE_BACKUP="_backup_probe_${TS}"

info "step 3: 备份 → public.${CMB_BACKUP}, public.${PROBE_BACKUP}"

run_sql "DROP TABLE IF EXISTS public.${CMB_BACKUP};" >/dev/null
run_sql "CREATE TABLE public.${CMB_BACKUP} AS
         SELECT * FROM credential_model_bindings
         WHERE (credential_id, provider_model_id) IN (
             SELECT rl.credential_id, pm.id
             FROM request_logs rl
             JOIN provider_models pm ON pm.raw_model_name = rl.outbound_model
                                     AND pm.provider_id = (SELECT provider_id FROM credentials WHERE id = rl.credential_id)
             WHERE rl.error_kind = 'empty_response'
               AND rl.failure_detail_code = 'zero_tokens_few_chunks'
               AND rl.upstream_finish_reason IS NULL
               AND rl.credential_id IS NOT NULL
               AND rl.ts >= $SINCE
               AND rl.ts <  $UNTIL
         )
           AND available = FALSE
           AND COALESCE(unavailable_reason, '') NOT LIKE 'manual%'
           AND unavailable_reason <> 'model_probe_broken';"

run_sql "DROP TABLE IF EXISTS public.${PROBE_BACKUP};" >/dev/null
run_sql "CREATE TABLE public.${PROBE_BACKUP} AS
         SELECT * FROM model_probe_state
         WHERE (credential_id, raw_model_name) IN (
             SELECT rl.credential_id, rl.outbound_model
             FROM request_logs rl
             WHERE rl.error_kind = 'empty_response'
               AND rl.failure_detail_code = 'zero_tokens_few_chunks'
               AND rl.upstream_finish_reason IS NULL
               AND rl.credential_id IS NOT NULL
               AND rl.ts >= $SINCE
               AND rl.ts <  $UNTIL
         )
           AND state IN ('unavailable', 'suspicious')
           AND (last_err_code = 'zero_tokens_few_chunks'
                OR last_unavailable_reason = 'zero_tokens_few_chunks');"

ok "备份完成"

# ── Step 4: ensure audit table ──
run_sql "CREATE TABLE IF NOT EXISTS public.recovery_audit (
         id              BIGSERIAL PRIMARY KEY,
         ts              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
         script          TEXT NOT NULL,
         ticket          TEXT NOT NULL,
         operator        TEXT NOT NULL,
         reason          TEXT NOT NULL,
         cmb_backup      TEXT,
         probe_backup    TEXT,
         cmb_updated     INTEGER NOT NULL DEFAULT 0,
         probe_updated   INTEGER NOT NULL DEFAULT 0,
         dry_run         BOOLEAN NOT NULL DEFAULT FALSE,
         sql_window      TEXT NOT NULL
       );" >/dev/null

# ── Step 5: apply ──
info "step 4: 写 cmb + probe state"

cat <<SQL | run_sql_file
WITH affected AS (
    SELECT rl.credential_id, pm.id AS provider_model_id
    FROM request_logs rl
    JOIN provider_models pm ON pm.raw_model_name = rl.outbound_model
                            AND pm.provider_id = (SELECT provider_id FROM credentials WHERE id = rl.credential_id)
    WHERE rl.error_kind = 'empty_response'
      AND rl.failure_detail_code = 'zero_tokens_few_chunks'
      AND rl.upstream_finish_reason IS NULL
      AND rl.credential_id IS NOT NULL
      AND rl.ts >= $SINCE
      AND rl.ts <  $UNTIL
    GROUP BY rl.credential_id, pm.id
)
UPDATE credential_model_bindings cmb
SET available              = TRUE,
    unavailable_reason     = NULL,
    unavailable_at         = NULL,
    unavailable_recover_at = NULL,
    consecutive_failures   = 0,
    updated_at             = NOW()
FROM affected a
WHERE cmb.credential_id    = a.credential_id
  AND cmb.provider_model_id = a.provider_model_id
  AND cmb.available = FALSE
  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
  AND cmb.unavailable_reason <> 'model_probe_broken';
SQL

CMB_UPDATED=$(run_sql "SELECT count(*) FROM public.${CMB_BACKUP};")
ok "cmb updated: $CMB_UPDATED 行"

cat <<SQL | run_sql_file
WITH affected AS (
    SELECT rl.credential_id, rl.outbound_model
    FROM request_logs rl
    WHERE rl.error_kind = 'empty_response'
      AND rl.failure_detail_code = 'zero_tokens_few_chunks'
      AND rl.upstream_finish_reason IS NULL
      AND rl.credential_id IS NOT NULL
      AND rl.ts >= $SINCE
      AND rl.ts <  $UNTIL
    GROUP BY rl.credential_id, rl.outbound_model
)
UPDATE model_probe_state mps
SET state                  = 'available',
    consecutive_failures   = 0,
    last_status            = 'recovered_by_2b2fc911',
    last_unavailable_reason= NULL,
    last_err_code          = NULL,
    last_state_change_at   = NOW(),
    next_retry_at          = NOW(),
    marked_suspicious_at   = NULL,
    real_request_failure_count = 0
FROM affected a
WHERE mps.credential_id = a.credential_id
  AND mps.raw_model_name = a.outbound_model
  AND mps.state IN ('unavailable', 'suspicious')
  AND (mps.last_err_code = 'zero_tokens_few_chunks'
       OR mps.last_unavailable_reason = 'zero_tokens_few_chunks');
SQL

PROBE_UPDATED=$(run_sql "SELECT count(*) FROM public.${PROBE_BACKUP};")
ok "probe updated: $PROBE_UPDATED 行"

# ── Step 6: record audit ──
info "step 5: 记录 recovery_audit"

# 用单引号转义：把 SINCE/UNTIL 里的单引号变成两个单引号
SAFE_WINDOW_SINCE="${SINCE//\'/\'\'}"
SAFE_WINDOW_UNTIL="${UNTIL//\'/\'\'}"

cat <<SQL | run_sql_file
INSERT INTO public.recovery_audit
    (script, ticket, operator, reason, cmb_backup, probe_backup,
     cmb_updated, probe_updated, dry_run, sql_window)
VALUES (
    'recover-empty-response-misclassification.sh',
    '$TICKET',
    '$OPERATOR',
    \$\$$REASON\$\$,
    '${CMB_BACKUP}',
    '${PROBE_BACKUP}',
    $CMB_UPDATED,
    $PROBE_UPDATED,
    FALSE,
    '[${SAFE_WINDOW_SINCE}, ${SAFE_WINDOW_UNTIL})'
);
SQL

ok "recovery_audit 已记录"

echo
ok "恢复完成: cmb=$CMB_UPDATED probe=$PROBE_UPDATED"
echo
echo "  备份: public.${CMB_BACKUP}, public.${PROBE_BACKUP}"
echo "  回滚 (极端情况):"
echo "    -- cmb:"
echo "       UPDATE credential_model_bindings cmb SET"
echo "           available              = b.available,"
echo "           unavailable_reason     = b.unavailable_reason,"
echo "           unavailable_at         = b.unavailable_at,"
echo "           unavailable_recover_at = b.unavailable_recover_at,"
echo "           consecutive_failures   = b.consecutive_failures,"
echo "           updated_at             = b.updated_at"
echo "       FROM public.${CMB_BACKUP} b WHERE cmb.id = b.id;"
echo "    -- probe: 同样用 ${PROBE_BACKUP}"
echo "  审计查询:"
echo "    SELECT * FROM public.recovery_audit ORDER BY ts DESC LIMIT 5;"
