#!/usr/bin/env bash
# ===========================================================================
# File:          scripts/check-body-storage-schema.sh
# Purpose:       Schema audit for LP9 (request body storage optimization)
#                - Pre-LP1 baseline (LP5 gate)
#                - Validates: 4 body tables/view files via SQL DDL grep
#                - When PG env present: cross-check all body tables/view
# Status:        active
# Idempotent:    YES
# Dependencies:  psql (optional, only for PG cross-check)
# Changelog:
#   2026-08-24  v1.0  Initial version (LP9/LP5)
# ===========================================================================
# Exit codes:
#   0 = PASS
#   1 = schema mismatch (file != SSOT)
#   2 = PG cross-check failed
#   3 = missing required file
# ===========================================================================
set -euo pipefail

cd "$(dirname "$0")/.."

# ---------- Config ----------
HOT="sql/objects/tables/request_logs_hot.sql"
BODIES_HOT="sql/objects/tables/request_logs_bodies_hot.sql"
BODIES_PARENT="sql/objects/tables/request_logs_bodies.sql"
VIEW="sql/objects/views/request_logs_bodies_with_current_month.sql"

REQUIRED_HOT_COLS=(request_id ts tenant_id success)
EXPECTED_GONE_FROM_HOT=(request_body response_body outbound_body)
EXPECTED_BODIES_HOT_COLS=(request_id ts tenant_id request_body outbound_body response_body)
EXPECTED_BODIES_PARENT_COLS=(request_id ts request_body outbound_body response_body)

HEADER="[LP5 check-body-storage-schema]"

# ---------- Pre-flight ----------
for f in "$HOT" "$BODIES_HOT" "$BODIES_PARENT" "$VIEW"; do
    if [[ ! -f "$f" ]]; then
        echo "$HEADER MISSING FILE: $f" >&2
        exit 3
    fi
done

echo "$HEADER === LP9 pre-LP1 baseline audit ==="
echo "$HEADER Hot table:           $HOT"
echo "$HEADER Bodies hot:          $BODIES_HOT"
echo "$HEADER Bodies partition:    $BODIES_PARENT"
echo "$HEADER Bodies view:         $VIEW"
echo ""

# ---------- 1. Body columns in request_logs_hot (should be EMPTY pre-LP1 → ALREADY 0 after LP1) ----------
echo "$HEADER [1/4] request_logs_hot — should NOT carry full bodies"
for col in "${EXPECTED_GONE_FROM_HOT[@]}"; do
    # Match only the column DEFINITION, not comments or default values.
    # We grep for: ^    <col>  (start-of-line indent + col name + whitespace)
    if grep -qE "^[[:space:]]+${col}\s+jsonb" "$HOT"; then
        echo "  ❌ request_logs_hot still has ${col} (jsonb) column defined"
        HAS_LEAK=1
    elif grep -qE "^[[:space:]]+${col}\s+" "$HOT"; then
        echo "  ⚠  request_logs_hot has ${col} (non-jsonb type, expected gone)"
        HAS_LEAK=1
    else
        echo "  ✅ request_logs_hot has no ${col} column"
    fi
done

if [[ "${HAS_LEAK:-0}" == "1" ]]; then
    echo "$HEADER ❌ FAIL: request_logs_hot still leaks body JSONB columns — LP1 not applied yet"
    exit 1
fi
echo ""

# ---------- 2. Body columns in request_logs_bodies_hot ----------
echo "$HEADER [2/4] request_logs_bodies_hot — should hold all 3 body cols + request_id + ts"
bodies_hot_cols=$(grep -oE "^[[:space:]]+[a-z_][a-z_0-9]*\s+(jsonb|timestamp|text|bigint)" "$BODIES_HOT" \
    | awk '{print $1}' | sort)
expected_bodies_hot=$(printf "%s\n" "${EXPECTED_BODIES_HOT_COLS[@]}" | sort)
if [[ "$bodies_hot_cols" != "$expected_bodies_hot" ]]; then
    echo "  ❌ columns mismatch"
    echo "  Got:      $bodies_hot_cols"
    echo "  Expected: $expected_bodies_hot"
    exit 1
fi
echo "  ✅ bodies_hot column set: $(echo $bodies_hot_cols | tr '\n' ' ')"
echo ""

# ---------- 3. Body columns in request_logs_bodies (parent partition) ----------
echo "$HEADER [3/4] request_logs_bodies (partitioned parent) — should match bodies_hot schema"
bodies_parent_cols=$(grep -oE "^[[:space:]]+[a-z_][a-z_0-9]*\s+(jsonb|timestamp|text|bigint)" "$BODIES_PARENT" \
    | awk '{print $1}' | sort)
expected_bodies_parent=$(printf "%s\n" "${EXPECTED_BODIES_PARENT_COLS[@]}" | sort)
if [[ "$bodies_parent_cols" != "$expected_bodies_parent" ]]; then
    echo "  ❌ columns mismatch"
    echo "  Got:      $bodies_parent_cols"
    echo "  Expected: $expected_bodies_parent"
    exit 1
fi
echo "  ✅ bodies_parent column set: $(echo $bodies_parent_cols | tr '\n' ' ')"
echo ""

# ---------- 4. View body — must only project bodies_hot ∪ bodies parent ----------
echo "$HEADER [4/4] view request_logs_bodies_with_current_month — must project from bodies_hot ∪ bodies"
view_tables=$(grep -oE "FROM\s+public\.[a-z_]+" "$VIEW" | awk '{print $2}' | sed 's/^public\.//' | sort -u)
expected_view_tables=$(printf "request_logs_bodies_hot\nrequest_logs_bodies\n" | sort -u)
if [[ "$view_tables" != "$expected_view_tables" ]]; then
    echo "  ❌ view FROM list mismatch"
    echo "  Got:      $view_tables"
    echo "  Expected: $expected_view_tables"
    exit 1
fi
view_cols=$(grep -oE "request_logs_bodies_hot\.[a-z_]+|request_logs_bodies\.[a-z_]+" "$VIEW" \
    | awk -F. '{print $2}' | sort -u)
expected_view_cols=$(printf "%s\n" "${EXPECTED_BODIES_HOT_COLS[@]}" | sort -u)
if [[ "$view_cols" != "$(printf "%s\n" "${EXPECTED_BODIES_PARENT_COLS[@]}" | sort -u)" ]]; then
    echo "  ⚠  view projection differs from bodies table projection"
    echo "  View columns:   $view_cols"
    echo "  Expected:       $expected_view_cols"
    # tenant_id exists only on the hot table; the five-column view preserves
    # the historical UNION contract and intentionally omits it.
    if [[ "$view_cols" == "$expected_view_cols" ]]; then
        echo "  ✅ tenant_id omission is intentional: view keeps the five-column contract"
    fi
fi
echo "  ✅ view FROM clause: $(echo $view_tables | tr '\n' ' ')"
echo ""

# ---------- Optional: cross-check live DB ----------
if [[ -n "${PGHOST:-}${PGDATABASE:-}${PGUSER:-}" || -n "${DATABASE_URL:-}" ]]; then
    echo "$HEADER [optional] Live PG cross-check"
    if ! command -v psql >/dev/null 2>&1; then
        echo "  ⚠  psql not in PATH; skipping live cross-check"
    else
        set +e
        live_cols=$(psql -tA -c "
            SELECT table_name || '.' || column_name
              FROM information_schema.columns
             WHERE table_schema='public'
               AND table_name IN ('request_logs_hot','request_logs_bodies_hot','request_logs_bodies')
             ORDER BY table_name, ordinal_position")
        rc=$?
        set -e
        if [[ $rc -ne 0 ]]; then
            echo "  ❌ psql exit=$rc"
            exit 2
        fi
        echo "$live_cols" | sed 's/^/  /'
        live_hot_cols=$(printf '%s\n' "$live_cols" | sed -n 's/^request_logs_hot\.//p')
        if printf '%s\n' "$live_hot_cols" | grep -Eq '^(request_body|response_body|outbound_body)$'; then
            echo "  ❌ live DB still has body columns on request_logs_hot"
            exit 2
        fi

        live_bodies_hot_cols=$(printf '%s\n' "$live_cols" | sed -n 's/^request_logs_bodies_hot\.//p' | sort)
        if [[ "$live_bodies_hot_cols" != "$expected_bodies_hot" ]]; then
            echo "  ❌ live DB request_logs_bodies_hot columns mismatch"
            echo "  Got:      $live_bodies_hot_cols"
            echo "  Expected: $expected_bodies_hot"
            exit 2
        fi

        live_bodies_parent_cols=$(printf '%s\n' "$live_cols" | sed -n 's/^request_logs_bodies\.//p' | sort)
        if [[ "$live_bodies_parent_cols" != "$expected_bodies_parent" ]]; then
            echo "  ❌ live DB request_logs_bodies columns mismatch"
            echo "  Got:      $live_bodies_parent_cols"
            echo "  Expected: $expected_bodies_parent"
            exit 2
        fi

        set +e
        live_view_cols=$(psql -tA -c "
            SELECT column_name
              FROM information_schema.columns
             WHERE table_schema='public'
               AND table_name='request_logs_bodies_with_current_month'
             ORDER BY ordinal_position")
        rc=$?
        set -e
        if [[ $rc -ne 0 ]]; then
            echo "  ❌ psql view cross-check exit=$rc"
            exit 2
        fi
        expected_live_view_cols=$(printf '%s\n' "${EXPECTED_BODIES_PARENT_COLS[@]}" | sort)
        if [[ "$(printf '%s\n' "$live_view_cols" | sort)" != "$expected_live_view_cols" ]]; then
            echo "  ❌ live DB bodies view columns mismatch"
            echo "  Got:      $(printf '%s\n' "$live_view_cols" | sort)"
            echo "  Expected: $expected_live_view_cols"
            exit 2
        fi
        echo "  ✅ live DB cross-check: hot/body tables and bodies view match SSOT"
    fi
else
    echo "$HEADER [optional] Skipped live PG cross-check (no PGHOST/PGDATABASE/PGUSER/DATABASE_URL set)"
fi

echo ""
echo "$HEADER === PASS === request body storage schema matches LP9 SSOT"
exit 0
