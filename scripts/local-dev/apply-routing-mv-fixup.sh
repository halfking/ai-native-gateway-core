#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# File:          scripts/local-dev/apply-routing-mv-fixup.sh
# Purpose:       Apply the 252 → local schema fixup for the routing-analytics
#                materialized views and their `columnar_insert_only_parents`
#                helper. Idempotent: every object uses CREATE OR REPLACE / IF
#                NOT EXISTS, so a re-run is a no-op.
#
# Why this exists (2026-09-01):
#   pg-table-copy.sh's plain dump + import path was observed to drop these
#   three objects on local even after a clean 252 → local schema import:
#     - public.routing_analytics_7d        (MATERIALIZED VIEW; 252 created
#                                          it in the same migration 632)
#     - public.routing_audit_summary_7d   (MATERIALIZED VIEW; ditto)
#     - public.columnar_insert_only_parents()  (helper referenced by 252's
#                                          `enforce_columnar_trigger` event
#                                          trigger)
#   `verify-db-consistency.sh` (structure audit) flagged all three as 252-only
#   drift. `verify-db-data-consistency.sh` showed the 264 ordinary tables
#   were identical, so re-running `pg-table-copy.sh --replace-data` is
#   wasteful and risky; this script is the minimal, idempotent equivalent.
#
# Source of truth: 252's pg_dump output captured during the 2026-09-01 sync.
# DDL is hand-trimmed (search_path guard stripped — see
# .agents/skills/db-sync-252-local/SKILL.md pitfall 4.4 — and `\restrict` /
# `\unrestrict` markers and `SET default_*` lines removed to avoid breaking
# the columnar event trigger on import).
#
# Policy: NO automation of `ALTER ROLE/USER`, NO password changes, NO
# DROP/CREATE of the cluster. CREATE OR REPLACE / IF NOT EXISTS only.
#
# Status:        active
# Changelog:
#   2026-09-01  v1.2  Comment accuracy: PHASE 8.5 runs whenever the target is a
#                      local docker container (not dry-run/data-only), even if
#                      earlier schema/data import phases failed (idempotency
#                      makes that safe); precondition is docker exec reachability,
#                      not a TCP check on 5432.
#   2026-09-01  v1.1  + PG_FIXUP_DB env (default llm_gateway) so pg-table-copy.sh
#                      PHASE 8.5 can invoke this script with a non-default DB
#                      name. Behavior unchanged when env is unset. Auto-called
#                      by pg-table-copy.sh during sync finalization — manual use
#                      is still supported (idempotent).
#   2026-09-01  v1.0  Initial — 3 objects extracted from 252 dump
# -----------------------------------------------------------------------------
# Usage:
#   bash scripts/local-dev/apply-routing-mv-fixup.sh
#   PG_FIXUP_DB=mydb bash scripts/local-dev/apply-routing-mv-fixup.sh
#   # Auto-invoked by scripts/pg-table-copy.sh PHASE 8.5 (no manual step needed
#   # after a sync). Manual invocation is still safe (idempotent).
# -----------------------------------------------------------------------------
# Preconditions:
#   - llm-gateway-pg container running (executed via docker exec; no direct
#     TCP reachability on 127.0.0.1:5432 is required)
#   - docker available on PATH
# -----------------------------------------------------------------------------

set -euo pipefail

CONTAINER="${PG_FIXUP_CONTAINER:-llm-gateway-pg}"
PG_USER="${PG_FIXUP_USER:-llm_gateway}"
PG_DB="${PG_FIXUP_DB:-llm_gateway}"

ENVS_LOADER="$HOME/workspace/ai-native-tools/envs/loader.sh"
if [[ -f "$ENVS_LOADER" ]]; then
  # shellcheck disable=SC1090
  source "$ENVS_LOADER" --project llm-gateway-go >/dev/null 2>&1 || true
  PGPASSWORD="${PG_FIXUP_PASS:-${COMMON_PG_SUPERUSER_PASS:-}}"
else
  PGPASSWORD="${PG_FIXUP_PASS:-}"
fi

if [[ -z "$PGPASSWORD" ]]; then
  echo "ERROR: cannot determine PGPASSWORD. Set PG_FIXUP_PASS or load envs." >&2
  exit 1
fi

# Sanity: container reachable
if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  echo "ERROR: container '$CONTAINER' is not running" >&2
  exit 1
fi

# Pre-flight: log current state so a re-run is visibly a no-op
echo "▶ pre-fixup state:"
docker exec -e PGPASSWORD="$PGPASSWORD" "$CONTAINER" \
  psql -U "$PG_USER" -d "$PG_DB" -tAc \
  "SELECT
     (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
       WHERE n.nspname='public' AND c.relkind='m'
         AND c.relname IN ('routing_analytics_7d','routing_audit_summary_7d'))
     AS matviews_present,
     (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
       WHERE n.nspname='public' AND p.proname='columnar_insert_only_parents')
     AS fn_present;"

# SQL body
SQL=$(cat <<'SQL_EOF'
BEGIN;

-- ============================================================================
-- Routing analytics matviews (migration 632 / 2026-08-31) +
-- columnar_insert_only_parents helper.
-- DDL extracted from 252's pg_dump on 2026-09-01; trimmed to skip pg_dump's
-- search_path='' guard (breaks 252/local's columnar event trigger) and the
-- \restrict / \unrestrict markers / SET default_* statements.
-- Idempotent: every CREATE has IF NOT EXISTS or OR REPLACE.
-- ============================================================================

-- ── Helper function ───────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION public.columnar_insert_only_parents()
 RETURNS text[]
 LANGUAGE sql
 STABLE
AS $function$
    SELECT ARRAY['routing_decision_log'];
$function$;

-- ── Matview: routing_analytics_7d ─────────────────────────────────────────
CREATE MATERIALIZED VIEW IF NOT EXISTS public.routing_analytics_7d AS
 SELECT date_trunc('hour'::text, ts) AS time_bucket,
    COALESCE(NULLIF(task_type, ''::text),
        CASE
            WHEN is_auto_request THEN 'unknown'::text
            ELSE '__specified__'::text
        END) AS effective_task_type,
    COALESCE(NULLIF(outbound_model, ''::text), client_model) AS effective_model,
    COALESCE(NULLIF(work_type, ''::text), 'unknown'::text) AS effective_work_type,
    COALESCE(provider_id, ( SELECT cr.provider_id
           FROM public.credentials cr
          WHERE (cr.id = request_logs_with_current_month_without_customer_id.credential_id)
         LIMIT 1)) AS effective_provider_id,
    COALESCE(is_auto_request, false) AS is_auto_request,
    tenant_id,
    count(*) AS request_count,
    count(*) FILTER (WHERE success) AS success_count,
    count(*) FILTER (WHERE (is_auto_request = true)) AS auto_request_count,
    count(*) FILTER (WHERE (is_auto_request IS NOT TRUE)) AS specified_request_count,
    percentile_cont((0.5)::double precision) WITHIN GROUP (ORDER BY ((latency_ms)::double precision)) AS p50_latency_ms,
    percentile_cont((0.95)::double precision) WITHIN GROUP (ORDER BY ((latency_ms)::double precision)) AS p95_latency_ms,
    percentile_cont((0.99)::double precision) WITHIN GROUP (ORDER BY ((latency_ms)::double precision)) AS p99_latency_ms,
    COALESCE(sum(cost_usd), (0)::numeric) AS total_cost_usd,
    now() AS refreshed_at
   FROM public.request_logs_with_current_month_without_customer_id
  WHERE ((ts >= (now() - '7 days'::interval))
         AND (COALESCE(origin_stage, ''::text) NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'))
         AND (COALESCE(task_type, ''::text) <> 'probe_triggered'::text)
         AND (COALESCE(request_id, ''::text) NOT LIKE 'probe-'::text || '%')
         AND ((is_auto_request = true) OR ((is_auto_request IS NOT TRUE) AND (client_model IS NOT NULL) AND (client_model <> ''::text)))
         AND (COALESCE(NULLIF(outbound_model, ''::text), client_model) IS NOT NULL))
  GROUP BY (date_trunc('hour'::text, ts)),
           COALESCE(NULLIF(task_type, ''::text),
               CASE WHEN is_auto_request THEN 'unknown'::text ELSE '__specified__'::text END),
           COALESCE(NULLIF(outbound_model, ''::text), client_model),
           COALESCE(NULLIF(work_type, ''::text), 'unknown'::text),
           COALESCE(provider_id, ( SELECT cr.provider_id
                  FROM public.credentials cr
                 WHERE (cr.id = request_logs_with_current_month_without_customer_id.credential_id)
                LIMIT 1)),
           is_auto_request,
           tenant_id
  WITH NO DATA;

COMMENT ON MATERIALIZED VIEW public.routing_analytics_7d
  IS 'Pre-aggregated 7-day routing analytics for /api/admin/auto-route/analytics/* endpoints. Refreshed every 10 minutes by bg.MaterializedViewRefresher. Created by migration 632 (2026-08-31).';

-- ── Matview: routing_audit_summary_7d ────────────────────────────────────
CREATE MATERIALIZED VIEW IF NOT EXISTS public.routing_audit_summary_7d AS
 SELECT tenant_id,
    count(*) AS total_requests,
    count(*) FILTER (WHERE success) AS success_count,
    count(*) FILTER (WHERE (is_auto_request = true)) AS auto_request_count,
    count(*) FILTER (WHERE (is_auto_request IS NOT TRUE)) AS specified_request_count,
    now() AS refreshed_at
   FROM public.request_logs_with_current_month_without_customer_id
  WHERE ((ts >= (now() - '7 days'::interval))
         AND (COALESCE(origin_stage, ''::text) NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual'))
         AND (COALESCE(task_type, ''::text) <> 'probe_triggered'::text)
         AND (COALESCE(request_id, ''::text) NOT LIKE 'probe-'::text || '%')
         AND ((is_auto_request = true) OR ((is_auto_request IS NOT TRUE) AND (client_model IS NOT NULL) AND (client_model <> ''::text))))
  GROUP BY tenant_id
  WITH NO DATA;

COMMENT ON MATERIALIZED VIEW public.routing_audit_summary_7d
  IS 'High-level audit summary for /api/admin/auto-route/audit endpoint. Refreshed every 10 minutes by bg.MaterializedViewRefresher. Created by migration 632 (2026-08-31).';

-- ── Indexes ───────────────────────────────────────────────────────────────
-- Routing_analytics_7d indexes (idempotent via IF NOT EXISTS).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='routing_analytics_7d_task_model_idx') THEN
    CREATE INDEX routing_analytics_7d_task_model_idx
      ON public.routing_analytics_7d USING btree (effective_task_type, effective_model);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='routing_analytics_7d_tenant_idx') THEN
    CREATE INDEX routing_analytics_7d_tenant_idx
      ON public.routing_analytics_7d USING btree (tenant_id) WHERE (tenant_id IS NOT NULL);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='routing_analytics_7d_time_idx') THEN
    CREATE INDEX routing_analytics_7d_time_idx
      ON public.routing_analytics_7d USING btree (time_bucket DESC);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='routing_analytics_7d_ukey') THEN
    CREATE UNIQUE INDEX routing_analytics_7d_ukey
      ON public.routing_analytics_7d USING btree
      (time_bucket, effective_task_type, effective_model, effective_work_type, effective_provider_id, is_auto_request, tenant_id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='routing_audit_summary_7d_ukey') THEN
    CREATE UNIQUE INDEX routing_audit_summary_7d_ukey
      ON public.routing_audit_summary_7d USING btree (tenant_id);
  END IF;
END$$;

COMMIT;
SQL_EOF
)

# Apply
echo "▶ applying fixup to $CONTAINER..."
if ! docker exec -i -e PGPASSWORD="$PGPASSWORD" "$CONTAINER" \
    psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAq <<<"$SQL"; then
  echo "ERROR: fixup apply failed (see above)" >&2
  exit 1
fi

# Post-flight: verify the canonical columns and required indexes exist.
echo "▶ post-fixup state:"
POSTCHECK_SQL="SELECT CASE WHEN
  (SELECT count(*) FROM pg_matviews WHERE schemaname='public' AND matviewname IN ('routing_analytics_7d','routing_audit_summary_7d')) = 2
  AND (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='columnar_insert_only_parents') >= 1
  AND (SELECT count(*) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
       WHERE n.nspname='public' AND c.relname='routing_audit_summary_7d' AND a.attname IN ('auto_request_count','specified_request_count') AND NOT a.attisdropped) = 2
  AND (SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('routing_analytics_7d_ukey','routing_audit_summary_7d_ukey')) = 2
  THEN 'ok' ELSE 'invalid' END;"
POSTCHECK=$(docker exec -e PGPASSWORD="$PGPASSWORD" "$CONTAINER" \
  psql -U "$PG_USER" -d "$PG_DB" -tAc "$POSTCHECK_SQL")
if [[ "$POSTCHECK" != "ok" ]]; then
  echo "ERROR: routing MV post-flight catalog check failed" >&2
  exit 1
fi

echo "✓ fixup applied and canonical routing MV contract verified"
