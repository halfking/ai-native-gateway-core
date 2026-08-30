#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/verify-db-consistency.sh
# Purpose:       Deep structure consistency check between 252 (test) and the
#                local docker container (llm-gateway-pg): table inventory,
#                column signatures (ALL tables), views (name+definition hash),
#                index logical shape, constraints, sequences, functions
#                (name + argument identity + body hash). Optionally
#                reconcile schema drift by pushing local feature-table DDL
#                to 252.
# Status:        active
# Changelog:
#   2026-08-31  v1.2  + functions dimension (name|arg-identity|md5(prosrc)) to
#                      close the blind spot that migrations like 628 (function
#                      DDL) were never compared by the 6-object audit.
#   2026-08-31  v1.1  Full-object audit: + views / indexes / constraints /
#                      sequences comparison (v1.0 compared tables+columns only
#                      and missed the drift found by the 2026-08-31 audit:
#                      request_logs body columns, 610 constraint, stale views).
#   2026-08-31  v1.0  Initial version (table inventory + structure diff,
#                      gated --reconcile mode).
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/local-dev/verify-db-consistency.sh            # full verify
#   bash scripts/local-dev/verify-db-consistency.sh --verify
#
#   # Push local feature-table DDL to 252 (gated, requires --yes):
#   bash scripts/local-dev/verify-db-consistency.sh --reconcile agent_discovery proxy_nodes --yes
# -----------------------------------------------------------------------------
# Preconditions:
#   - envs loader at ~/workspace/ai-native-tools/envs/loader.sh
#   - configs/env-252.sh present (tunnel target + creds)
#   - llm-gateway-pg docker container running locally
#   - SSH access to 252 configured (ssh-config-auth or injected SSH_PASS_252)
# -----------------------------------------------------------------------------

set -euo pipefail

# ── Colors ────────────────────────────────────────────────────────────────
G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; C='\033[0;36m'; N='\033[0m'
ok()    { echo -e "${G}✓${N} $*"; }
info()  { echo -e "${Y}▶${N} $*"; }
warn()  { echo -e "${Y}⚠${N} $*"; }
err()   { echo -e "${R}✗${N} $*" >&2; }
phase() { echo -e "\n${B}════════════════════════════════════════════════════════════${N}"; echo -e "${B}  $*${N}"; echo -e "${B}════════════════════════════════════════════════════════════${N}"; }

# ── Config ────────────────────────────────────────────────────────────────
ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
PROJECT="llm-gateway-go"
LOCAL_CONTAINER="llm-gateway-pg"
LOCAL_DB="llm_gateway"
LOCAL_USER="llm_gateway"
WORK_DIR="/tmp/verify-db-consistency"
MODE="verify"
RECONCILE_TABLES=()
RECONCILE_YES=false

# Hot-table patterns: 252-only objects matching these are EXPECTED drift
# (runtime monthly partitions / hot windows), not a real inconsistency.
HOT_PATTERNS='*_hot *_2026_* *_2027_* *_archived *_archive'

mkdir -p "$WORK_DIR"

# ── Parse args ────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --verify)    MODE="verify"; shift ;;
    --reconcile) MODE="reconcile"; shift
      while [[ $# -gt 0 && "$1" != --* ]]; do RECONCILE_TABLES+=("$1"); shift; done ;;
    --yes)       RECONCILE_YES=true; shift ;;
    -h|--help)
      grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) err "unknown arg: $1"; exit 1 ;;
  esac
done

# ── Load envs (credentials + tunnel target) ───────────────────────────────
load_envs() {
  if [[ ! -f "$ENVS_LOADER" ]]; then err "envs loader not found: $ENVS_LOADER"; exit 1; fi
  # shellcheck disable=SC1090
  source "$ENVS_LOADER" --project "$PROJECT" 2>/dev/null
  export PG_PASS_252="${COMMON_PG_SUPERUSER_PASS:?COMMON_PG_SUPERUSER_PASS not loaded}"
  export SSH_PASS_252="${SSH_PASS_252:-ssh-config-auth}"
  # shellcheck disable=SC1090
  source configs/env-252.sh
  # Local docker container uses the SAME superuser password as 252 (synced by
  # recreate-llm-gateway-pg.sh); no need for configs/env-local.sh (PG_PASS_LOCAL).
}

# ── Query helpers ──────────────────────────────────────────────────────────
p252() { PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway -tAc "$1"; }
ploc() { docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" psql -U "$LOCAL_USER" -d "$LOCAL_DB" -tAc "$1"; }

# ── Tunnel management ──────────────────────────────────────────────────────
TUNNEL_PID=""
ensure_tunnel() {
  if p252 'SELECT 1' >/dev/null 2>&1; then
    info "reusing existing tunnel on localhost:$TUNNEL_LOCAL_PORT"
    return 0
  fi
  ssh -f -N -L "$TUNNEL_LOCAL_PORT:$TUNNEL_REMOTE_TARGET" 252 2>&1
  sleep 3
  if ! p252 'SELECT 1' >/dev/null 2>&1; then
    err "cannot reach 252 via tunnel (localhost:$TUNNEL_LOCAL_PORT)"; exit 1
  fi
  TUNNEL_PID=$(lsof -tiTCP:"$TUNNEL_LOCAL_PORT" -sTCP:LISTEN 2>/dev/null | head -1)
  ok "tunnel up (localhost:$TUNNEL_LOCAL_PORT -> $TUNNEL_REMOTE_TARGET)"
}

teardown_tunnel() {
  if [[ -n "$TUNNEL_PID" ]]; then
    kill "$TUNNEL_PID" 2>/dev/null && info "tunnel torn down ($TUNNEL_PID)"
  fi
}

# ── Classification helpers ─────────────────────────────────────────────────
is_hot_pattern() {
  local name="$1" p
  for p in $HOT_PATTERNS; do
    case "$name" in
      $p) return 0 ;;
    esac
  done
  return 1
}

# Compare one signature pair. $1=section label, $2=252 file, $3=local file,
# $4=max diff lines (default 25). Sets DRIFT=1 on any difference. Always
# returns 0 — callers check $DRIFT (a non-zero return would abort under set -e).
DRIFT=0
compare_sig() {
  local label="$1" f252="$2" floc="$3" maxlines="${4:-25}"
  if diff -q "$f252" "$floc" >/dev/null 2>&1; then
    ok "$label: identical ($(wc -l < "$f252" | tr -d ' ') entries)"
    return 0
  fi
  err "$label: DIFFERS —"
  diff "$f252" "$floc" | head -n "$maxlines" || true
  local n
  n=$(diff "$f252" "$floc" | grep -cE '^[<>]' || true)
  err "$label: ${n:-0} differing lines total (full diff: diff $f252 $floc)"
  DRIFT=1
  return 0
}

# ── Signature SQL (shared by both sides; identical text = comparable) ─────
# Scope: public schema; exclude local `_`-prefixed cleanup objects and
# runtime monthly partitions (`*_2026_08` style).
# NOTE: columns are sorted by NAME, not ordinal position — PG cannot reorder
# columns in place, and the two instances legitimately differ in physical
# order for identical logical schemas (e.g. request_logs after migration 573).
COLS_SQL="SELECT table_name||'|'||column_name||'|'||data_type||'|'||is_nullable||'|'||coalesce(column_default,'') FROM information_schema.columns WHERE table_schema='public' AND table_name NOT LIKE '\_%' AND table_name !~ '_202[0-9]_[0-9]+$' ORDER BY table_name, column_name"

VIEWS_SQL="SELECT viewname||'|'||md5(definition) FROM pg_views WHERE schemaname='public' AND viewname NOT LIKE '\_%' ORDER BY viewname"

# Index LOGICAL shape (table|index|unique|primary|key columns|has_predicate).
# Raw pg_get_indexdef() is NOT used: PG renders ANY(ARRAY[...]) casts
# differently on the two instances for logically identical partial indexes.
IDXS_SQL="SELECT t.relname||'|'||ix.indexrelid::regclass::text||'|'||ix.indisunique||'|'||ix.indisprimary||'|'||coalesce((SELECT string_agg(a.attname,',' ORDER BY k.ord) FROM unnest(ix.indkey) WITH ORDINALITY AS k(attnum,ord) JOIN pg_attribute a ON a.attrelid=ix.indrelid AND a.attnum=k.attnum),'')||'|'||(ix.indpred IS NOT NULL) FROM pg_index ix JOIN pg_class t ON t.oid=ix.indrelid WHERE t.relnamespace='public'::regnamespace AND t.relname NOT LIKE '\_%' AND t.relname !~ '_202[0-9]_[0-9]+$' ORDER BY 1"

CONS_SQL="SELECT conrelid::regclass::text||'|'||conname||'|'||contype::text||'|'||pg_get_constraintdef(oid) FROM pg_constraint WHERE connamespace='public'::regnamespace AND conrelid::regclass::text NOT LIKE '\_%' AND conrelid::regclass::text !~ '_202[0-9]_[0-9]+$' ORDER BY 1"

# Sequences, excluding those owned by `_` cleanup tables (rename keeps
# sequence ownership, so legacy orphans must not count as drift).
SEQS_SQL="SELECT s.relname FROM pg_class s LEFT JOIN pg_depend d ON d.objid=s.oid AND d.classid='pg_class'::regclass AND d.objsubid=0 AND d.deptype='a' LEFT JOIN pg_class tbl ON tbl.oid=d.refobjid WHERE s.relkind='S' AND s.relnamespace='public'::regnamespace AND (tbl.relname IS NULL OR (tbl.relname NOT LIKE '\_%' AND tbl.relname !~ '_202[0-9]_[0-9]+$')) ORDER BY 1"

# Functions / procedures (prokind f|p): name + argument identity + body hash.
# Closes the audit blind spot — migrations like 628 ship function DDL that the
# 6-object audit (tables/cols/views/indexes/constraints/sequences) never
# compared. Extension-owned functions (citus/columnar/vchord in public) are
# identical on both instances so they are not drift; we keep them in scope to
# surface a real extension-version gap if one ever appears.
FUNCS_SQL="SELECT p.proname||'|'||pg_get_function_identity_arguments(p.oid)||'|'||md5(p.prosrc) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind IN ('f','p') AND p.proname NOT LIKE '\_%' ORDER BY 1"

# ── Verify mode ────────────────────────────────────────────────────────────
do_verify() {
  phase "A. Table inventory diff (252 vs local)"
  local expected=0
  local only252 onlylocal
  only252=$(comm -23 "$WORK_DIR/tables252.txt" "$WORK_DIR/tableslocal.txt")
  onlylocal=$(comm -13 "$WORK_DIR/tables252.txt" "$WORK_DIR/tableslocal.txt")

  # Process substitution (not a pipe) so counter updates persist in this shell.
  if [[ -n "$only252" ]]; then
    while IFS= read -r t; do
      if is_hot_pattern "$t"; then warn "252-only (EXPECTED hot/partition): $t"; expected=$((expected+1));
      else err "252-only (REAL DRIFT): $t"; DRIFT=1; fi
    done < <(printf '%s\n' "$only252")
  else
    ok "no 252-only tables"
  fi

  if [[ -z "$onlylocal" ]]; then
    ok "no local-only tables — inventories match"
  else
    while IFS= read -r t; do
      [[ -z "$t" ]] && continue
      err "local-only (REAL DRIFT): $t"; DRIFT=1
    done < <(printf '%s\n' "$onlylocal")
  fi

  phase "B–F. Full object structure comparison"
  p252 "$COLS_SQL"  > "$WORK_DIR/cols252.txt";  ploc "$COLS_SQL"  > "$WORK_DIR/colslocal.txt"
  p252 "$VIEWS_SQL" > "$WORK_DIR/views252.txt"; ploc "$VIEWS_SQL" > "$WORK_DIR/viewslocal.txt"
  p252 "$IDXS_SQL"  > "$WORK_DIR/idx252.txt";   ploc "$IDXS_SQL"  > "$WORK_DIR/idxlocal.txt"
  p252 "$CONS_SQL"  > "$WORK_DIR/cons252.txt";  ploc "$CONS_SQL"  > "$WORK_DIR/conslocal.txt"
  p252 "$SEQS_SQL"  > "$WORK_DIR/seqs252.txt";  ploc "$SEQS_SQL"  > "$WORK_DIR/seqslocal.txt"
  p252 "$FUNCS_SQL" > "$WORK_DIR/funcs252.txt"; ploc "$FUNCS_SQL" > "$WORK_DIR/funcslocal.txt"

  # Normalize PG's two textual renderings of `= ANY(ARRAY[...])` in CHECK
  # constraints — logically identical constraints render differently on the
  # two instances (cosmetic variance, 32 occurrences found 2026-08-31).
  normalize_cons() {
    sed -E \
      -e "s/\(ARRAY\[([^]]*)\]\)::text\[\]/ARRAY[\1]/g" \
      -e "s/::character varying//g" \
      -e "s/\('([^']*)'\)::text/'\1'/g" \
      -e "s/::text//g" "$1"
  }
  normalize_cons "$WORK_DIR/cons252.txt"  > "$WORK_DIR/cons252.norm"
  normalize_cons "$WORK_DIR/conslocal.txt" > "$WORK_DIR/conslocal.norm"

  compare_sig "B. Columns (all tables: name|type|null|default)" "$WORK_DIR/cols252.txt"  "$WORK_DIR/colslocal.txt"
  compare_sig "C. Views (name|md5(definition))"                 "$WORK_DIR/views252.txt" "$WORK_DIR/viewslocal.txt"
  compare_sig "D. Index logical shape (table|idx|uniq|pk|cols|pred)" "$WORK_DIR/idx252.txt" "$WORK_DIR/idxlocal.txt" 40
  compare_sig "E. Constraints (table|name|type|def, normalized)" "$WORK_DIR/cons252.norm"  "$WORK_DIR/conslocal.norm" 40
  compare_sig "F. Sequences (excluding _-owned)"                "$WORK_DIR/seqs252.txt"  "$WORK_DIR/seqslocal.txt"
  compare_sig "G. Functions (name|args|md5(prosrc))"            "$WORK_DIR/funcs252.txt" "$WORK_DIR/funcslocal.txt"

  phase "Verdict"
  if [[ "$DRIFT" -eq 0 ]]; then
    ok "CONSISTENT — tables/columns/views/indexes/constraints/sequences/functions all match"
    ok "(expected hot/partition differences: $expected)"
    return 0
  else
    err "INCONSISTENT — see sections flagged with ✗ above"
    return 1
  fi
}

# ── Reconcile mode: push local feature-table DDL to 252 ────────────────────
do_reconcile() {
  if [[ ${#RECONCILE_TABLES[@]} -eq 0 ]]; then err "--reconcile needs at least one table name"; exit 1; fi
  if ! $RECONCILE_YES; then
    err "refusing to apply DDL without --yes (this modifies 252). Re-run with --yes to confirm."
    exit 1
  fi
  phase "Reconcile: push local DDL -> 252 for: ${RECONCILE_TABLES[*]}"

  local ddl="$WORK_DIR/reconcile.sql"
  : > "$ddl"
  for t in "${RECONCILE_TABLES[@]}"; do
    if [[ "$(ploc "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='$t'")" != "1" ]]; then
      err "table not found locally: $t — skipping"; continue
    fi
    # Loop per table: pg_dump -t needs a repeated -t per table, and passing a
    # bash array into `docker exec ... pg_dump` collapses under zsh.
    docker exec -e PGPASSWORD="$PG_PASS_252" "$LOCAL_CONTAINER" \
      pg_dump -U "$LOCAL_USER" -d "$LOCAL_DB" --schema-only --clean --if-exists --no-owner --no-privileges -t "$t" \
      >> "$ddl" 2>"$WORK_DIR/reconcile.dumperr"
  done

  # CRITICAL: pg_dump emits `SET search_path=''` for security. On 252 this
  # breaks the enforce_columnar_trigger event trigger (unqualified function
  # lookup fails). Everything is already schema-qualified, so strip it.
  grep -vE "set_config\('search_path', '', false\)" "$ddl" > "$ddl.fixed"

  info "applying $(wc -l < "$ddl.fixed" | tr -d ' ') lines of DDL to 252 (localhost:$TUNNEL_LOCAL_PORT)..."
  if PGPASSWORD="$PG_PASS_252" psql -h localhost -p "$TUNNEL_LOCAL_PORT" -U llm_gateway -d llm_gateway \
       -v ON_ERROR_STOP=1 -f "$ddl.fixed" > "$WORK_DIR/reconcile.out" 2>"$WORK_DIR/reconcile.psqlerr"; then
    ok "DDL applied to 252"
  else
    err "DDL apply FAILED — see $WORK_DIR/reconcile.psqlerr"; cat "$WORK_DIR/reconcile.psqlerr"; exit 1
  fi

  info "re-verifying consistency..."
  pull_inventories
  do_verify
}

pull_inventories() {
  p252 "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename" > "$WORK_DIR/tables252.txt"
  ploc "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT LIKE '\_%' ESCAPE '\\' ORDER BY tablename" > "$WORK_DIR/tableslocal.txt"
  ok "252 tables: $(wc -l < "$WORK_DIR/tables252.txt") | local tables (excl _): $(wc -l < "$WORK_DIR/tableslocal.txt")"
}

# ── Main ───────────────────────────────────────────────────────────────────
trap teardown_tunnel EXIT
load_envs
ensure_tunnel
pull_inventories

case "$MODE" in
  verify)    do_verify ;;
  reconcile) do_reconcile ;;
esac
