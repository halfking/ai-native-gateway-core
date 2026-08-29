#!/usr/bin/env bash
# dump-standard-models.sh — Re-export the standard-models-canonical snapshot
# from a live PostgreSQL DB into CSV / JSON / SQL.
#
# Usage:
#   ./dump-standard-models.sh                              # auto-detect from env
#   ./dump-standard-models.sh --label prod-2026-08-29      # custom label
#   ./dump-standard-models.sh --only csv                   # subset of formats
#   ./dump-standard-models.sh --target docs                # write to docs/ tree
#
# Outputs (default: $SCRIPT_DIR/snapshots/):
#   standard_models_<label>.csv
#   standard_models_<label>.json
#   standard_models_<label>.sql
#
# Inputs (env, in priority order):
#   LLM_GATEWAY_DATABASE_URL
#   DATABASE_URL
#   PG* (host/port/user/pass/db)
#
# The output files are *also* the canonical artifacts mirrored into
# docs/03-design/04-data-design/model-catalog/ at the end of every run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

LABEL="$(date -u +%Y%m%d)"
ONLY=""
TARGET_DIR="$SCRIPT_DIR/snapshots"

usage() {
  sed -n '2,15p' "$0"
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --label) LABEL="$2"; shift 2;;
    --only) ONLY="$2"; shift 2;;
    --target) TARGET_DIR="$2"; shift 2;;
    --docker) DOCKER_CONTAINER="$2"; shift 2;;
    -h|--help) usage 0;;
    *) echo "unknown arg: $1" >&2; usage 1;;
  esac
done

# Resolve DB connection. If --docker is set, run psql inside that container.
# This avoids needing host port mapping for local dev containers like llm-gateway-pg.
if [[ -n "${DOCKER_CONTAINER:-}" ]]; then
  PSQL_CMD=(docker exec -e PGPASSWORD="${PGPASSWORD:-}" "$DOCKER_CONTAINER" psql -h 127.0.0.1 -U "${PGUSER:-llm_gateway}" -d "${PGDATABASE:-llm_gateway}")
elif [[ -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
  PSQL_CMD=(psql "$LLM_GATEWAY_DATABASE_URL")
elif [[ -n "${DATABASE_URL:-}" ]]; then
  PSQL_CMD=(psql "$DATABASE_URL")
else
  : "${PGHOST:=localhost}"
  : "${PGPORT:=5432}"
  : "${PGUSER:=llm_gateway}"
  : "${PGDATABASE:=llm_gateway}"
  PSQL_CMD=(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE")
  [[ -n "${PGPASSWORD:-}" ]] && export PGPASSWORD
fi

mkdir -p "$TARGET_DIR"

CSV_OUT="$TARGET_DIR/standard_models_${LABEL}.csv"
JSON_OUT="$TARGET_DIR/standard_models_${LABEL}.json"
SQL_OUT="$TARGET_DIR/standard_models_${LABEL}.sql"

# Query: only "standard" rows.
#   - `seed` / `seed-standard-rollout` / `db` / `manual` / `standard`:
#     the canonical seed/manual entries.
#   - `migration-352/354/355/356`: standard-model bootstrap migrations
#     (canonical_name still tagged with the original migration source).
#   - `auto_discovered` / `discovery` / `provider_refresh`: include ONLY
#     canonical_name that 611 explicitly corrected, so we don't bake in
#     random freshly-discovered names that have not been audited.
# Filter on status='active' so historical / disabled rows never enter the snapshot.
COMMON_WHERE="status = 'active'
  AND (
    source IN ('seed','seed-standard-rollout','db','manual','standard',
               'migration-352','migration-354','migration-355','migration-356')
    OR canonical_name IN (
      -- 611-corrected grok-4.6 / kimi-k* / glm-5.3 / gemini-3.* / deepseek-v4
      'grok-4.6',
      'kimi-k3','kimi-k2.6','kimi-k2.7-code','kimi-k2.7-code-highspeed',
      'glm-5.3','glm-5','glm-5.1','glm-5.2',
      'gemini-3.6-flash','gemini-3.5-flash','gemini-3.5-flash-lite',
      'gemini-3.1-flash-lite','gemini-3.1-flash-lite-image',
      'gemini-3.1-pro-preview','gemini-3.1-flash-image','gemini-3-pro-image',
      'gemini-3-flash-preview','gemini-omni-flash',
      'deepseek-v3.2','deepseek-v3.2-exp','deepseek-v4-flash',
      'deepseek-v4-flash-vision-exp','deepseek-v4-pro','deepseek-v4-pro-cn',
      -- 612-corrected sensenova / sensechat (SenseNova 原厂 + 历史 seed)
      'sensechat-5','sensechat-5-thinking','sensechat-turbo','sensenova-xl',
      'sensenova-6.7-flash-lite','sensenova-6.8-flash-lite',
      'sensenova-u1-fast','sensenova-u1.5-lite'
    )
  )"

emit_csv() {
  # Use server-side COPY ... WITH CSV HEADER so quoting / escaping are
  # done by Postgres itself (handles embedded commas in display_name / notes
  # correctly). For psql CLI, the meta-command is \copy and bypasses any
  # lack of FILE permissions on the server side.
  "${PSQL_CMD[@]}" -v ON_ERROR_STOP=1 -c "
    COPY (
      SELECT
        canonical_name,
        family,
        parameters_b,
        modality,
        context_window,
        multimodal_caps,
        context_window_override,
        context_window_source,
        released_at,
        version_rank,
        complexity_ceiling,
        COALESCE(display_name, canonical_name) AS display_name,
        notes
      FROM models_canonical
      WHERE $COMMON_WHERE
      ORDER BY family, canonical_name
    ) TO STDOUT WITH CSV HEADER
  " > "$CSV_OUT"
  echo "  ✓ $CSV_OUT ($(wc -l < "$CSV_OUT") lines)"
}

emit_json() {
  "${PSQL_CMD[@]}" -v ON_ERROR_STOP=1 -tAc "
    SELECT json_agg(row_to_json(t) ORDER BY t.family, t.canonical_name)
    FROM (
      SELECT
        canonical_name,
        family,
        parameters_b,
        modality,
        context_window,
        multimodal_caps,
        context_window_override,
        context_window_source,
        released_at,
        version_rank,
        complexity_ceiling,
        min_complexity,
        cost_tier,
        strengths,
        display_name,
        source,
        notes
      FROM models_canonical
      WHERE $COMMON_WHERE
      ORDER BY family, canonical_name
    ) t
  " > "$JSON_OUT"
  echo "  ✓ $JSON_OUT ($(wc -c < "$JSON_OUT") bytes)"
}

emit_sql() {
  "${PSQL_CMD[@]}" -v ON_ERROR_STOP=1 -tAc "
    SELECT format(
      E'-- snapshot %s generated %s\n'
      'BEGIN;\n\n'
      'INSERT INTO public.models_canonical (\n'
      '    canonical_name, family, parameters_b, modality, context_window,\n'
      '    multimodal_caps, context_window_override, context_window_source,\n'
      '    status, source, released_at, version_rank, complexity_ceiling,\n'
      '    display_name, notes, created_at, updated_at)\n'
      'VALUES (%L, %L, %s, %L, %s,\n'
      '        ARRAY[%s]::text[], %s, %L,\n'
      '        ''active'', %L, %s::date, %s, %L,\n'
      '        %L, %L, NOW(), NOW())\n'
      'ON CONFLICT (canonical_name) DO UPDATE SET\n'
      '    family                 = EXCLUDED.family,\n'
      '    parameters_b           = EXCLUDED.parameters_b,\n'
      '    modality               = EXCLUDED.modality,\n'
      '    context_window         = EXCLUDED.context_window,\n'
      '    multimodal_caps        = EXCLUDED.multimodal_caps,\n'
      '    context_window_override = EXCLUDED.context_window_override,\n'
      '    context_window_source  = EXCLUDED.context_window_source,\n'
      '    released_at            = EXCLUDED.released_at,\n'
      '    version_rank           = EXCLUDED.version_rank,\n'
      '    complexity_ceiling     = EXCLUDED.complexity_ceiling,\n'
      '    display_name           = EXCLUDED.display_name,\n'
      '    notes                  = EXCLUDED.notes,\n'
      '    updated_at             = NOW()\n'
      ';\n\n',
      '$LABEL',
      CURRENT_TIMESTAMP::text,
      canonical_name, family,
      COALESCE(parameters_b::text, 'NULL'),
      modality,
      COALESCE(context_window::text, 'NULL'),
      COALESCE((SELECT string_agg(quote_literal(x), ',') FROM unnest(multimodal_caps) x), ''),
      COALESCE(context_window_override::text, 'NULL'),
      COALESCE(context_window_source, 'catalog'),
      source,
      COALESCE(released_at::text, 'NULL'),
      COALESCE(version_rank::text, 'NULL'),
      COALESCE(complexity_ceiling, 'NULL'),
      COALESCE(display_name, canonical_name),
      COALESCE(notes, '')
    )
    FROM models_canonical
    WHERE $COMMON_WHERE
    ORDER BY family, canonical_name
  " > "$SQL_OUT.tmp"

  {
    echo "-- ============================================================================"
    echo "-- Snapshot of standard models_canonical rows."
    echo "-- Generated by sql/scripts/dump-standard-models.sh on $LABEL."
    echo "--"
    echo "-- This file is the canonical seed for fresh-DB recovery. It uses"
    echo "-- ON CONFLICT (canonical_name) DO UPDATE so re-running it always wins over"
    echo "-- an existing row. For *additive* merge (do not touch operator-edited rows),"
    echo "-- replace the DO UPDATE block with DO NOTHING."
    echo "--"
    echo "-- Source filter:"
    echo "--   status='active' AND source IN ('seed','seed-standard-rollout',"
    echo "--                                 'db','manual','standard')"
    echo "-- ============================================================================"
    echo ""
    cat "$SQL_OUT.tmp"
    echo ""
    echo "COMMIT;"
    echo ""
    echo "NOTIFY auto_route_refresh, 'standard_models_snapshot_${LABEL}';"
  } > "$SQL_OUT"
  rm -f "$SQL_OUT.tmp"
  echo "  ✓ $SQL_OUT ($(wc -l < "$SQL_OUT") lines)"
}

want() {
  [[ -z "$ONLY" ]] && return 0
  [[ "$ONLY" == *"$1"* ]]
}

echo "[dump-standard-models] target=$TARGET_DIR  label=$LABEL  only=${ONLY:-all}"
want csv && emit_csv
want json && emit_json
want sql && emit_sql

# Mirror to docs tree if --target docs.
if [[ "$TARGET_DIR" == *docs* ]] || [[ "${MIRROR_TO_DOCS:-0}" == "1" ]]; then
  DOCS_DIR="$REPO_ROOT/docs/03-design/04-data-design/model-catalog"
  if [[ -d "$DOCS_DIR" ]]; then
    cp -f "$CSV_OUT"  "$DOCS_DIR/standard-models-canonical.csv"
    cp -f "$JSON_OUT" "$DOCS_DIR/standard-models-canonical.json"
    cp -f "$SQL_OUT"  "$DOCS_DIR/standard-models-canonical.sql"
    echo "[dump-standard-models] mirrored to $DOCS_DIR"
  fi
fi

echo "[dump-standard-models] done."
