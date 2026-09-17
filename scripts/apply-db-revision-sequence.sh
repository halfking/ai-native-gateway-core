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
  # 2026-09-11 部署缺口审计（本机实测）：693 只进了仓库文件与 installer
  # 全新安装路径，升级库无任何通道应用它（本序列止于 686，db.go 也无 ensure
  # 镜像），新二进制的 clear_canonical / discovery upsert /
  # routing_health_checker 每个周期报 42703 column does not exist。文件幂等
  # （IF NOT EXISTS 守卫 + DO 块），db.go 侧另有 ensureProviderModelsCanonicalClearedAt
  # 自愈镜像兜底。
  "$ROOT_DIR/sql/migrations/startup/693_provider_models_canonical_cleared_at.sql"
  # 2026-09-12 通道同步：694 把 13 个 ensure_* 分区函数的边界计算固定在
  # Asia/Shanghai（SET LOCAL），消除 UTC 会话下月初边界偏移 8h 的 473 类
  # 缝隙复发面。共享 252 PG 的迁移账本已于 2026-09-11 21:28 记录 applied
  # （695 重编号时确认 690-694 已占用），升级库按账本跳过、其余环境正常
  # 应用；纯 CREATE OR REPLACE FUNCTION，幂等收敛，序列内无同名函数冲突
  # （各 ensure_* 的更早定义文件均不在本序列），故无需 clobber chain 登记。
  "$ROOT_DIR/sql/migrations/startup/694_partition_ensure_timezone.sql"
  # 2026-09-12 同款升级通道缺口：695 promote final-success 冲突自愈（P2
  # 冷迁移停滞根因的数据面修复）同样只存在于仓库文件，db.go 无 ensure 镜像、
  # 本序列此前止于 693。原编号 694 在共享 252 PG 的迁移账本里已被其他项目
  # 的 694_partition_ensure_timezone.sql 占用（2026-09-11 21:28），deploy
  # pending 判定按账本记录跳过、自愈永不生效，故 2026-09-12 重编号为 695。
  # 纯 CREATE OR REPLACE promote_request_logs_hot_to_partition
  # （688 体 + demote 步骤），幂等收敛，down 无。必须在 693 之后（无依赖，
  # 顺序仅为序列递增约定）。
  "$ROOT_DIR/sql/migrations/startup/695_request_logs_promote_final_success_self_heal.sql"
  # 2026-09-12 视图列缺口：696 给 request_logs_with_current_month 追加
  # system_fingerprint lateral 阶段（487 加父表列、603 补 hot 列，但视图基础
  # 包装的列交集在 603 之前冻结，链上从未重建）。integrity_fingerprint_drift
  # 的 7 天窗口读者因此一直钉在裸父表（近期窗口失明教义的有意排除项）。
  # 纯 CREATE OR REPLACE VIEW（尾部追加列，无 DROP），幂等收敛，down 无。
  # 编号前已查生产双账本：696 在 gateway_db_revision_sequences 与
  # schema_migrations 均未被本库或其他项目占用。
  "$ROOT_DIR/sql/migrations/startup/696_request_logs_view_system_fingerprint.sql"
  # 2026-09-12 指纹写路径贯通:697 把 system_fingerprint 追加进 promote 的
  # RETURNING/INSERT/SELECT 三列清单尾部(695 体),使 telemetry 新落的
  # request_logs_hot.system_fingerprint 列随晋升进月度分区——否则 8h 窗口外
  # 漂移检测即失明(晋升行丢列,父表默认 NULL)。列在 hot(603)/parent(487)
  # 均已存在,纯 CREATE OR REPLACE,幂等收敛,down 无。697 已查生产双账本空闲。
  "$ROOT_DIR/sql/migrations/startup/697_request_logs_promote_system_fingerprint.sql"
  # 2026-09-12 promote 月份路由会话时区钉扎（694 的 promote 侧补全）：9 个
  # promote_*_hot_to_partition 函数体在调用 ensure_* 之前先用 date_trunc 按
  # 会话时区分组,694 只钉了 ensure 的边界计算——UTC 会话下 [月初 00:00,08:00)
  # +08 窗口的行落入上一月分组,ensure 按 694 钉扎的上海边界建的分区不含该行,
  # INSERT 23514 整批卡死（cfl 表的 TTL trim 随即开数据丢失窗口,657828114 的
  # 预建只救了 cfl 一张表）。生产集群 default TZ=Asia/Shanghai 故潜伏;
  # 新宿主机/维护 psql/裸 pgx 任一 UTC 会话即触发。request_logs 体 = 697 体
  # （含 695 自愈与 system_fingerprint 三列）加钉扎,其余 8 个 = objects/ 规范
  # 体加钉扎,迁移文件由 objects/ 机械变换生成。纯 CREATE OR REPLACE,幂等
  # 收敛,down 无。698 已查生产双账本空闲（seq/schema_migrations 均 0 命中）。
  "$ROOT_DIR/sql/migrations/startup/698_promote_hot_partition_timezone_pin.sql"
  # 2026-09-12 接线补齐:699 supplier_errors ensure 钉扎(531ea1a86)交付时只接了
  # installer embeddata/dbinit 路径,漏了本通道——升级型数据库(共享 252 生产 PG
  # 即是)经通道升级将永远轮不到该迁移,正是 693 类缺口。文件自带 schema_migrations
  # INSERT(ON CONFLICT DO UPDATE)与 down 镜像,通道补条目即闭环。升级库 V371 已装
  # 该函数(签名一致,纯函数体重定义),幂等收敛。
  "$ROOT_DIR/sql/migrations/startup/699_supplier_errors_ensure_timezone_pin.sql"
  # 2026-09-12 视图列缺口第二起:700 给 request_logs_with_current_month 追加
  # raw_model_name lateral 阶段(485 加父表列、603 补 hot 列,但视图基础包装的
  # 列交集在 485 之前冻结、链上从未重建)——integrity_fingerprint_drift 自
  # 83bf582dd 起按近期窗口教义读视图并 SELECT raw_model_name,每周期 42703:
  # 本机 2089 部署即时炸出,生产 252 视图同构(112 列、无 raw_model_name),
  # 携带 83bf582dd 的下一个生产二进制上线即复现。696 的 system_fingerprint
  # 在同一 select list 保留。纯 CREATE OR REPLACE VIEW(尾部追加列,无 DROP),
  # 幂等收敛,down 无。编号注记:本轮先按当时双账本核查取 699,与并行线
  # (698 promote 钉扎 / 699 supplier_errors 钉扎)撞号——两线均先入 origin,
  # 本迁移重编号 699→700;重编号前已复核生产双账本 698-702 均 0 命中
  # (schema_migrations 无 698/699/700/701,sequences 无 :69[89]/:70[0-2])。
  "$ROOT_DIR/sql/migrations/startup/700_request_logs_view_raw_model_name.sql"
  # 2026-09-13 balance-floor guard: carry the schema migration through the
  # upgrade channel as well as Go startup ensure, keeping both ledgers aligned.
  "$ROOT_DIR/sql/migrations/startup/701_credential_balance_floor.sql"
  # 2026-09-13 接线补齐:703 supplier_errors promote 钉扎(703 文件注释引 r20 §二.4)
  # 交付时只落了 startup 文件 + 01-schema baseline + Go ensure 面,漏了本通道——
  # 升级型数据库(共享 252 生产 PG 即是)经通道升级将永远轮不到它,693/699/701
  # 同款缺口。文件自带 schema_migrations INSERT(695-699 先例),通道补条目即闭环。
  "$ROOT_DIR/sql/migrations/startup/703_supplier_errors_promote_timezone_pin.sql"
  # 2026-09-14 P0 存储治理:request_logs promote 断链修复。337 曾 DETACH
  # 2026_07..2026_12 月分区,而 ensure_request_logs_partition(694 体)只查
  # pg_class relname——DETACH 后的空壳仍存在,ensure 永远跳过,promote
  # (602/688)INSERT INTO 父表全路由进 request_logs_default(本机实测
  # 609MB/255,084 行积压,月分区 0 行空壳)。705 重写 ensure(attached-aware
  # + 空壳重挂 + default 缝隙自愈),并用 repair_request_logs_detached_partitions()
  # 一次性补列重挂空壳、把 default 积压按月搬回分区。文件自带双账本
  # schema_migrations INSERT(695-704 定式)。
  "$ROOT_DIR/sql/migrations/startup/705_request_logs_reattach_detached_partitions.sql"
  # 2026-09-14 存储优化方案 v2 S1a(docs/03-design/04-data-design/
  # storage-optimization-plan.md §4):六表族补全。706 建 session_memora/
  # session_censors/session_tools 三新表族(hot+ensure_session_family_partitions
  # +promote,430/614/638 惯例)+ sessions 访问维度补列;707 session_turns 宽表化
  # (D1 五类补采+正文列+is_final_success 部分唯一索引)并把
  # promote_session_turns_hot_to_partition 重写为有序列契约校验+SELECT * 形态
  # (显式列清单会在加列后 promote 静默丢新列);708 session_bodies kind 列
  # (final_full/turn_delta pivot 前置)+旧行回填+final_full 部分唯一索引+
  # DROP sessions.last_full_* 456 死列(零读写,本机审计 100% NULL)+
  # promote_session_bodies_hot_to_partition 同款重写。三文件均自带双账本
  # schema_migrations INSERT(695-705 定式)。707/708 内为对应 promote 函数在
  # 本通道清单内的唯一定义者,无 chain 登记需求(526/615/626/636/638/640/688
  # 不在本清单)。
  "$ROOT_DIR/sql/migrations/startup/706_session_family_s1a.sql"
  "$ROOT_DIR/sql/migrations/startup/707_session_turns_s1a.sql"
  "$ROOT_DIR/sql/migrations/startup/708_session_bodies_s1a.sql"
  # 2026-09-15 存储优化方案 v2 S4 前置（plan §4-S4 / §4.2-10，观察台账
  # Round 1b）：713 turns cost_usd numeric(12,6)→(14,8)（E5 G1-cost 舍入
  # 对齐 v1；cost_display 为 double precision 无需动）+712
  # session_mirror_outbox（spec §12 GAP-2 闭环：mirror 失败行持久登记，
  # payload=完整 entry JSON，internal/sessionv2mirror replay.go 重放器消化；
  # 历史回填脚本 scripts/audit/mirror_outbox_backfill.sql 灌同表）。均自带
  # schema_migrations INSERT(695-705 定式)；两者不定义函数，无 chain 登记。
  # 编号注记(R29 审计)：turns cost 精度迁移原编号 711 与并行线
  # 711_hosted_tasks 撞号(双账本 '711' 同号异文件歧义)，按 699→700/709→710
  # 先例重编号 713；重编号前已核远端 main(7ffcddaee) 双账本 713 零命中。
  # 存量库若已按旧 '711' 应用过同体，重放 713 幂等(ALTER TYPE 同精度无害)。
  "$ROOT_DIR/sql/migrations/startup/712_session_mirror_outbox.sql"
  "$ROOT_DIR/sql/migrations/startup/713_session_turns_cost_precision.sql"
  # 2026-09-15 R29 审计：hostedtask P0(711_hosted_tasks)此前只进 installer
  # 全新安装通道，存量库(seq 通道)永远没有三表——功能开关一旦打开即每 5s
  # reconciler Error×3 + 首请求 500。文件幂等(全 IF NOT EXISTS)、纯建表、
  # 无函数定义，无 chain 登记需求；不自登记 schema_migrations(installer
  # 通道惯例，账本由 installer 侧维护)。
  "$ROOT_DIR/sql/migrations/startup/711_hosted_tasks.sql"
  # 2026-09-14 存储优化方案 v2 S2（docs/03-design/04-data-design/
  # storage-optimization-plan.md §3 D6/§4）：request_logs_with_current_month
  # 同名视图体重建为 session 家族拼装体——session_turns(_hot) 113 列会话投影
  # （缺源列 NULL 补位，列映射契约登记在文件头）UNION ALL v1 体（700 形态
  # 冻结）× 反连接（request_id 已入 turns 的 v1 行不再输出，双写期不重复
  # 计数）。约 40 个读方零改动切到会话族读路径。纯 CREATE OR REPLACE VIEW
  # （113 列名称/顺序/类型逐一保持，显式 CAST 钉类型），幂等收敛（viewdef
  # 含 session_turns 即跳过；session_turns 缺表保留 v1 体由 db.ensure 兜底），
  # down 恢复 700 双形态体。文件自带 schema_migrations INSERT(695-705 定式)。
  # 编号注记:方案原文编号 709 已被共享账本占用(2026-09-14 14:19 并行线
  # work_type route coverage,裸 '709'),按 699→700 先例重编号 710;编号前已
  # 查本机双账本 710 空闲(schema_migrations/sequences 均 0 命中)。
  # Go 镜像体:db/request_logs_view_schema.go canonicalV2DDL(启动自愈/升级
  # 同体),等价性由 db/view_schema_v2_contract_test.go 校验。
  "$ROOT_DIR/sql/migrations/startup/710_request_logs_view_session_family_v2.sql"
  # 2026-09-16 R30 审计（时区钉扎波收尾）：694/698 漏网的 6 个分区函数统一
  # 钉扎 Asia/Shanghai——4 个 ensure（session_module_executions/
  # dashboard_access_events/cache_metrics 为 475 date 签名体，handoff_logs
  # 为 534 columnar 体，其 DECLARE 初始化器先于 BEGIN 求值，已移入函数体）
  # + 579/580 两个 promote 的 date_trunc 月份分组。686 已在
  # session_module_executions 实际复发一次 473 类边界漂移（42P17），本迁移
  # 除根。纯 CREATE OR REPLACE FUNCTION + 账本 upsert，幂等收敛；714 编号
  # 已核对本机与 origin/main 双侧空闲。
  "$ROOT_DIR/sql/migrations/startup/714_partition_timezone_pin_remaining.sql"
  # 2026-09-17 R33 审计（通道登记补齐）：715 route_incidents pending state。
  # bba08b922 引入 StatePending、6f3d03073 接线 Go-ensure（ensure 的 CHECK/
  # 索引重建幂等），但 sql/migrations/startup/715_*.sql 未登记任何投递通道，
  # 通道门禁红灯（693/699/701/703 复发形态）。按 701 定式双通道登记：
  # sequence 管存量库升级（共享 PG 预升级不启动新二进制的路径），
  # ensure 管全新安装与启动自愈（db.go ensureRouteIncidentPendingState）。
  "$ROOT_DIR/sql/migrations/startup/715_route_incidents_pending_state.sql"
  # 2026-09-17 R34 审计（通道登记补齐）：716 统一探测健康视图族到
  # node_probe_state 单一事实源。SQL 体与 db.ensureProbeHealthDashboardViews
  # 幂等同构（网关启动亦重建），但未登记任何投递通道——R34 新装的
  # TestCanonicalStartupMigrationsAtOrAbove704AreRegistered 守卫与通道门禁
  # 双双红灯。按 701 定式登记 sequence 管存量库升级。
  "$ROOT_DIR/sql/migrations/startup/716_unify_probe_health_views.sql"
  # 2026-09-17 R36 审计（新迁移登记）：717 request_logs_hot 列型对齐母表
  # （10 列，6 列硬不兼容）——闭合 R34 遗留#2 全新安装 42804 启动链阻断
  # 与 602 promote 批次失败。三份 01-schema baseline 已同步对齐并由
  # TestBaselineRequestLogsHotColumnTypesMatchMother 守卫。sequence 管存量
  # 库升级（hot 仅 8h 数据，ALTER 重写秒级）。
  "$ROOT_DIR/sql/migrations/startup/717_request_logs_hot_column_alignment.sql"
  # 2026-09-17 R37 SQL 专项审计（新迁移登记）：718 冗余索引清理 + TTL/claim
  # 缺失索引补齐（42 个函数等价冗余索引 drop，全部亲核无约束支撑、无 ensure
  # 创建者；request_envelope/sticky_sessions 补 expires_at 索引、
  # session_aggregate_outbox 补 claimable partial 索引）。非分区表 CONCURRENTLY；
  # 父表 idx_request_logs_ts_desc 级联清 attached 叶子副本。生产低峰执行。
  "$ROOT_DIR/sql/migrations/startup/718_drop_redundant_indexes_and_add_ttl_indexes.sql"
  # 2026-09-17 R38 审计（新迁移登记）：719 ensure 约束影蔽索引根治 + request_logs
  # 父索引跨通道所有权归一（drop idx_request_logs_parent_request_id，canonical
  # 归 parent_ts）+ tool_usage_stats_hot ASC/DESC 三对收敛（保 348 显式 DESC）。
  # 与 db.go/db_omnifree.go 同 commit 删除 ensure 创建者联动，drop 后不再复活。
  "$ROOT_DIR/sql/migrations/startup/719_unify_ensure_shadowed_indexes_and_parent_index_owner.sql"
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
  # 697 appends system_fingerprint to the three explicit column lists on top
  # of 695's body (self-heal demote kept); 698 adds the Asia/Shanghai pin on
  # top of 697's body and must stay the later entry.
  'promote_request_logs_hot_to_partition|695_request_logs_promote_final_success_self_heal.sql|697_request_logs_promote_system_fingerprint.sql|698_promote_hot_partition_timezone_pin.sql|'
  # 699 re-pins ensure_supplier_errors_partition (V371 deployed the original
  # out-of-repo-family body) to Asia/Shanghai; the pin must stay the later entry.
  'ensure_supplier_errors_partition|V371__supplier_errors_hot_and_stats.sql|699_supplier_errors_ensure_timezone_pin.sql|'
  # 703 re-pins promote_supplier_errors_hot_to_partition month grouping to
  # Asia/Shanghai on top of V371's original body; the pin must stay the later
  # entry (same V371-track pattern as 699; 703 landed without this
  # registration and aborted every later deploy at the pre-flight guard,
  # 2026-09-14 deploy-local incident).
  'promote_supplier_errors_hot_to_partition|V371__supplier_errors_hot_and_stats.sql|703_supplier_errors_promote_timezone_pin.sql|'
  # 705 rewrote ensure_request_logs_partition as the attached-aware body
  # (detached-shell re-attach + default-gap self-heal) on top of 694's
  # timezone-pinned body; the rewrite must stay the later entry. 705 landed
  # without this registration — same pre-flight guard abort class as 703
  # (caught 2026-09-14 when S1a files joined the sequence).
  'ensure_request_logs_partition|694_partition_ensure_timezone.sql|705_request_logs_reattach_detached_partitions.sql|'
  # 659 rewrote these seven promote bodies as the single atomic CTE form;
  # 688 later aligned their defaults to the Go scheduler (not in this array,
  # so invisible to the scanner) and 698 re-derives each body from the
  # sql/objects/ canonical (byte-identical to 688's live body, verified
  # per-function) plus the Asia/Shanghai pin. 698 must stay the later entry.
  'promote_tool_usage_stats_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_credit_ledger_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_request_logs_bodies_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_usage_ledger_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_request_wal_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_credential_model_index_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_routing_decision_log_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
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
