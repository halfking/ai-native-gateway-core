#!/usr/bin/env bash
# Export the complete record of rows whose recorded cost is already wrong.
#
# WHY THIS EXISTS
#   Three different things get conflated when you look at "cost accuracy":
#     1. "will we mis-record cost from now on"  -> bg/routing_health_checks.go
#        (14 checks; recorded_cost_is_negative is the one that guards the ledger
#        we wrote, not the writer)
#     2. "have we already recorded wrong cost"  -> THIS SCRIPT
#     3. "is the recorded number right"         -> nothing checks this
#   (1) went green when the write path started returning nil for a negative
#   total. That is correct and it is also useless for (2): the guard does not
#   rewrite history. This script is what (2) actually means.
#
# THE TRAP THIS SCRIPT IS BUILT AROUND
#   The damage record is FRAGMENTED across four tables with FOUR DIFFERENT
#   RETENTION WINDOWS, and the oldest damage survives only in the rollups where
#   it can no longer be traced back to a source row. An operator who "repairs
#   from request_logs" and stops will leave real money unaccounted for:
#     - usage_ledger holds negative-cost history for credentials that
#       request_logs no longer has any row for;
#     - stats_usage_daily holds negative-cost buckets whose source events are
#       older than anything still in request_logs or usage_ledger;
#     - usage_facts, the reconciliation's ground truth, has no row for events
#       the inbox already marked processed, so the cost-drift signal is blind
#       no matter what the sections below report.
#   Section 4, Section 6 and Section 7 exist specifically to make that visible
#   instead of letting it look finished.
#
# READ-ONLY
#   Every query is prefixed with `SET TRANSACTION READ ONLY` in the SAME -c
#   string, so psql runs each one in a single implicit read-only transaction.
#   A typo that turns a SELECT into an UPDATE fails instead of writing.
#
# USAGE
#   DATABASE_URL='postgres://...' ./scripts/export-negative-cost-rows.sh [OUT_DIR]
#   default OUT_DIR = ./negative-cost-export-<UTC timestamp>
#
# RE-RUN AFTER REPAIR
#   The point of a script over a document: after you decide whether to correct
#   history, run this again against the same database. The counts must go to
#   zero (or to a residue you can explain). A one-off paste cannot be re-run.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"
command -v psql >/dev/null 2>&1 || { printf 'error: psql is required\n' >&2; exit 1; }

OUT_DIR="${1:-negative-cost-export-$(date -u '+%Y%m%dT%H%M%SZ')}"
mkdir -p "$OUT_DIR"
umask 077
REPORT="$OUT_DIR/report.md"
CSV="$OUT_DIR/source-by-combo.csv"

# q <label> <sql>  -- read-only psql, CSV output, no headers.
q() {
  printf '\n-- %s\n' "$1" >&2
  psql -X -q -v ON_ERROR_STOP=1 --csv "$DATABASE_URL" \
    -c "SET TRANSACTION READ ONLY; $2"
}

# One value, for scalars.
scalar() {
  psql -X -q -t -A -v ON_ERROR_STOP=1 "$DATABASE_URL" \
    -c "SET TRANSACTION READ ONLY; $1"
}

# NOTE ON MODEL COLUMNS
#   request_logs has SIX model-ish columns: client_model, raw_model_name,
#   canonical_model, model_chosen, outbound_model, provider_model.
#   The grouping column below is outbound_model. raw_model_name is NOT a
#   synonym: on the real database it is empty on every negative-cost row, so
#   grouping by it yields fake "one credential, blank model" groups. A comment
#   in routing_health_checks.go once called this column "raw_model" and sent
#   the next reader at the unusable one.

{
printf '# Export: rows whose recorded cost is already wrong\n\n'
printf '**Generated**: %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')"

printf '\n## 0. Which database (asked of the server, never echoed from the URL)\n\n'
printf '```text\n'
scalar "SELECT 'database=' || current_database() || '  user=' || current_user || '  server=' || coalesce(inet_server_addr()::text,'local') || ':' || coalesce(inet_server_port()::text,'?')"
printf '```\n'

printf '\n## 1. What this export CANNOT compute, and why\n\n'
printf 'It cannot tell you what these rows *should* have cost. That needs a\n'
printf 'vendor price multiplied by tokens, and the baseline price SSOT is\n'
printf 'deliberately empty. Numbers shown below are what was recorded and how far\n'
printf 'it is from zero -- not what it should have been.\n\n'
printf '```text\n'
scalar "SELECT 'models_canonical rows with a baseline input price: ' || count(*) FROM public.models_canonical WHERE baseline_input_price_per_1m IS NOT NULL"
scalar "SELECT 'models_canonical rows total: ' || count(*) FROM public.models_canonical"
printf '```\n'

printf '\n## 2. Source rows: request_logs (the traceable tail)\n\n'
printf '```text\n'
q 'all-time vs the 30-day window the health check uses' "
SELECT count(*)                                            AS all_time_rows,
       round(sum(cost_usd)::numeric, 6)                    AS all_time_usd,
       count(*) FILTER (WHERE ts > now() - interval '30 days') AS rows_in_30d_window,
       round(sum(cost_usd) FILTER (WHERE ts > now() - interval '30 days')::numeric, 6)
                                                           AS usd_in_30d_window,
       min(ts)::date                                       AS oldest,
       max(ts)::date                                       AS newest
  FROM public.request_logs
 WHERE cost_usd < 0;"
printf '```\n'
printf '\n> The health check only looks 30 days back, while the historical damage\n'
printf '> starts earlier. As old rows age out, the alert goes quiet while the\n'
printf '> ledger stays wrong. Silence here is not repair.\n'

printf '\n## 3. Source rows grouped by (credential_id, outbound_model) -> %s\n\n' "$CSV"
q 'per-combination breakdown (also written to CSV)' "
SELECT r.credential_id,
       r.outbound_model,
       count(*)::int                                                             AS neg_rows,
       round(sum(r.cost_usd)::numeric, 6)                                        AS neg_usd,
       sum(COALESCE(r.prompt_tokens, 0))                                         AS prompt_tokens,
       sum(COALESCE(r.cache_read_tokens, 0))                                     AS cache_read_tokens,
       sum(COALESCE(r.cache_read_tokens, 0) - COALESCE(r.prompt_tokens, 0))      AS cache_exceeds_prompt_by,
       min(r.ts)::date                                                           AS first_day,
       max(r.ts)::date                                                           AS last_day
  FROM public.request_logs r
 WHERE r.cost_usd < 0
 GROUP BY r.credential_id, r.outbound_model
 ORDER BY neg_usd;"
cp_out="$(mktemp)"; trap 'rm -f "$cp_out"' EXIT
psql -X -q -v ON_ERROR_STOP=1 --csv "$DATABASE_URL" \
  -c "SET TRANSACTION READ ONLY;
      SELECT r.credential_id, r.outbound_model, count(*)::int AS neg_rows,
             round(sum(r.cost_usd)::numeric, 6) AS neg_usd,
             sum(COALESCE(r.prompt_tokens,0)) AS prompt_tokens,
             sum(COALESCE(r.cache_read_tokens,0)) AS cache_read_tokens,
             min(r.ts)::date AS first_day, max(r.ts)::date AS last_day
        FROM public.request_logs r
       WHERE r.cost_usd < 0
       GROUP BY r.credential_id, r.outbound_model ORDER BY neg_usd;" >"$cp_out"
mv "$cp_out" "$CSV"; chmod 0600 "$CSV"
trap - EXIT

printf '\n## 4. usage_ledger: WIDER than request_logs\n\n'
printf '```text\n'
q 'negative-cost ledger rows per credential' "
SELECT credential_id,
       count(*)::int                      AS neg_rows,
       round(sum(cost_usd)::numeric, 6)   AS neg_usd,
       min(ts)::date                      AS first_day,
       max(ts)::date                      AS last_day
  FROM public.usage_ledger
 WHERE cost_usd < 0
 GROUP BY 1
 ORDER BY 1;"
q 'credentials damaged in the ledger but with NO traceable row left in request_logs' "
WITH rl AS (SELECT DISTINCT credential_id FROM public.request_logs  WHERE cost_usd < 0),
     lg AS (SELECT DISTINCT credential_id FROM public.usage_ledger  WHERE cost_usd < 0)
SELECT lg.credential_id,
       (SELECT count(*)::int FROM public.usage_ledger u
         WHERE u.cost_usd < 0 AND u.credential_id = lg.credential_id) AS unrecoverable_rows,
       (SELECT round(sum(u.cost_usd)::numeric, 6) FROM public.usage_ledger u
         WHERE u.cost_usd < 0 AND u.credential_id = lg.credential_id) AS unrecoverable_usd
  FROM lg
  LEFT JOIN rl ON rl.credential_id = lg.credential_id
 WHERE rl.credential_id IS NULL
 ORDER BY 1;"
printf '```\n'
printf '\n> Empty result = every damaged ledger row still has its source row. A\n'
printf '> non-empty result is the dangerous one: those credentials were billed\n'
printf '> wrongly, the number is still in the ledger, and no row survives to\n'
printf '> recompute it from.\n'

printf '\n## 5. Rollups: the same money stored at several grains\n\n'
printf '```text\n'
q 'negative cost per rollup grain' "
SELECT 'stats_usage_daily' AS rollup, dimension_type,
       count(*)::int                    AS neg_rows,
       round(sum(cost_usd)::numeric, 6) AS neg_usd,
       min(day_utc)                     AS first_day,
       max(day_utc)                     AS last_day
  FROM public.stats_usage_daily
 WHERE cost_usd < 0
 GROUP BY 1, 2
UNION ALL
SELECT 'stats_usage_monthly', dimension_type,
       count(*)::int,
       round(sum(cost_usd)::numeric, 6),
       min(month_start),
       max(month_start)
  FROM public.stats_usage_monthly
 WHERE cost_usd < 0
 GROUP BY 1, 2
 ORDER BY 1, 2;"
printf '```\n'
printf '\n> Each `dimension_type` stores the SAME metric at a different grain\n'
printf '> (credential / person / provider / provider_model / tenant). Summing\n'
printf '> cost_usd across the table without filtering `dimension_type` counts\n'
printf '> every dollar once per grain. Compare the per-grain subtotals above: if\n'
printf '> they are identical, an unfiltered sum is that multiple.\n'
printf "> The product's own readers are already correct here\n"
printf "> (domains/stats/reconciliation.go pins dimension_type = 'provider_model';\n"
printf "> domains/stats/daily_monthly_rollup.go carries the grain through its\n"
printf '> GROUP BY), so this is a hazard for ad-hoc SQL and dashboards, not a\n'
printf '> live miscount in the application.\n'
printf "> Read the monthly rows before reusing a multiplier: the 'person' grain\n"
printf "> above has a different row count and subtotal from the other four, so\n"
printf "> the exact 5-way duplication holding for stats_usage_daily does NOT\n"
printf "> hold for stats_usage_monthly. Derive the number from this table; do\n"
printf "> not carry it over from the other one.\n"

printf '\n## 6. Retention gap: damage older than any surviving source row\n\n'
printf '```text\n'
q 'oldest surviving source row vs oldest damaged rollup bucket' "
SELECT (SELECT min(ts)::date  FROM public.request_logs WHERE cost_usd < 0) AS oldest_negative_request_log,
       (SELECT min(ts)::date  FROM public.usage_ledger WHERE cost_usd < 0) AS oldest_negative_ledger_row,
       (SELECT min(day_utc)   FROM public.stats_usage_daily WHERE cost_usd < 0) AS oldest_negative_daily_bucket,
       (SELECT min(month_start) FROM public.stats_usage_monthly WHERE cost_usd < 0) AS oldest_negative_month;"
q 'damaged rollup buckets whose whole window predates every surviving source row' "
SELECT dimension_type, count(*)::int AS buckets, min(day_utc) AS oldest, max(day_utc) AS newest,
       round(sum(cost_usd)::numeric, 6) AS usd
  FROM public.stats_usage_daily
 WHERE cost_usd < 0
   AND day_utc < (SELECT min(ts)::date FROM public.request_logs WHERE cost_usd < 0)
 GROUP BY 1 ORDER BY 1;"
printf '```\n'
printf '\n> A non-empty second table means: those buckets are wrong, their events\n'
printf '> are gone, and no recompute can recover them. They can only be restated\n'
printf '> or written off. Decide explicitly rather than discovering this from a\n'
printf '> total that does not add up.\n'

printf '\n## 7. Is cost-drift detection even able to see?\n\n'
printf 'The sections above answer "what is recorded wrongly". They cannot answer\n'
printf '"would the reconciliation have told me" -- and right now it cannot, so a\n'
printf 'clean reading above is not evidence that the costs are right.\n\n'
printf 'The reconciliation treats usage_facts as ground truth and\n'
printf 'stats_usage_daily as the projection. If an event is in the inbox but its\n'
printf 'fact row is missing, the projection row reconciles as a phantom and the\n'
printf 'diff backlog fills with artifacts that no one can tell from real drift.\n\n'
printf '```text\n'
q 'inbox events with no matching ground-truth row, per day' "
SELECT i.occurred_at::date AS day,
       count(*)::int                                            AS inbox_events,
       count(f.event_id)::int                                   AS facts_present,
       count(*) - count(f.event_id)::int                        AS facts_missing
  FROM public.stats_event_inbox i
  LEFT JOIN public.usage_facts f
         ON f.event_id = i.event_id AND f.occurred_at = i.occurred_at
 GROUP BY 1
HAVING count(*) - count(f.event_id) > 0
 ORDER BY 1;"
q 'inbox processing_status vs presence of the fact row' "
SELECT i.processing_status,
       count(*)::int                                AS events,
       count(f.event_id)::int                       AS facts_present,
       count(*) - count(f.event_id)::int            AS facts_missing
  FROM public.stats_event_inbox i
  LEFT JOIN public.usage_facts f
         ON f.event_id = i.event_id AND f.occurred_at = i.occurred_at
 GROUP BY 1
 ORDER BY 1;"
q 'cumulative row counters: inserts vs deletes per facts partition' "
SELECT s.relname AS relation, s.n_tup_ins, s.n_tup_del, s.n_live_tup,
       (s.n_tup_ins - s.n_tup_del) AS net_rows,
       s.last_autovacuum
  FROM pg_stat_user_tables s
 WHERE s.relname LIKE 'usage_facts%'
 ORDER BY s.relname;"
q 'every facts partition with its counter and autovacuum history' "
SELECT c.relname AS partition,
       COALESCE(s.n_tup_ins, 0)    AS ins,
       COALESCE(s.n_tup_del, 0)    AS del,
       COALESCE(s.n_live_tup, 0)  AS live,
       s.last_autovacuum,
       c.relfilenode
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
 WHERE i.inhparent = 'public.usage_facts'::regclass
 ORDER BY c.relname;"
q 'ground-truth window vs projection window' "
SELECT (SELECT min(occurred_at)::date FROM public.usage_facts)      AS facts_oldest,
       (SELECT max(occurred_at)::date FROM public.usage_facts)      AS facts_newest,
       (SELECT min(day_utc)         FROM public.stats_usage_daily)  AS projection_oldest,
       (SELECT max(day_utc)         FROM public.stats_usage_daily)  AS projection_newest;"
printf '```\n'
printf '\n> An empty first table means every inbox event has its fact row. A\n'
printf '> non-empty one with a high `facts_missing` share is the signature of a\n'
printf '> ground truth that was emptied rather than never written: the inbox\n'
printf '> keeps the events, the projection keeps the totals, and the facts that\n'
printf '> should sit between them do not exist. Compare `processing_status`\n'
printf '> against `facts_missing` -- events already marked processed but absent\n'
printf '> from the facts table were removed after the fact was written, not\n'
printf '> skipped before it.\n'
printf '>\n'
printf '> ATTRIBUTION LIMIT, stated plainly: this export can show the shape of\n'
printf '> the loss, never who caused it. Statement logging is off\n'
printf '> (log_statement=none), so the database keeps no record of the DELETE\n'
printf '> that did it. Anyone reading this and naming a cause is guessing.\n'
printf '>\n'
printf '> The counter table above is the strongest evidence available and it is\n'
printf '> worth reading before drawing any conclusion: a partition whose n_tup_ins\n'
printf '> and n_tup_del are near-equal with n_live_tup at 0 was WRITTEN AND THEN\n'
printf '> EMPTIED, while a partition that was never written shows n_tup_ins = 0.\n'
printf '> Those two look identical from a row count alone -- which is exactly why\n'
printf '> the counters are here: SELECT count(*) cannot tell "never written" from\n'
printf '> "written then deleted", and only the first of those is benign.\n'
printf '>\n'
printf '> LAST_AUTOVACUUM is the closest thing to a deletion timestamp that\n'
printf '> survives without statement logging: autovacuum runs on a partition only\n'
printf '> after the deletes leave dead tuples, so a cluster of adjacent dates here\n'
printf '> is one cleanup burst, not one delete per row. Read the dates as "the\n'
printf '> delete happened shortly BEFORE these", not as "these are the deletes".\n'
printf '> A partition with empty ins AND empty del AND no autovacuum history has\n'
printf '> been replaced rather than merely emptied -- it is a new relfilenode,\n'
printf '> which is a different event again from "written then deleted".\n'
printf '>\n'
printf '> WHAT IS ALREADY RULED OUT (checked 2026-10-05; do not redo this search):\n'
printf '>   · EventWriter.persist sync branch is ATOMIC -- dedup, inbox, fact insert\n'
printf '>     and the processing_status flip share one transaction, so a row cannot\n'
printf '>     be marked processed without its fact. Async projection is OFF in this\n'
printf '>     deployment (log says "retaining synchronous projection"), so the\n'
printf '>     consumer-claim path is not in play either.\n'
printf '>   · Migration 761 documents the old "pending with processed_at set" shape\n'
printf '>     and backfilled it to processed; those facts existed at that time.\n'
printf '>   · ensure_usage_facts_daily_partition (migration 750) moves rows with a\n'
printf '>     symmetric "WITH moved AS (DELETE ... RETURNING) INSERT ... SELECT" in\n'
printf '>     one statement, so creating the daily partition is not losing rows.\n'
printf '>   · The nightly hot-table cron does not touch it: hotPromoteTableMap\n'
printf '>     lists 19 *_hot tables and usage_facts is not among them.\n'
printf '>   · Host crontab and launchd: the only llm-gateway job is a weekly LOG\n'
printf '>     cleaner; the only daily db cleanup trims an unrelated SQLite db.\n'
printf '>   · sql/fixes/2026-10-02-db-storage-reclaim.sql DOES target this table: it\n'
printf '>     TRUNCATEs usage_facts_default and DROPs past EMPTY date partitions\n'
printf '>     behind a real emptiness probe. That accounts for the default partition\n'
printf '>     and for the replaced empty partitions, but it cannot account for\n'
printf '>     write-then-delete on NON-empty partitions -- it contains no DELETE.\n'
printf '>\n'
printf '> ATTRIBUTED (2026-10-05, from pg_stat_statements in THIS database, whose\n'
printf '> oldest entry is 2026-10-02): the write-then-delete pattern is a real-DB\n'
printf '> test cleanup, not a retention policy. The two statements land one second\n'
printf "> apart and match domains/reportrollup/realdb_e2e_test.go's cleanup pair\n"
printf '> (DELETE FROM report_snapshots; DELETE FROM usage_facts):\n'
printf '>\n'
printf '>     6 x DELETE FROM report_snapshots   2026-10-05 06:31:29.87\n'
printf '>     3 x DELETE FROM usage_facts        2026-10-05 06:31:30.10\n'
printf '>     autovacuum on usage_facts_20261004/5   06:32:34  (their dead tuples)\n'
printf '>\n'
printf '> That test carries guardDestructiveE2E, which refuses any database whose\n'
printf '> name is not scratch/e2e UNLESS REPORT_E2E_ALLOW_DESTRUCTIVE=1 is set.\n'
printf '> "llm_gateway" is neither, so the opt-in must have been set. The sibling\n'
printf '> admin/user_usage_stats_live_test.go has NO such guard at all -- it is a\n'
printf '> "live-DB regression" test whose cleanup DELETEs by event_id prefix; its\n'
printf '> prefix is scoped enough to leave real facts alone (verified: 47 real rows\n'
printf '> survive, 0 synthetic rows remain), but it will happily run against a\n'
printf '> data-bearing database given TEST_DATABASE_URL.\n'
printf '>\n'
printf '> If the wipes recur, pg_stat_statements is the place to look FIRST -- it\n'
printf '> needs no log_statement change and it keeps a rolling record of statement\n'
printf '> text, call counts and execution windows. Reach for log_statement only\n'
printf '> when pg_stat_statements is off or has already evicted the entry.\n'

printf '\n## 8. Re-running this after a repair\n\n'
printf 'Same database, same command. What must move to zero:\n\n'
printf '```text\n'
printf '  Section 2  all_time_rows and all_time_usd   (source rows)\n'
printf '  Section 4  unrecoverable_rows               (rows you cannot recompute)\n'
printf '  Section 5  neg_rows per grain               (rollups; expect a rollup\n'
printf '                                                rebuild, they do not\n'
printf '                                                self-heal from a source\n'
printf '                                                UPDATE)\n'
printf '  Section 6  buckets older than the source tail\n'
printf '  Section 7  facts_missing per day  (ground truth restored; this is the\n'
printf '             one that tells you a re-run is now able to SEE a cost\n'
printf '             drift at all, which none of the sections above do)\n'
printf '```\n'
printf '\nIf a count does not reach zero, this export says how many rows and\n'
printf 'how many dollars are still wrong -- not that the repair succeeded.\n'
} >"$REPORT"

chmod 0600 "$REPORT"
printf 'wrote %s\nwrote %s\n(both 0600)\n' "$REPORT" "$CSV"
