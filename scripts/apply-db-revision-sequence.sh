#!/usr/bin/env bash
# Apply the small, ordered database repair set shipped with the gateway.
# This is intentionally not a historical migration replay.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${DATABASE_URL:?DATABASE_URL must be explicitly set}"

if command -v psql >/dev/null 2>&1; then
  psql_query() { psql -X -v ON_ERROR_STOP=1 -Atqc "$1" "$DATABASE_URL"; }
  psql_file() { psql -X -v ON_ERROR_STOP=1 "$DATABASE_URL" -f "$1"; }
else
  : "${LLM_GATEWAY_PG_CONTAINER:?psql is unavailable; set LLM_GATEWAY_PG_CONTAINER for Docker execution}"
  : "${LLM_GATEWAY_PG_PASSWORD:?set LLM_GATEWAY_PG_PASSWORD for Docker execution}"
  psql_query() {
    docker exec -e PGPASSWORD="$LLM_GATEWAY_PG_PASSWORD" "$LLM_GATEWAY_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" \
      -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" -Atqc "$1"
  }
  psql_file() {
    docker exec -i -e PGPASSWORD="$LLM_GATEWAY_PG_PASSWORD" "$LLM_GATEWAY_PG_CONTAINER" \
      psql -X -v ON_ERROR_STOP=1 -U "${LLM_GATEWAY_PG_USER:-llm_gateway}" \
      -d "${LLM_GATEWAY_PG_DATABASE:-llm_gateway}" -f - < "$1"
  }
fi

# Do not mutate a database whose required base relation is absent. In
# particular, never rename/drop a same-named table from another product.
#
# 2026-09-07: shared PG instances (llm-gateway-pg with memora/kxmemory as
# sibling products) routinely reach this gate with public.session_summaries
# ABSENT — neither the gateway snapshot (skipped because the DB already
# holds 700+ sibling tables) nor memora (which only overwrites the table
# when it exists with a minimal shape) creates it. Instead of refusing,
# bootstrap the canonical table via the dedicated 677 migration and let the
# rest of the sequence proceed. After bootstrap the gate re-classifies the
# state as 'canonical' so downstream migrations see a stable relation.
base_state=$(psql_query "
SELECT CASE
  WHEN to_regclass('public.session_summaries') IS NULL THEN 'missing'
  WHEN EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_summaries'
      AND column_name='session_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='session_summaries'
      AND column_name='session_key'
  ) THEN 'reconcile'
  ELSE 'canonical'
END")
if [[ "$base_state" == missing ]]; then
  printf 'database schema state: missing (public.session_summaries absent; will bootstrap via 677)\n'
else
  printf 'database schema state: %s\n' "$base_state"
fi

sequence_name="session-summary-and-integrity-2026-09"
# deploy-local.sh already serializes deployments with its build lock. Markers
# are recorded PER FILE ("<sequence>:<basename>") so appending a new migration
# to an already-applied sequence still runs it — a single sequence-wide marker
# silently skipped 656 after it was added to the list (2026-09-05 PG log audit:
# ensure/promote auto_route_selections functions missing on every boot). Each
# underlying migration is idempotent, so re-running is safe.
psql_query "CREATE TABLE IF NOT EXISTS public.gateway_db_revision_sequences (sequence_name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"

# Fixed order: 655 restores the canonical session_summaries columns; 560
# supplies the tenant uniqueness guard; 572 must replace the bounded token
# ratio before 563 performs its backfill; 563/564 then restore the hot trigger
# and safe aggregate backfill; 644/645 finish the independent repairs; 650
# adds the treatment-attribution columns that 656's promote/ensure functions
# and all-view require on the parent; 656 creates the auto_route_selections
# hot heap (no db.go ensure covers it).
#
# 2026-09-05 migration-completion audit (P1): 651/652/653/654 had no Go
# ensure equivalent in db/db.go and were not covered by any track on
# upgraded deployments (only the installer's fresh-install path ran them,
# installer/internal/dbinit/runner.go). 647 and 649 do have Go equivalents
# (db.ensureGoalClientSignalSchema at db/db.go:350 and
# db.ensureRoutingAnalyticsMaterializedViews + routingAnalyticsMVSQL at
# db/db.go:1081/937 respectively), so they stay out of this list.
# 651 (request_logs_hot contract columns + minute-aggregator index),
# 652 (system_monitor_fallback_queue table) and 653/654
# (archive_credential_model_index canonical tuple + DETACH/DROP rewrite)
# are mutually independent of the session_summaries/auto_route_selections
# chain above, so they append safely after 656. 653 precedes 654 because
# 654 supersedes 653's DELETE-based function body with the columnar-safe
# DETACH PARTITION + DROP path (matching the installer's runner order);
# all four are idempotent (IF NOT EXISTS / DROP FUNCTION IF EXISTS +
# CREATE OR REPLACE).
#
# 2026-09-05 PG log audit (deploy gap): V371 (deploy/sql/migrations —
# supplier_errors_hot + monthly columnar partitions + promote/ensure
# functions + unified view + supplier_error_stats) and the
# credential_model_weekly_peak unique bucket index were shipped as repo
# files but no deployment track applied them on upgraded databases, so the
# gateway logged 42P01/42883 on every insert / rollup / partition cron
# (supplier_errors family) and 42P10 on the weekly peak rollup
# (ON CONFLICT target missing). Both are idempotent and safe to re-run;
# they append after 654 like the other independent repairs.
#
# 2026-09-05 evening audit (22003 numeric field overflow): the sequence
# order runs 572 (token-ratio fix) BEFORE 563, but both CREATE OR REPLACE
# the same update_session_summary() — 563's older body re-clobbered the
# fix one second after 572 applied, so every ≥10K-token request failed its
# request-log persist with 22003 (insert + RowsAffected==0 update
# fallback). 661 re-asserts the fixed body AFTER 563 and validates the
# function source; 563's file was also corrected for fresh installs.
# (2026-09-05 evening renumber: the first-cut 657/658 prefixes collided
# with origin/main's 657_durable_llm_tasks / 658_auto_route_structured_features,
# so the weekly-peak repair moved to 660 and the reassert to 661.)
files=(
  # 2026-09-07 deploy-gap audit (P0): on shared PG instances where neither
  # the gateway snapshot nor memora created public.session_summaries, the
  # base_state=='missing' gate previously refused the whole sequence. 677
  # bootstraps the canonical table with CREATE TABLE IF NOT EXISTS (no-op
  # when memora already covers it) and MUST run before 655, which is the
  # first migration that ALTERs the table — otherwise 655 fails with
  # 'relation public.session_summaries does not exist' and the deploy aborts
  # before any of the integrity / hot-heap / treatment-attribution chain
  # below gets a chance to run. Idempotent; safe to re-run.
  "$ROOT_DIR/sql/migrations/startup/677_session_summaries_canonical_bootstrap.sql"
  "$ROOT_DIR/sql/migrations/startup/655_session_summaries_schema_reconcile.sql"
  "$ROOT_DIR/sql/migrations/startup/560_session_summaries_tenant_uniqueness.sql"
  "$ROOT_DIR/sql/migrations/startup/572_session_summary_large_token_ratio.sql"
  "$ROOT_DIR/sql/migrations/startup/606_session_summaries_agent_expert_tags.sql"
  "$ROOT_DIR/sql/migrations/startup/563_session_summary_trigger_on_hot.sql"
  "$ROOT_DIR/sql/migrations/startup/564_session_summary_backfill_safe.sql"
  "$ROOT_DIR/sql/migrations/startup/644_tuning_views_selfcheck_and_candidate_failure_cache.sql"
  "$ROOT_DIR/sql/migrations/startup/645_session_bodies_hot_request_unique_repair.sql"
  "$ROOT_DIR/sql/migrations/startup/650_auto_route_selection_treatment_attribution.sql"
  "$ROOT_DIR/sql/migrations/startup/656_auto_route_selections_hot.sql"
  # 2026-09-06 deploy-gap audit: 658 (structured feature columns on the
  # auto_route_selections PARENT) has only its hot-table half mirrored by the
  # Go ensure (db.ensureAutoRouteSelectionsHotSchema adds columns to _hot, then
  # builds auto_route_selections_all referencing the parent's 658 columns), so
  # on upgraded databases every boot fails 42703 (detected_language) and the
  # deploy aborts at gateway migrate. Must run after 656 (hot heap + the
  # promote function it redefines) and before the Go ensure chain.
  "$ROOT_DIR/sql/migrations/startup/658_auto_route_structured_features.sql"
  "$ROOT_DIR/sql/migrations/startup/659_legacy_promote_atomic_cte.sql"
  "$ROOT_DIR/sql/migrations/startup/651_provider_quality_hot_rollup.sql"
  "$ROOT_DIR/sql/migrations/startup/652_system_monitor_fallback_queue.sql"
  "$ROOT_DIR/sql/migrations/startup/653_archive_credential_model_index_canonical_return.sql"
  "$ROOT_DIR/sql/migrations/startup/654_archive_credential_model_index_detach_drop.sql"
  "$ROOT_DIR/sql/migrations/startup/660_credential_model_weekly_peak_unique.sql"
  "$ROOT_DIR/sql/migrations/startup/661_session_summary_token_ratio_reassert.sql"
  # 2026-09-06 PG log audit: bg/feature_stats_worker.go computes daily feature
  # distributions and dedup rates for AUTO route ML training quality monitoring,
  # but 662 (feature_distribution_stats + dedup_stats tables) had no deployment
  # track and was missing on upgraded databases, causing 42P01 on every hourly
  # worker tick. Must run before the bg worker starts.
  "$ROOT_DIR/sql/migrations/startup/662_feature_distribution_stats.sql"
  "$ROOT_DIR/sql/migrations/startup/663_training_export.sql"
  "$ROOT_DIR/sql/migrations/startup/664_provider_error_details_agg_key_dedup.sql"
  # 2026-09-07 24h-audit P0 adjudication: 665 is RETIRED from this track.
  # It was written against the pre-664 world (639/V368 9-part fingerprint)
  # and rebuilds the index WITH LEFT(error_message,200), while 664 (E-#3)
  # plus the current Go upsert (bg/provider_error_aggregator.go) use the
  # 8-part shape — running 665 after 664 makes every aggregator tick fail
  # 42P10 and provider_error_details stops updating. 681 (appended below)
  # converges databases that already ran 665 back to the 8-part shape.
  # The 665 file itself stays in the repo untouched (checksum ledgers of
  # databases that already applied it keep validating).
  # 2026-09-06 PG log audit: external orchestration services and statistics
  # collectors attempt to INSERT into orchestration_runtime_instances and
  # llm_hourly_stats, but these tables were never deployed (42P01 errors every
  # 30s). Create them to support external integrations; the gateway itself does
  # not write to these tables. The llm_hourly_stats hour column accepts only
  # properly formatted TIMESTAMPTZ (YYYY-MM-DD HH:00:00+00); callers sending
  # truncated formats like "2026-09-06T01" will fail (22007 invalid timestamp).
  "$ROOT_DIR/sql/migrations/startup/666_orchestration_and_stats_tables.sql"
  # 2026-09-06 PG log audit follow-up: external redclaw services send truncated
  # timestamps like "2026-09-05T23" to llm_hourly_stats causing 22007 errors
  # every hour. 665 provides 3 compatibility layers: (1) normalize_hour_timestamp()
  # function to parse flexible formats, (2) upsert_llm_hourly_stats() stored
  # procedure for safe insertion, (3) llm_hourly_stats_flexible view with INSTEAD
  # OF trigger for zero-code-change compatibility. External services can choose any
  # layer; the base table stays TIMESTAMPTZ for data integrity. Query
  # llm_hourly_stats_usage_guide for integration examples.
  "$ROOT_DIR/sql/migrations/startup/667_llm_hourly_stats_timestamp_fix.sql"
  # 2026-09-06 PG log audit follow-up: 666 attempted BEFORE trigger but PG validates
  # types before triggers run. Abandoned in favor of 667.
  # "$ROOT_DIR/sql/migrations/startup/666_llm_hourly_stats_direct_trigger.sql"
  # 2026-09-06 PG log audit final fix: 667 clarifies that external services must
  # wrap their hour parameter in normalize_hour_timestamp() to use truncated formats.
  # The recommended pattern is:
  #   INSERT INTO llm_hourly_stats (hour, ...) VALUES (normalize_hour_timestamp($1), ...)
  #   ON CONFLICT (hour) DO UPDATE ...
  # This preserves ON CONFLICT support while accepting "2026-09-06T11" input.
  # Alternative: call upsert_llm_hourly_stats(hour_text, ...) helper function.
  "$ROOT_DIR/sql/migrations/startup/668_llm_hourly_stats_final_fix.sql"
  "$ROOT_DIR/deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql"
  # 2026-09-07 PG log audit: migration 455 已声明把 request_logs_bodies_hot
  # 唯一索引从 (request_id, ts) 切到 (request_id),但实测库上索引仍是
  # (request_id, ts);每个 telemetry 写入触发 42P10。同时 model_offers 视图
  # 缺 priority / unavailable_recover_at 列,provider/client.go 历史 mo.priority
  # 引用与 bg/credential_recovery 冷却写入都失败 42703。
  # 编号 678:原编号 676,与远端 676_routing_opt_active_fix.sql 撞号,重编号到 678。
  "$ROOT_DIR/sql/migrations/startup/678_request_logs_bodies_hot_unique_repair_and_model_offers_columns.sql"
  # 2026-09-07 24h-audit (P0 deploy-gap): 669~676 were missing from this
  # track entirely while their Go code is already on hot paths — annotation
  # pages (training_human_annotations, 669/673/674), routing optimizer
  # (routing_optimization_*, 670/676) and local-provider title/summary
  # routing (671/672/675). On upgraded databases every request through
  # those paths fails 42P01. All idempotent; numeric order; 676 is the
  # renumbered (ex-671) single-active-state fix and must stay after 670.
  "$ROOT_DIR/sql/migrations/startup/669_training_human_annotations.sql"
  "$ROOT_DIR/sql/migrations/startup/670_routing_optimization.sql"
  "$ROOT_DIR/sql/migrations/startup/671_local_provider_catalog.sql"
  "$ROOT_DIR/sql/migrations/startup/672_local_first_title_summary_routing.sql"
  "$ROOT_DIR/sql/migrations/startup/673_annotation_stats_empty_table_fix.sql"
  "$ROOT_DIR/sql/migrations/startup/674_annotation_request_id_unique.sql"
  "$ROOT_DIR/sql/migrations/startup/675_qwen38_family_vendor.sql"
  "$ROOT_DIR/sql/migrations/startup/676_routing_opt_active_fix.sql"
  # 681 converges the provider_error_details fingerprint index back to the
  # 8-part shape (664/E-#3 authoritative, matches the Go ON CONFLICT target)
  # on databases that already ran the retired 665. See the 665 note above.
  # (原编号 678，与 678_request_logs_bodies_hot_unique_repair 撞号，重编号至 681。)
  "$ROOT_DIR/sql/migrations/startup/681_provider_error_details_fingerprint_restore_8part.sql"
  # 679: at most one live local placeholder credential per provider —
  # closes ensureLocalCredential's check-then-act race (duplicate
  # placeholder credentials doubled the local concurrency budget); folds
  # existing duplicates (keeps min id) before creating the partial unique
  # index. Idempotent.
  "$ROOT_DIR/sql/migrations/startup/679_local_credential_unique.sql"
  # 2026-09-07 incident (P0): the canonical query view
  # request_logs_with_current_month was found dropped out-of-band on the
  # shared local PG (wrapper views …_without_customer_id and
  # …_without_request_class_due_at present, canonical absent), breaking every
  # /api/logs request with 42P01. 680 is a staged, purely-additive rebuild of
  # the 577+610 wrapper chain; db.ensureRequestLogsCurrentMonthView mirrors it
  # at every boot. Idempotent no-op on a healthy database.
  "$ROOT_DIR/sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql"
  # 2026-09-07 网关日志审计 (42703 x 92+): 469 只给 models_canonical 加了
  # context_window_override 三列,provider_models 有同名列,但 model_offers
  # 视图从未透出 —— provider/client.go:1507 的候选查询每个请求 42703
  # "column mo.context_window_override does not exist"。682 按 678 的
  # DROP+重建+重挂触发器模式给视图追加三列。独立、幂等,顺序仅要求在 678
  # 之后(同为视图重建,后写者胜,两者追加的列互不重叠)。
  "$ROOT_DIR/sql/migrations/startup/682_model_offers_context_window_columns.sql"
  # 2026-09-07 网关日志审计 (42703 x ~19/min): internal/sessionv2mirror 的
  # session_dim upsert 引用 api_key_id/application_*/owner_*/end_user_id/
  # client_id 七列,但本库 session_dim 停留在 350 的窄形状,358 的 ALTER
  # 从未落地。683 只取 358:25-45 的列+索引段(定义完全一致) —— 不能整
  # 文件重放 358,其 update_session_summary() 体会覆盖 572|563|661 链的
  # 661 终版(clobber guard 有意维护)。幂等,独立,顺序无要求。
  "$ROOT_DIR/sql/migrations/startup/683_session_dim_ownership_columns.sql"
  # 2026-09-07 部署后观察 (聚合器 tick 23505 间歇复现): 坏 665 残留的
  # idx_provider_error_details_tenant_fingerprint(键含 LEFT(error_message,200)、
  # 无 credential_id)与 664/E-#3 权威的 tenant_cred_fingerprint 语义打架 ——
  # 同桶不同凭据+消息样例相同时,cred 仲裁者放行的 INSERT 撞 tenant 指纹。
  # 681 只收敛了 cred 指纹;684 按守卫 DROP 残留(cred 指纹在位且为 8 段才删)。
  "$ROOT_DIR/sql/migrations/startup/684_drop_stale_provider_error_tenant_fingerprint.sql"
  # 2026-09-07 部署后观察 (22P02 x ~9/25min): task_default_routing(_audit)
  # 是 421 的 bigint 租户形状(与 388 text 形状竞争),Go 全线按 text 扫描/
  # 比较(*string、tenant_id = $2 OR 'default'),$2 被推断 bigint 后
  # pgx 传 "default" → 22P02,每个无候选请求的 alternatives 兜底查询失败。
  # 685 按守卫把两表 tenant_id 转 text,并按 388 的 '' 哨兵重建唯一索引。
  "$ROOT_DIR/sql/migrations/startup/685_task_default_routing_tenant_text.sql"
  # 2026-09-07 部署后观察 (ensure tick 42P17 每次复现): 本库
  # session_module_executions_2026_10 上界被写成 '2026-11-01 08:00:00+08'
  # (+08 字面量污染,多 8 小时),ensure 建 2026_11 必然 overlap。686 按守卫
  # DETACH+重建为正确的 00:00:00+08 上界,含 default 分区行搬回兜底。
  "$ROOT_DIR/sql/migrations/startup/686_fix_session_module_executions_2026_10_bounds.sql"
)

# 2026-09-05 PG log audit follow-up (function clobber guard): 572 and 563
# both CREATE OR REPLACE update_session_summary() and 563's older body
# silently overwrote the 572 fix one second after it applied (397 x 22003
# on the day). Any function defined by more than one sequence file must be
# registered below as an intentional chain, written in EXACT sequence order
# with the intended final definition LAST — a chain whose entries drift out
# of sequence order fails the equality check below. Unregistered duplicates
# abort the deployment before the first file is applied. The scanner strips
# SQL line/block comments and dollar-quoted bodies, so prose or dynamic SQL
# that merely mentions CREATE OR REPLACE cannot trigger it.
intentional_function_chains=(
  # 563 restores the hot trigger with the corrected unbounded-numeric ratio
  # body; 661 re-asserts the same fixed body AFTER 563 and validates the
  # function source. 572 must stay before 563; 661 must stay last.
  'update_session_summary|572_session_summary_large_token_ratio.sql|563_session_summary_trigger_on_hot.sql|661_session_summary_token_ratio_reassert.sql|'
  # 654 supersedes 653's DELETE-based archive body with the columnar-safe
  # DETACH PARTITION + DROP path and must stay the later entry.
  'archive_credential_model_index|653_archive_credential_model_index_canonical_return.sql|654_archive_credential_model_index_detach_drop.sql|'
  # 658 extends 656's promote body with the 15 structured feature columns and
  # must stay the later entry.
  'promote_auto_route_selections_hot_to_partition|656_auto_route_selections_hot.sql|658_auto_route_structured_features.sql|'
)
redefined_functions="$(
  for file in "${files[@]}"; do
    base="$(basename "$file")"
    awk '
      BEGIN { indq = 0; inbc = 0 }
      {
        line = $0
        clean = ""
        while (length(line) > 0) {
          if (indq) {
            end = index(line, dqtag)
            if (end == 0) { line = "" }
            else { line = substr(line, end + length(dqtag)); indq = 0 }
          } else if (inbc) {
            end = index(line, "*/")
            if (end == 0) { line = "" }
            else { line = substr(line, end + 2); inbc = 0 }
          } else {
            dq = match(line, /\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$/)
            bc = index(line, "/*")
            lc = index(line, "--")
            if (dq > 0 && (bc == 0 || dq < bc) && (lc == 0 || dq < lc)) {
              clean = clean substr(line, 1, dq - 1)
              tag = substr(line, dq, RLENGTH)
              rest = substr(line, dq + RLENGTH)
              end = index(rest, tag)
              if (end == 0) { indq = 1; line = "" }
              else { line = substr(rest, end + length(tag)) }
            } else if (bc > 0 && (lc == 0 || bc < lc)) {
              clean = clean substr(line, 1, bc - 1)
              line = substr(line, bc + 2)
              end = index(line, "*/")
              if (end == 0) { inbc = 1; line = "" }
              else { line = substr(line, end + 2) }
            } else if (lc > 0) {
              clean = clean substr(line, 1, lc - 1)
              line = ""
            } else {
              clean = clean line
              line = ""
            }
          }
        }
        if (length(clean) > 0) print tolower(clean)
      }
    ' "$file" \
      | grep -oE 'create[[:space:]]+or[[:space:]]+replace[[:space:]]+function[[:space:]]+("?[a-z_][a-z0-9_$]*"?\.)*"?[a-z_][a-z0-9_$]*"?' \
      | sed -E 's/.*function[[:space:]]+//; s/"//g; s/^([a-z_][a-z0-9_$]*\.)+//' \
      | awk -v f="$base" '{ print $0 "\t" f }' || true
  done | awk -F'\t' '
    {
      if (!(($1) in chain)) { chain[$1] = "|"; hits[$1] = 0 }
      if (index(chain[$1], "|" $2 "|") == 0) { hits[$1]++; chain[$1] = chain[$1] $2 "|" }
    }
    END { for (f in chain) if (hits[f] > 1) print f chain[f] }'
)"
guard_violations="$(printf '%s\n' "$redefined_functions" | while IFS= read -r entry; do
  if [[ -z "$entry" ]]; then continue; fi
  known=0
  for rule in "${intentional_function_chains[@]}"; do
    if [[ "$entry" == "$rule" ]]; then known=1; break; fi
  done
  if [[ "$known" == 0 ]]; then printf '%s\n' "${entry%|}"; fi
done)"
if [[ -n "$guard_violations" ]]; then
  printf 'error: sequence redefines the same function from multiple files; register each intentional chain in intentional_function_chains (entries must match sequence order, last entry wins):\n%s\n' "$guard_violations" >&2
  exit 5
fi
if [[ -n "$redefined_functions" ]]; then
  printf 'function clobber guard: multi-file redefinitions match the intentional allowlist:\n'
  printf '%s\n' "$redefined_functions" | sed 's/^/  /; s/|$//'
fi

for file in "${files[@]}"; do
  [[ -f "$file" ]] || { printf 'error: missing migration %s\n' "$file" >&2; exit 4; }
  marker="${sequence_name}:$(basename "$file")"
  if [[ "$(psql_query "SELECT 1 FROM public.gateway_db_revision_sequences WHERE sequence_name='${marker}' LIMIT 1")" == 1 ]]; then
    printf 'already applied: %s\n' "${file#"$ROOT_DIR/"}"
    continue
  fi
  printf 'applying %s\n' "${file#"$ROOT_DIR/"}"
  psql_file "$file"
  psql_query "INSERT INTO public.gateway_db_revision_sequences (sequence_name) VALUES ('${marker}') ON CONFLICT (sequence_name) DO NOTHING"
done
# Retire the legacy sequence-wide marker so it cannot mask future appends.
psql_query "DELETE FROM public.gateway_db_revision_sequences WHERE sequence_name='${sequence_name}'"
printf 'database revision sequence completed successfully: %s\n' "$sequence_name"
