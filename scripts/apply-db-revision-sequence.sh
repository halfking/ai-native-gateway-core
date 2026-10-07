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
# 2026-09-21 252 部署验证轮（纪律⑨，F4 机制债收口）：台账从"按文件名记账"
# 升级为"文件名+内容指纹记账"。content_sha256 为 NULL 的行是历史遗留记账
# （指纹通道上线前应用，无法证明当时应用的是哪个内容版本）；此后本脚本的
# 每次应用都记录当前文件内容 sha256，同名文件内容变更即可被识别并重放
# （通道契约：每个迁移幂等，重跑安全——见文件头注释）。
psql_query "ALTER TABLE public.gateway_db_revision_sequences ADD COLUMN IF NOT EXISTS content_sha256 text"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    printf 'error: no sha256sum/shasum available for content fingerprinting\n' >&2
    exit 1
  fi
}

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
  # 2026-10-02 24h 审计第二十八轮（534 canonical 收编）：handoff_logs_hot 列存
  # 改造此前只进 installer StartupFiles（fresh 链 baseline-gap 簇），存量库的
  # sequence 通道断链——809 触发「最高编号必须在 files 数组」预提交门红 60 提交
  # 时才现形（该守卫只盯最高编号，534 静默漏网）。头注明示 legacy heap/已升级
  # 252/fresh baseline 三形态 safe to replay。位置硬约束（clobber guard 实证）：
  # 其 ensure_handoff_logs_partition 与 714 同名异体，必须先于 714——714 的
  # handoff_logs 腿本就是 534 columnar 体的时区钉扎版，倒序会让 534 旧体覆盖
  # 714 钉扎体（deploy exit 5）。
  "$ROOT_DIR/sql/migrations/startup/534_handoff_logs_hot_columnar.sql"
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
  # 2026-09-18 R40 审计（新迁移登记）：720 RLS policy 词汇统一（设计
  # docs/design/rls-tenant-isolation-architecture.md §五 Phase1#1）——
  # app.tenant_id 57 条 → get_current_tenant()、app.is_super_admin 残留 2 条
  # → 标准旁路形；superuser 时期零行为变化（llm_gateway 仍 SUPERUSER+BYPASSRLS）。
  # f5328e13c 仅落 embeddata 单点（TestStartupFilesAreAllEmbedded 在 main 上红），
  # 本轮补齐五点同步后登记存量库升级通道。
  "$ROOT_DIR/sql/migrations/startup/720_rls_policy_vocabulary_unification.sql"
  # 2026-09-18 R40 审计（通道登记补齐）：721 credentials 余额来源/错误列
  # （507d78cff）仅落 canonical+embeddata 文件副本，四处代码登记全缺——
  # 与 720 同款不完整五点同步，R40 补登记。纯 ADD COLUMN IF NOT EXISTS +
  # 幂等约束补挂，存量库重放零风险。
  "$ROOT_DIR/sql/migrations/startup/721_credential_balance_source_and_error.sql"
  # 2026-09-18 R40 审计（新迁移登记）：722 durable 家族 schema 收敛——把 516
  # 最终形态的幂等体（durable_llm_task_events / durable_pending_outbox 两表
  # + RLS 政策 + durable_llm_tasks.checkpoint_payload）重放到应用过 516 中间
  # 形态的存量库（本机真库取证：中间形态从未入 git，marker 已锁死 516、
  # ensure 链无 durable 条目，代码引用缺失表/列 = 首次事件写入即运行时失败）。
  # 全语句幂等，全新安装零变化。
  "$ROOT_DIR/sql/migrations/startup/722_durable_family_schema_convergence.sql"
  # 2026-09-18 R40 审计（新迁移登记）：723 RLS Phase1#3 空 RLS 补 ENABLE——
  # attachments / candidate_failure_logs_columnar_old 两表 policy 已在且词汇
  # 标准形，但 relrowsecurity=false 沦为摆设；ENABLE（不 FORCE）superuser
  # 时期零行为变化，为 Phase 2 降权作准备（设计 §五 Phase1#3）。原编 721 与
  # 507d78cff 余额元数据迁移撞号，重编 723。
  "$ROOT_DIR/sql/migrations/startup/723_rls_enable_attachments_and_cfl_old.sql"
  # 2026-09-18 taskprofile 插件模块（新迁移登记）：724 task_type_corrections
  # 独立新表（auto 任务类型逐请求人工修正），无既有对象改动、IF NOT EXISTS 幂等。
  # 原编 721 与 R40 余额元数据迁移撞号，重编 724。
  "$ROOT_DIR/sql/migrations/startup/724_task_type_corrections.sql"
  # 2026-09-18 R41 修复轮前置（并行会话交付，R42 补五点登记）：725
  # request_logs + tenant_model_policies 补 super_admin_bypass policy,
  # 全语句 DROP POLICY IF EXISTS + CREATE POLICY 幂等。
  "$ROOT_DIR/sql/migrations/startup/725_r41_request_logs_and_tmp_super_admin_bypass.sql"
  # 2026-09-18 auto route rollup 42P10 修复（58384b0d8，R43 补五点登记）：726
  # 恢复 718 误删的 credential_model_index_hot 唯一索引——154 生产事故
  # （rollup ON CONFLICT 42P10 全失败）在存量库的标准修复通道。
  # ctid 去重 + CREATE UNIQUE INDEX IF NOT EXISTS 幂等。
  "$ROOT_DIR/sql/migrations/startup/726_restore_credential_model_index_hot_unique.sql"
  # 2026-09-20 252 PG SQL 日志审计轮（本轮交付）：727 慢查询索引三连——
  # request_stage_events 保留清理 DELETE 补 created_at 索引（9 天均值 10.5s
  # 撞 30s 批超时，retention 文件头 2026-09-05 预登记条件达成）；
  # session_turns 父表 + session_turns_hot 独立表补 CASE 表达式索引
  # （会话存在性 EXISTS 视图查询 P50 687ms→1.1ms，真库 A/B 实证）。
  # CREATE INDEX CONCURRENTLY IF NOT EXISTS 幂等；2026-09-20 已在 252
  # 存量真库实跑验证。
  "$ROOT_DIR/sql/migrations/startup/727_sql_audit_slow_query_indexes.sql"
  # 2026-09-21 252 PG SQL 日志审计复核轮：728 request_logs 分区侧
  # credential+model 表达式索引（父表 + 存量月分区三段式，hot 侧
  # sql/objects 已有）——provider-model 抽屉轮询查询窗口跨入月分区时
  # Parallel Seq Scan（pss 10 天 184,620 次/累计 8.6h 全库第一；
  # 72h 窗 EXPLAIN ANALYZE 实测 105s）。CREATE INDEX CONCURRENTLY
  # IF NOT EXISTS 幂等；本日已在 252 存量真库实跑验证。
  "$ROOT_DIR/sql/migrations/startup/728_sql_audit_request_logs_credential_model_index.sql"
  # 2026-09-20 R48（ec605014c 交付本体，本条目为门禁收口补登记）：730
  # session role hierarchy——会话角色识别 + role_task_llm_mapping 配置表
  # （按角色×任务类型自动选 LLM）。CREATE TABLE IF NOT EXISTS 幂等。
  # R48 提交时漏登记致契约门禁红（R33 期 715 同款缺口），补录于此。
  "$ROOT_DIR/sql/migrations/startup/730_session_role_hierarchy.sql"
  # 2026-09-21 R50 审计轮：731 auto_route_selections 角色路由归因三列
  # （agent_role/task_kind/routing_source，父表+hot 双表 ADD COLUMN IF NOT
  # EXISTS 幂等）——亲和学习剔除 role 强制选型行的数据基础。
  "$ROOT_DIR/sql/migrations/startup/731_auto_route_selection_role_attribution.sql"
  # 2026-09-20 会话存储解耦 v3（六点同步登记：db.Open ensure 链不扫 SQL
  # 文件，新迁移必须进本清单才会在存量库应用）：原编 727/728 与 origin
  # 上 R46/R48 撞号，重编 733/734。733 建 session_turn_details 特征层
  # 表族（hot + 月分区 + RLS + promote + request_logs 反向回填）；
  # 734 把 canonical 视图 session 分支 30 个 NULL 占位换成 details
  # LEFT JOIN。Go 侧 ensure（ensureRequestLogsCurrentMonthView）在表族
  # 缺席时自动回退 710 形态，双形态兼容。
  "$ROOT_DIR/sql/migrations/startup/733_session_turn_details.sql"
  "$ROOT_DIR/sql/migrations/startup/734_request_logs_view_details_join.sql"
  # 2026-09-21 252 部署验证轮：729 session_turns credential+ts 表达式索引
  # （父表 + 存量月分区三段式 + session_turns_hot 独立表）——抽屉轮询查询
  # 72h 长窗的 session_turns 分支裸 ts 范围扫 + 逐行 CASE 过滤（252 分支
  # cost 60650/总 71260，实测残余 10.9s）。CREATE INDEX CONCURRENTLY
  # IF NOT EXISTS 幂等；本日已在 252 与本机存量真库实跑验证。
  "$ROOT_DIR/sql/migrations/startup/729_sql_audit_session_turns_credential_ts_index.sql"
  # 2026-09-21 R51 审计轮：735 models_canonical active 折叠名表达式唯一索引
  # （R50 F19 对账收口，表达式与 modelname.DedupCanonicalNameSQL run-collapse
  # 臂逐字一致）。fail-closed 守卫：active 折叠重复对未清零时拒绝执行并指路
  # cleanup 脚本（CASCADE 引用族的裁决不进启动迁移）。守卫+IF NOT EXISTS 幂等；
  # 本轮已在本机存量真库先跑对账脚本后验证通过。
  "$ROOT_DIR/sql/migrations/startup/735_models_canonical_active_folded_unique.sql"
  # 2026-09-22 Wave 3 B1：736 峰谷倍率 —— usage_ledger[_hot].rate_multiplier
  # / request_logs[_hot].credits_rate_multiplier 四列（ADD COLUMN IF NOT
  # EXISTS，分区族自动级联）+ maas_resolve_rate_multiplier() 共享取档函数
  # （与 maas.ResolveRateMultiplier 同规则，配置 maas.rate_periods）。
  # 幂等，默认 enabled=false 行为零漂移。
  "$ROOT_DIR/sql/migrations/startup/736_maas_rate_multiplier.sql"
  # 2026-09-22 Wave 3 B8：737 内部对账落表 —— maas_reconciliation_findings
  # （CREATE TABLE IF NOT EXISTS，幂等），bg.LedgerReconciler 差异落表。
  "$ROOT_DIR/sql/migrations/startup/737_maas_reconciliation_findings.sql"
  # 2026-09-22 764d2514b：738 view 链补 credits_rate_multiplier 列（B1
  # follow-up：680/717/734 冻结体不自动补列，bg/stats_minute_rollup 每分钟
  # INSERT 抛 column does not exist）。幂等；R56 补登——该提交当时漏了本
  # 通道登记（693/699/701/703 同型复发形态）。
  "$ROOT_DIR/sql/migrations/startup/738_view_chain_credits_rate_multiplier.sql"
  # 2026-09-23 R56：739 promote 函数补倍率列（698 的两个 promote 显式列
  # 清单止于 system_fingerprint/error_kind，736 加列后热窗转移把倍率证据
  # 落 NULL/DEFAULT 1.0）。幂等（CREATE OR REPLACE FUNCTION）。
  "$ROOT_DIR/sql/migrations/startup/739_promote_functions_rate_multiplier.sql"
  # 2026-09-23 R57 B7：740 view 链补投影真实 client_ip——virtual_ip 是
  # identity 派生假名 10.x，GeoIP 归类对它不可达；rollup/看板切真源
  # client_ip（341 列经 740 投影进链）。regexp 补列 + 顶层全量重建 +
  # 列数守卫 fail-closed（738 惯用法）。幂等。
  "$ROOT_DIR/sql/migrations/startup/740_view_chain_client_ip.sql"
  # 2026-09-23 R65：742 hosted_task_events 类型 CHECK 扩容新增 'recalled'，
  # 供 POST /v1/hosted-tasks/{id}/recall（§3.3 轻量快照路径）追加召回事件
  # （711 白名单不含 recalled；741 已被 B11 申领故跳号）。经 sequence 通道
  # 升级的存量库不下发此行则 recall 写事件违反约束。幂等（约束重建）。
  "$ROOT_DIR/sql/migrations/startup/742_hosted_task_recalled_event.sql"
  # 2026-09-24 R60 S3-F4：743 存量 providers/provider_catalog.protocol 归一
  # 清洗（vapeur 类脏值，如 openai-response→openai-responses）——映射与
  # provider/catalog NormalizeProviderProtocol 别名表逐条对齐；未知值保持
  # 原样（与 Go 侧语义一致）。幂等；R59 已封三个写边界，本迁移清洗存量。
  # 经 sequence 通道升级的存量库不下发则管理面（健康检查/探针）对脏行
  # 持续误判（读面归一为防御层，非替代）。
  "$ROOT_DIR/sql/migrations/startup/743_normalize_provider_protocol.sql"
  # 2026-09-24 252 SQL 日志审计第六轮：744 部分索引补课
  # （A: session_aggregate_outbox done 行 TTL 清理缺 (status='done',
  # completed_at) 组合，645MB 全表扫 ×255 次/45min 是整库背景噪音主源；
  # B: session_turns digest 回填 WHERE digest IS NULL 无索引，backlog=0
  # 仍空扫 68 万行。分区父表三段式，与 727/728/729 同法）。c2f78d1c0
  # 重编号交付时漏登此条，门禁必需项已由 aa1160746 上账，此处补齐尾巴。
  # 经 sequence 通道升级的存量库不下发则两处全表扫噪音在升级库原样保留。
  "$ROOT_DIR/sql/migrations/startup/744_sql_audit_partial_indexes.sql"
  # 2026-09-24 对账报表设计切片（R63 收口）：745 report_snapshots 日报快照
  # 表（scope×model×day 粒度，UNIQUE 四键）。原文件曾死放 migrations/ 顶层
  # （无任何投递通道，五点同步全缺），本轮修正表结构后投递 startup 通道；
  # 消费方 worker 尚未实现，属设计预埋（幂等 CREATE TABLE IF NOT EXISTS）。
  "$ROOT_DIR/sql/migrations/startup/745_report_snapshots.sql"
  # 2026-09-25 R65 补登：746 report_snapshots internal dims（tenant_id
  # bigint→text + credits/latency 三列 + scope 枚举扩员）。9635b9b17 交付
  # 时走了 dbinit + boot ensure 双臂但漏登本 sequence 通道（744/745 先例
  # 均三臂齐备）；ALTER TYPE text::text 与 ADD COLUMN IF NOT EXISTS 均
  # 可重入，boot ensure 已升级过的库重跑本迁移体无副作用。
  "$ROOT_DIR/sql/migrations/startup/746_report_snapshots_internal_dims.sql"
  # 2026-09-26 R67 24h 审计轮：749 usage_facts occurred_at 前导索引——
  # 每日 rollup 五查询 + stats 对账全是纯 occurred_at 范围条件，537 的
  # 4 个二级索引全部非 occurred_at 前导，无索引可用即全表顺序扫，线性
  # 退化至被共享 PG 30s statement_timeout 成批击杀。分区父表三段式
  # （逐分区 CONCURRENTLY → ONLY 壳 → ATTACH，744 同法），不经 installer
  # （psql --single-transaction 容不下 CONCURRENTLY）；存量库由本通道 +
  # db.ensureUsageFactsOccurredAtIndex 双臂收敛（真库 ensure 回归 +
  # EXPLAIN 实证走索引）。
  "$ROOT_DIR/sql/migrations/startup/749_usage_facts_occurred_at_index.sql"
  # 2026-09-26 R68 24h 审计轮：750 usage_facts 按日分区函数 + 当日/次日
  # 预建——749 仅解决索引，partition pruning 仍不可用（无具体分区则 PG
  # 只能扫 DEFAULT 全表）；DEFAULT 保留作历史 catch-all，新一日数据走
  # 日分区。function 安装可走 installer（CREATE TABLE PARTITION OF 不需
  # 事务），同时由 db.ensureUsageFactsDailyPartition 在 boot 链兜底
  # （与 749 双通道收敛同款）。
  "$ROOT_DIR/sql/migrations/startup/750_usage_facts_daily_partition.sql"
  # 2026-09-26 R69 12h 审计轮：751 ensure_usage_facts_daily_partition
  # 时区钉扎（ALTER FUNCTION SET timezone）——750 的 DECLARE 初始化器
  # 边界转换 p_date::timestamptz 随会话时区求值，UTC 会话产出与
  # Shanghai 日边界错位 8h 的分区窗口；函数级 GUC 在函数入口生效、
  # 覆盖初始器（694 先例的对偶）。幂等 ALTER；boot 链
  # db.ensureUsageFactsDailyPartition 同语句双通道收敛。
  "$ROOT_DIR/sql/migrations/startup/751_usage_facts_partition_tz_pin.sql"
  # 2026-09-27 mock probe 生产入口收口轮：752 mock_probe_history 历史表 +
  # 按日分区函数——DDL 原死放 migrations/ 顶层（036）无投递通道（745 同款
  # 病），仅 252 被手工跑过，收编正典通道。相对 036 加固：函数级 SET
  # timezone 钉扎（751 对偶）+ move-then-attach（750 同款）。幂等，252
  # 存量库重放安全。Go 侧 ensure 点在 internal/mockprobe HistoryStore
  # （启动 bootstrap + writeLoop 每日 tick），无 db.go boot ensure。
  "$ROOT_DIR/sql/migrations/startup/752_mock_probe_history.sql"
  # 2026-09-24 supplier-protocol-optimization §3.2：800 provider_endpoint_
  # protocols 每 provider 多端点表 + 从 providers 旧行回填（ON CONFLICT
  # DO NOTHING 幂等）。原 deploy/sql/migrations/V800__*.sql 从未进任何
  # 存量库投递通道，本轮移入 startup 目录并在此登记（文件移动由并行代理
  # 完成，登记先行；无 Go boot ensure，本通道是升级库唯一投递路径）。
  "$ROOT_DIR/sql/migrations/startup/800_provider_endpoint_protocols.sql"
  # 2026-09-27 R67 session-storage 审计子任务 2：753 session_turn_logs
  # 可配 TTL——保留期在写入方（turn_logs_writer.go）按 settings_kv 的
  # lifecycle.session_turn_logs_ttl_hours 烘焙进 expires_at；清理函数
  # cleanup_session_turn_logs_by_ttl(p_ttl_hours, p_batch_size) 只做
  # 「到期即删」（expires_at < NOW()），每调用删一有界批（LIMIT 主键选批），
  # bg 侧循环消化积压（R72 审计轮修订；p_ttl_hours 是 [1,168] 越界 RAISE
  # 的 fail-closed 联锁，不参与谓词；不建任何索引——430:275 已有同列索引，
  # 初稿的重复索引已被批判式审计移除）。430 定义的旧清理函数全仓无调用方，
  # 本迁移是首次真正接上清扫，不是参数化既有行为。
  # 编号：模板原写 745，但 745/750/751/752 已被 report_snapshots /
  # usage_facts_daily_partition / usage_facts_partition_tz_pin（已 applied
  # 到 245 库）/ mock_probe_history 依次占用，故取当时首个空闲号 753。
  # 无 CONCURRENTLY（普通堆表，走 installer 单事务通道）；Go 侧调用点在
  # bg.PartitionManager.cleanupSessionTurnLogsByTTL（跑在 24h 的
  # archiveOldPartitionsIfNeeded tick 上），支持热重载。
  "$ROOT_DIR/sql/migrations/startup/753_session_turn_logs_ttl.sql"
  # 2026-09-27 R67 session-storage 审计子任务 7：754 request_logs 主表
  # archive 流水线——331 移除 archive_request_logs 整族后主表月分区一直
  # 只保留不归档；本迁移按 lifecycle.request_logs_ttl_days 把超出窗口的
  # 月分区摘要字段落进 request_logs_archive_YYYY_MM（丢弃 18 个大 JSONB
  # 列）。源分区不 DROP（R68 move-then-attach 纪律）。逐分区建表用
  # CREATE TABLE IF NOT EXISTS，函数 CREATE OR REPLACE，天然幂等；小批量
  # 1000 行 + 主键游标，无 CONCURRENTLY（走 installer 单事务通道）。
  # 编号：模板原写 746，已被 report_snapshots_internal_dims 占用；750~753
  # 亦已占用，故取复核时的首个空闲号 754。
  "$ROOT_DIR/sql/migrations/startup/754_archive_request_logs_default.sql"
  # 756 makes 754's id cursor index-backed on upgraded databases. Build at
  # a controlled migration window because the non-concurrent index takes a
  # write-blocking lock on existing request_logs partitions.
  "$ROOT_DIR/sql/migrations/startup/756_request_logs_id_index.sql"
  # 757/758 repair production read/write contracts. Both are idempotent and
  # also registered in the installer for fresh databases.
  "$ROOT_DIR/sql/migrations/startup/757_session_turns_origin_actor_projection.sql"
  "$ROOT_DIR/sql/migrations/startup/758_routeincident_missing_columns.sql"
  # 760/761/762 (2026-09-29/30 审计 R27/R28/R33，此前只在 installer 新库通道
  # 登记、漏掉本升级序列——R63 "迁移双轨制死区"教训的补登)：760 给
  # analysis_events / stats_event_inbox 补 TTL 部分索引；761 回填 stats inbox
  # processing_status（R28 HC-10 收口）；762 项目维度回填链（resolve/sync
  # 函数 + session_dim 双触发器 + 回填扫描部分索引，R33 P-3）。三者均幂等，
  # 且已在 installer embeddata 完成五点同步。
  "$ROOT_DIR/sql/migrations/startup/760_analysis_events_inbox_ttl_indexes.sql"
  "$ROOT_DIR/sql/migrations/startup/761_stats_inbox_sync_status_backfill.sql"
  "$ROOT_DIR/sql/migrations/startup/762_session_project_backfill_chain.sql"
  # 2026-09-30 R14 批判式复审轮：763 provider_events 契约对齐收编正典通道
  # （round11 D16 登记 → round14 部署窗手工执行的 parity 文件
  # deploy/sql/migrations/2026-07-26-provider-events-local.sql 从未进任何
  # 投递通道，752 收编 036 同款病）。幂等：CREATE IF NOT EXISTS ×3 + 幂等
  # ALTER + conname 守卫 PK + is_called 感知防回退 setval；存量 id 有重复时
  # PK 显式失败（fail-closed）。252/本机已手工修复，重放为 no-op 并补台账。
  "$ROOT_DIR/sql/migrations/startup/763_provider_events_contract.sql"
  # 2026-09-30 三十六轮 R36-B3：request_logs 分区家族补 (tenant_id, ts DESC)
  # 索引——341 只索引 hot 侧，分区父表从未有 tenant 前导索引，tenant 维度
  # days>7 聚合对每分区全表扫（252-dev 实测 3 行租户 6.5s / default 21.5s）。
  # 父表 CREATE INDEX IF NOT EXISTS 级联全部分区，重放 no-op 并补台账。
  "$ROOT_DIR/sql/migrations/startup/764_request_logs_tenant_ts_index.sql"
  # 765 (2026-09-30, R16 存储轮): bodies 月分区列存化 + lz4 TOAST +
  # request_stage_events 三死索引（pss 19 天 0 扫描）。
  "$ROOT_DIR/sql/migrations/startup/765_bodies_columnar_storage.sql"
  # 2026-09-30 补登 800/801 升级通道（ec5edcfd7 只落了 installer StartupFiles，
  # 序列通道缺席 → 目录驱动门报「startup migration 801 exists but is missing
  # from the channel files=(...) array」，正是该门注释里点名的 693/699/701/703
  # 复发形态：fresh install 有、存量库升级永远不落地）。
  # 800：每 provider 多端点表 + providers 旧行回填，CREATE TABLE IF NOT
  # EXISTS + ON CONFLICT DO NOTHING 幂等。
  # 801（原 759，撞号让位后改号）：只 CREATE OR REPLACE 733 建的
  # promote_session_turn_details_hot_to_partition，不跑批量 DELETE、不改
  # 存量分区，重放安全。位置约束同 StartupFiles：必须晚于 733（其目标函数
  # 依赖 733 建的 session_turn_details 表与 hot 表），故排在序列末尾。
  "$ROOT_DIR/sql/migrations/startup/800_provider_endpoint_protocols.sql"
  "$ROOT_DIR/sql/migrations/startup/801_session_turn_details_duplicate_drain.sql"
  # 2026-09-30 会话存储解耦 v3 S4 前置（802）：session_turn_details 族补
  # (tenant_id, gw_task_id) 部分索引（母表 + hot）。跨租户访问门
  # assertTaskInTenant 的 session 族腿依赖它——缺索引时 EXISTS 判定会对
  # 167 万行母表做顺序扫描。幂等：CREATE INDEX IF NOT EXISTS ×2 + 两条
  # COMMENT ON（重复执行覆盖注释，无副作用）。位置约束同 StartupFiles：
  # 必须晚于 733（建表与分区）与 801（同族），故排在序列末尾。
  "$ROOT_DIR/sql/migrations/startup/802_session_turn_details_gw_task_id_index.sql"
  # 2026-10-01 fresh-install e2e 轮（803-806，canonical 收编四件）：首批登
  # 记进本清单——本轮实证它们此前只进 installer StartupFiles，存量库的
  # sequence 通道是断链的（fresh-install e2e 轮 §五.1「跑脚本会顺带应用 803-806」与当时
  # 的清单不符）。
  #   803 candidate_failure_logs hot 列对账 / 804 credential_model_bindings
  #   context_window 列：均 ALTER TABLE IF EXISTS + ADD COLUMN IF NOT
  #   EXISTS，存量库 no-op。
  #   805 session_dim 全形：CREATE TABLE IF NOT EXISTS + 索引 IF NOT EXISTS
  #   + RLS policy DROP IF EXISTS+CREATE（按生产真库 2026-10-01 实测形态
  #   重建，存量库语义不变）。位置约束（fresh 链内须先于 683）只约束
  #   installer 通道；本清单的服务对象是存量库，session_dim 皆已存在。
  #   806 session_bodies columnar 分区转 heap：仅对「非 heap 且为空」分区
  #   动手；生产/本机 session_bodies 分区全 heap，循环为空集零动作。位置
  #   约束：晚于 708（其 S1A 结构），已满足。
  "$ROOT_DIR/sql/migrations/startup/803_candidate_failure_logs_hot_column_reconcile.sql"
  "$ROOT_DIR/sql/migrations/startup/804_credential_model_context_window_columns.sql"
  "$ROOT_DIR/sql/migrations/startup/805_session_dim_reconcile.sql"
  "$ROOT_DIR/sql/migrations/startup/806_session_bodies_partitions_heap.sql"
  # 2026-10-01 审计十七轮（807）：request_logs_bodies_hot 删除被同列 UNIQUE
  # 索引 idx_request_logs_bodies_hot_request_id（455/678 建，承重
  # ON CONFLICT (request_id) upsert）全量影蔽的冗余普通索引。DROP INDEX
  # CONCURRENTLY 不允许在事务块内执行——文件带 dbinit:no-transaction 标记
  # 走非事务通道；幂等（IF EXISTS + NOTICE skipping）。位置约束：晚于
  # 455/678（保留侧创建者），排在序列末尾。
  "$ROOT_DIR/sql/migrations/startup/807_request_logs_bodies_hot_drop_duplicate_request_id_index.sql"
  # 2026-10-02 24h 审计第二十八轮（612/808/809，canonical 收编三件）：
  # 本轮独立复审发现 809 接线时未登记本清单——预提交门「最高编号必须在
  # files 数组」红了 60 个提交（提交方绕过）；且该守卫只盯最高编号，
  # 612/808 两条静默漏网（同 803-806 批注的断链形态：只进 installer
  # StartupFiles，存量库的 sequence 通道拿不到）。三条对存量库均可重放：
  #   612 native_responses_capability：CREATE TABLE IF NOT EXISTS 幂等。
  #   808 request_logs DEFAULT 分区兜底：存在性检查 + NOTICE skipping。
  #   809 instance_release_status nullable release_id：DROP NOT NULL +
  #     偏索引 IF NOT EXISTS，重放 no-op。
  # 534 亦同批收编但位次受 clobber guard 约束——其 ensure_handoff_logs_partition
  # 与 714 同名异体，必须先于 714（见 714 条目上方）；见 710/714 之间。
  "$ROOT_DIR/sql/migrations/startup/612_native_responses_capability.sql"
  "$ROOT_DIR/sql/migrations/startup/808_request_logs_default_partition.sql"
  "$ROOT_DIR/sql/migrations/startup/809_instance_release_status_nullable_release_id.sql"


  # 2026-10-02 SQL 日志审计二十轮（810）：治愈「heap 但无 TOAST 表」的空分区
  # （10-01 列存事故回退/并行轨道往返实验遗留；session_bodies_2026_10 上
  # promote row-too-big ×59/14h 实证）。仅动空分区（count=0），非空分区
  # NOTICE 跳过指路 806/562 通道；幂等（治愈后扫描为空集）。
  "$ROOT_DIR/sql/migrations/startup/810_heap_partitions_toastless_heal.sql"

  # 2026-10-02 SQL 日志审计二十轮（811）：request_logs / routing_decision_log
  # 分区边界 473 型 UTC 零点污染重建为正典 +08 零点网格（687 姊妹篇；
  # 252 生产 overlap ×8、ensure 2026_11 永远建不出来、11-01 写入时间炸弹
  # 实证）。存储引擎跟随原分区（rdl=columnar 单族）；bak 两遍回灌计数
  # 守恒；幂等（干净边界跳过）。位置约束：晚于 694（时区钉扎正典）。
  "$ROOT_DIR/sql/migrations/startup/811_partition_bounds_shanghai_midnight_repair.sql"

  # 2026-10-02 SQL 日志审计二十轮（812）：model_probe_runs 空列存分区转
  # heap——探针状态更新器 CTID ×114 家族真根因（UPDATE 计划含 ColumnarScan
  # 即被 citus 拒绝，与目标表 AM 无关；R19 归因补全）。三个月分区全 0 行
  # 空壳（活数据在 model_probe_runs_hot=heap），806 同款仅动空分区；幂等。
  "$ROOT_DIR/sql/migrations/startup/812_model_probe_runs_partitions_heap.sql"

  # 2026-10-02 SQL 日志审计二十一轮（813）：supplier_errors 族 AM 归一
  # heap——R20 §五.2「Row-level DELETE 11-06 必炸」经复核为误登记（TTL
  # 实为 689 helper 整分区 DROP，列存安全），但列存残留仍是正典单族之外
  # 的漂移：UPDATE/DELETE/tableoid 读毒面 + ensure 函数 ELSE 分支每小时
  # enforce_columnar_partition 自愈反噬环。两步齐做（689 同款）：先换
  # ensure 函数为 heap 版，再转四分区（空壳 812 通道 / 带数据 689 通道，
  # 2026_09=124,846 行实测）；幂等。三基线函数体同步（新环境从源头 heap）。
  "$ROOT_DIR/sql/migrations/startup/813_supplier_errors_partitions_heap.sql"

  # 2026-10-02 SQL 日志审计二十一轮（814）：v_adaptive_probe_targets
  # recent_passive_failures 死列修复（R21 N2 拍板）——子查询改读
  # candidate_failure_logs_hot（5min 窗 ⊂ 8h promote 保留窗 ⇒ hot 是
  # 精确源且走复合索引；分区父表侧行集恒空=该列自 038 起恒 0）。
  # CREATE OR REPLACE VIEW 列集不变；幂等。三基线+objects 视图同步。
  "$ROOT_DIR/sql/migrations/startup/814_adaptive_probe_targets_hot_subquery.sql"

  # 2026-10-02 s4-audit §9.22（815）：canonical 视图补 origin_stage /
  # token_band / client_forwarded_for 三尾列——token_band 是
  # admin/compression_stats.go 分带聚合的既有读点（此前每调 42703 被
  # 吞掉、仪表盘静默为空）。顶层 view 全量重建为 118 列契约；幂等。
  # 三基线+installer 五点同步（815 曾漏登本通道=守卫红，见 f7f1c97f4）。
  "$ROOT_DIR/sql/migrations/startup/815_request_logs_view_stage_band_cff.sql"

  # 2026-10-02 s4-audit §9.46（816）：canonical 视图 client_ip 从 NULL
  # 补位改为 session 侧有源投影（§9.60.6.1 裁决更正落地；252 生产复测
  # 826/826 同值）。【注释订正 R33 域A P3-1：本条原写「CASE 守卫 ::inet 转换
  # 防畸形值打挂全读方」——已被同日 817（§9.64）证伪：字符类
  # ^[0-9a-fA-F:.]+$ 放行 192.168.1/deadbeef 等合法字符集、非法语义值，
  # ::inet 22P02 照打挂全读方；真守卫是 817 的 pg_input_is_valid，816 在
  # 视图链上只是中间形态】幂等。
  # 三基线+installer 五点同步（816 又漏登本通道=探门红，本轮补齐）。
  "$ROOT_DIR/sql/migrations/startup/816_request_logs_view_client_ip_projection.sql"

  # 2026-10-02 s4-audit §9.64（817）：canonical 视图 client_ip 守卫从字符类
  # 正则换语义守卫 pg_input_is_valid(v,'inet')——816 的 ^[0-9a-fA-F:.]+$ 放行
  # 192.168.1/deadbeef/::: 等「合法字符集、非法语义」值，::inet 22P02 打挂
  # 整条视图每读方（恰是 816 声称要防的事故形态）。写侧 ParseIP 门只保新行。
  # 幂等；三基线+installer 同步（817 又漏登本通道=第五次同类遗漏，本轮补齐）。
  "$ROOT_DIR/sql/migrations/startup/817_request_logs_view_client_ip_semantic_guard.sql"

  # 2026-10-02（818）：ursm_node_snapshot_min payload 字段拆分——31 个 hash 键
  # 提升为 typed 列（编号 817→818 避让并行会话迁移撞车，见 7fc0fb879）。幂等
  # 守卫形态；installer 五点已同步（597f65032）但通道腿漏登=第六次同类遗漏
  # （R35 审计 B-F1 实证：本通道数组止于 817，已升级库永远收不到 818/819）。
  "$ROOT_DIR/sql/migrations/startup/818_ursm_snapshot_typed_columns.sql"

  # 2026-10-05 补登 820（与末尾 825-833 一批补登同轮发现）：存量音频模型
  # （ASR/TTS）modality 回填 audio。四个 UPDATE 分别覆盖后缀形态（*-asr /
  # *-tts）、包含形态（*-asr-* / *-tts-* / *-stt-*）、whisper 家族、transcribe
  # 家族。
  #
  # 为什么必须进通道而不只是留在台账：本迁移是**纯数据回填**，只把
  # models_canonical 里 modality='text' 的行升级成 'audio'。已跑过的库不需要
  # 再跑，但**任何从更早快照升级上来的库都需要它**——否则 audio 请求会被
  # 候选过滤（COALESCE(mc.modality,'text') IN ('audio','multimodal')）挡掉，
  # 表现为 503 no_candidate，而上游本身完全健康（小米端点 chat+input_audio
  # 实测可用）。这是纯粹的标注错误，不是能力缺失。
  # 幂等：四段都是 `WHERE modality='text'` 收敛式 UPDATE，重复执行不改变结果。
  "$ROOT_DIR/sql/migrations/startup/820_audio_modality_backfill.sql"

  # 2026-10-03（821）：session_turns 补 is_abandoned 标记列——「开始了却没终态」
  # 在**会话族内**的落点（审计 §9.92，取代被推翻的 819 独立表方案）。
  # 252 实测遗弃率 0.047%/天，丢的是已发生的上游消耗。
  # 不变式：is_abandoned=TRUE ⇔ 这是终态 turn，而其请求在 v1 侧无 t0。
  # 写方（telemetry markAbandonedTurn）fail-open，故本迁移未应用时只是标记缺失。
  # 幂等（ADD COLUMN IF NOT EXISTS + 列集守卫）；母表与 hot 两面都要有。
  "$ROOT_DIR/sql/migrations/startup/821_session_turns_abandoned_marker.sql"

  # ⚠ 本条注释原先写作「（820）」，是 819→820→821 重编号时漏改的错标：820 是
  # 上面的 audio_modality_backfill，与本条无关。2026-10-05 订正。

  # 2026-10-04（822）：session_summaries 健康分待评分捞取查询的部分索引
  # （252 SQL 日志审计 R23）。bg/session_health_worker（10-02 ece68f148 接入
  # 生产）每 60min/实例 tick，无索引支撑时对 58.6 万行表 Parallel Seq Scan
  # +Sort，252 真库实测 25.8-29.4s/次（>1s 慢日志 24h 28 条）。
  # 带 dbinit:no-transaction 标记（CREATE INDEX CONCURRENTLY）；幂等。
  # ⚠ 失败恢复（R41 审计 F7 订正，2026-10-04）：CONCURRENTLY 中断留下 INVALID
  #   索引时，CREATE INDEX CONCURRENTLY IF NOT EXISTS 只会对同名关系发 NOTICE
  #   跳过，**复跑不会清掉 INVALID**。恢复步骤是先
  #   DROP INDEX CONCURRENTLY IF EXISTS idx_session_summaries_health_pending
  #   再重放本迁移（迁移头注释同步口径）。
  "$ROOT_DIR/sql/migrations/startup/822_session_summaries_health_pending_index.sql"

  # 2026-10-04（823）：session_turns/session_turns_hot 两侧同名加 request_status
  # 列——把网关已算好的生命周期标签（success|failure|rate_limited|in_progress）
  # 落进会话族；rate_limited 不可由 success/error_kind 推导（立项理由见迁移头
  # ）。幂等（ADD COLUMN IF NOT EXISTS + hot/parent 列集全等自检）。存量回填
  # 由后台作业（session_request_status_backfill）负责，不在本通道。
  "$ROOT_DIR/sql/migrations/startup/823_session_turns_request_status.sql"

  # 2026-10-04（824）：canonical 视图会话腿 request_status 改由 error_kind
  # 判定——status_code=429 臂在 session_turns 上是死代码（0 行），437,402 条
  # 真限流被报成 failure（§9.160）。幂等判据：viewdef 含 rate_limit_exceeded
  # 字面量即 no-op（R41 冻结前订正：判定臂为 IN ('rate_limit_exceeded',
  # 'key_throttled')，两字面量都来自写侧 EmitRateLimited，见 R41 审计 F1）。
  # 依赖 817 链形与 session_turn_details 族，缺链时 NOTICE 跳过由 db.ensure 重建。
  "$ROOT_DIR/sql/migrations/startup/824_request_status_rate_limited_projection.sql"

  # 2026-10-05（829，Owner 拍板登记）：request_logs_bodies 从 Citus columnar
  # 退回全 heap 的止血迁移。此前它是「有文件、无通道」——startup 通道数组里
  # 没有它，`apply-db-revision-sequence_test.sh` 的目录驱动不变式因此常驻红
  # （R44 移交第 2 项）。三臂里只有 embeddata/startup 副本是齐的，通道腿缺失。
  #
  # 为什么必须走通道而不是继续挂着：829 的头号作用是把
  # ensure_request_logs_bodies_partition 重定义为**纯 heap**。只要这行改动不
  # 下发，**新建的月分区就继续被建成列存**，而 §9.197.4 实测过 9 个列存分区
  # 的 UNION ALL 子查询计划 **7/7 全部失败**（invalid perminfoindex 0 in RTE
  # with relid 0），同形状的全 heap request_logs 正常返回 218 万行。也就是说
  # 「不登记」不是一个中性的等待态，它是在持续生产新的故障面。
  #
  # 为什么这条可以安全进通道：幂等（CREATE OR REPLACE / IF EXISTS / DO 守卫，
  # 可安全重放）；**不重写任何数据**——只转换**空**分区，非空分区一律
  # `RAISE NOTICE` 保留并交给 TTL 按月 DROP 消化；锁在 ACCESS EXCLUSIVE 下
  # 复核空值，避免与 promote 并发窗口互踩。代价是 request_logs_bodies 放弃
  # 列存压缩、新分区按 heap 建，存储占用会上升——这是已知的、被接受的代价。
  #
  # ⚠ 与 830 的 manual-by-design **不是同一类**（R44 明确区分）：830 之所以不
  # 登记，是因为它的 RENAME + CREATE PARENT TABLE 那一半会原地改名一张 10GB+
  # 的活表，**无人值守的升级不该跑它**。829 不改名、不搬数据，所以不具备
  # 「无人值守不可跑」的性质。不要拿 830 的理由来推迟 829。
  # ── 2026-10-05：825-828 补登通道腿（R33 复发族的下一次实例）──
  #
  # 这四条带着**完整的 installer 腿**（go:embed + StartupFiles map，
  # installer/cmd/llm-gw-installer/main.go:765-774 / 1008-1011）落地，却
  # **没有通道腿**。后果不是报错，是静默不投递：**全新安装会拿到它们，
  # 任何在它们之前安装的库永远拿不到**——新装与升级从此分叉。
  #
  # 这正是 R33 在本文件里点名并要求「deliberate, commented edit」的那一类
  # （693/699/701/703 → 815/816 → 817 → 现在 825-828）。R44 记的「通道门
  # 只红在 829 一条」是**首个失败点**，不是**唯一问题**：门在第一个失败处
  # exit 1，829 挡住了 819，819 挡住了 830，830 挡住了 825-828。一次修一个
  # 只会一层层揭开。
  #
  # 为什么可以安全进通道：它们跑的就是 installer 今天在全新安装时已经跑过的
  # 同一批 SQL——不是新的代码路径，只是把同一条路径接到升级上，让升级库与
  # 新装库对齐。这正是通道存在的意义。
  "$ROOT_DIR/sql/migrations/startup/825_modality_graded_verification.sql"
  "$ROOT_DIR/sql/migrations/startup/826_model_baseline_price.sql"
  "$ROOT_DIR/sql/migrations/startup/827_modality_verification_progress_view.sql"
  # 2026-10-06（834）：把 supplier_errors 族三张基表纳入受追踪链。
  # 编号在 828 之后但**位置在它之前**——这个数组按书写顺序执行，而 828 的
  # CREATE VIEW 是真校验（plpgsql 函数体才被 check_function_bodies=off 放过），
  # 缺表即硬失败。实测：缺本条时 828 报
  # `relation "public.supplier_errors_hot" does not exist`。
  # 补迁移而不是登记 startup_known_gaps.tsv 豁免：那份文件的头记录了上一轮
  # 19 条缺口的修法正是「把缺失的迁移加进受追踪链」，豁免只是记欠条。
  "$ROOT_DIR/sql/migrations/startup/834_supplier_errors_base_tables.sql"
  "$ROOT_DIR/sql/migrations/startup/828_supplier_errors_unified_tracked.sql"

  # 2026-10-05：831 补登通道腿（本轮合并期间由并行线落地，第 N 次同型）
  #
  # 与 825-828 完全同型：work_type_model_route.source 列（区分 ACC 拥有与运维
  # 拥有，让 ACC 同步不再抹掉运维在 UI 配的模型路由）带着完整 installer 腿落地
  # （embeddata 副本 + StartupFiles map），却没有通道腿 ⇒ 全新安装拿得到，
  # 在它之前装的库永远拿不到。而这列存在的**全部意义**就是让同步不再抹掉运维
  # 配置，缺了投递腿等于这个修复对老库不生效。
  #
  # 可安全进通道的依据：纯 ADD COLUMN IF NOT EXISTS + CHECK 重建 + CREATE INDEX
  # IF NOT EXISTS + 双账本自登记（ON CONFLICT DO UPDATE），幂等可重放；表只有
  # 22 行；不定义任何函数（无 clobber 链问题）、不 RENAME、不重写大表。
  # 文件自带 BEGIN/COMMIT 与通道内 71 个同类文件一致，psql --single-transaction
  # 下只发 WARNING，不致命。
  "$ROOT_DIR/sql/migrations/startup/831_work_type_route_source.sql"

  "$ROOT_DIR/sql/migrations/startup/829_bodies_columnar_rollback.sql"

  # 2026-10-04（832）：基准价观察源健康台账
  # model_baseline_price_observation_health（连续失败数 / 观察到的模型数）。
  # 缺失时 bg 的 baseline_observation_stale 检查报 42P01。
  # 编号注：本文件落地时占用 830，与并行线的 830_ursm_node_snapshot_min_
  # partitioned 撞号；编号是身份键（de7656806），2026-10-05 合并收口时重排
  # 为 832。幂等：CREATE TABLE IF NOT EXISTS。
  "$ROOT_DIR/sql/migrations/startup/832_model_baseline_observation_health.sql"

  # 2026-10-04（833）：供应商侧四个价格列加非负 CHECK，关掉「负价录入」这个
  # 入口（负价进 v_supplier_price_vs_baseline 会被读成「比原厂便宜 100%」）。
  # 编号注：原占 831，与并行线的 831_work_type_route_source 撞号，同批重排
  # 为 833。幂等 DROP + ADD。
  "$ROOT_DIR/sql/migrations/startup/833_supplier_price_nonneg_check.sql"

  # 2026-10-06（835）：把 system_probe_runs 的 task_type 词表放宽一个值
  # （modality_verify），让多模态定时核实的每一次尝试进自检台账。在它之前，
  # 核实循环是唯一一条「在跑但运维查不到」的链路：语义探针经
  # internal/upstreamurl 直连上游、不产生 request_logs，进程内尝试台账又随
  # 重启清零。编号注：834 悬空 —— 828（supplier_errors_unified 视图）的底表
  # 只在未受追踪的 deploy/sql/migrations/V371 里定义，受追踪链与基线都不建，
  # 而 828 排在 834 之前 ⇒ 一条编号在后的迁移救不了它。修法待裁决，故 835
  # 取下一个可用号。幂等：DROP CONSTRAINT IF EXISTS + ADD。
  "$ROOT_DIR/sql/migrations/startup/835_modality_verify_probe_ledger.sql"
  # 2026-10-06（836）：重新断言 supplier_errors 族的最终形态（813 的 re-assert）。
  # 根因：813 的文件 sha 与台账存的完全一致 ⇒ 幂等通道每次都跳过它，活库的
  # 列存漂移（3 个分区 + ensure 函数 + 盲掉的 columnar_healthcheck）永远没人修。
  # 本条无台账行 ⇒ 每次部署都跑；自身幂等。必须排在 813 之后。
  "$ROOT_DIR/sql/migrations/startup/836_supplier_errors_heap_reassert.sql"

  # ── 2026-10-07：837-841 补登升级通道腿（R33 同型第 7 次）──
  #
  # 这五条带着完整的 installer 腿落地，却没有通道腿 ⇒ 全新安装拿得到，
  # 在它们之前装的库永远拿不到。契约门连续报红（6 条：5 个迁移各报
  # 「有 installer 腿无升级路径」+ 841 额外触发「最高编号必须在 files 数组」）。
  #
  # ★ 为什么这五条**不是** installer-only by design（因此不进 channel_gap_allowlist）：
  # 上一轮刻意留红，理由是「选哪个取决于设计意图，无从判断」。本轮查证结论是
  # **它们早已在生产 applied 并验证过**，不是「只在新装库跑过」：
  #   · 838  analyze_llm_gateway_table_stats 函数体 2142 B（838 前值 1490）
  #   · 839-B 库函数含 opts_sql_base 与 0.005；当月 11 族分区 heap 5 个 @0.005
  #   · 840  llm_gateway_task_state 存在，最早一行 last_started_at=2026-10-07 15:22:57
  #   · 841  两个函数与两张表均存在；retention 表 0 行、drop_log 0 行、
  #          expired_month_partitions() 返回 0 行 ⇒ 未删任何分区或数据
  #   证据：docs/db-changelog.md「2026-10-07T09:52Z 更正」节（17:52 只读核验）。
  # ⇒ 手动部署已经发生，登记通道只是把**已经生效**的形态补成无人值守可达，
  #   不是新增风险面。这正是通道存在的意义（825-828 / 831 同型）。
  #
  # ⚠ 判据与 830 的区别：830 之所以不登记，是因为它的 RENAME + CREATE PARENT TABLE
  # 会原地改名一张 10GB+ 活表，**无人值守升级不该跑它**。这五条全部不满足那一条：
  # 837 只重建两个 7 天窗口的物化视图（且 db.go 的 Go-ensure 已在每次启动用
  # 「refreshed_at 不得存在」这条负控强制收敛，SQL 腿只是把同一形态补到新装库）；
  # 838/839 只 CREATE OR REPLACE 一个函数体；840/841 只建表与函数，均不重写业务数据。
  "$ROOT_DIR/sql/migrations/startup/837_routing_mv_refresh_state.sql"
  # 838 → 839 必须成对且有序：839 重建的正是 838 那个函数体（839 把当月**堆**分区
  # 交回 autovacuum），两者都要在通道里，否则升级库会停在中间形态。
  "$ROOT_DIR/sql/migrations/startup/838_analyze_skip_frozen_month.sql"
  "$ROOT_DIR/sql/migrations/startup/839_autovac_current_month_heap_handoff.sql"
  # 840：跨实例共享的 analyze 节流槽。与 §10.53 的 advisory 锁正交，删掉这两个函数
  # 后 Go 侧对 42P01 做降级 ⇒ 行为退化为「两台各跑一遍」（回到 10-07 上线前）。
  "$ROOT_DIR/sql/migrations/startup/840_analyze_stats_throttle_slot.sql"
  # 841：月度分区族的分区级 DROP 保留。**建表即为空 ⇒ 迁移应用后行为与今天完全一致**，
  # 真正启用需按族 INSERT 保留月数（业务决定，不在本迁移内）。
  "$ROOT_DIR/sql/migrations/startup/841_monthly_partition_retention.sql"
  # 842（2026-10-07 审计轮补登，837-841 同族复发后一小时）：给 autoroute
  # latest_bucket 聚合补 (credential_id, raw_model, bucket) 索引（runbook
  # §10.106.26）。正常升级通道：部署扫描腿本就会按目录+台账投递，登记是
  # 元数据补全；落 main（cbce70a60，自称未部署）时又未登记——最高编号
  # 守卫按设计拦下。
  "$ROOT_DIR/sql/migrations/startup/842_credential_model_index_latest_bucket_idx.sql"
  # 2026-10-07 §10.106.28：candidate_failure_logs 两臂补 (ts DESC) 索引。
  # 该族无任何 ts 打头的索引 ⇒ 无过滤的 max(ts) 只能全量扫索引条目
  # （父表 cost 4566 / hot 171，26 倍差），而告警语句 58,001 次调用。
  # 本地同形状实测：加索引前读 55,000 行 2.736ms，加后读 1 行 0.036ms。
  # 正常升级通道：部署扫描腿按目录+台账独立投递，登记是元数据补全。
  "$ROOT_DIR/sql/migrations/startup/843_candidate_failure_logs_ts_desc_idx.sql"
)

# 2026-09-21 内容指纹重放通道（纪律⑨，F4 机制债收口）：当某个"已应用"的
# 迁移文件内容被加固（幂等守卫重建、canonical 清单扩充等）而编号不变时，
# 仅按文件名记账的台账永远不会重放它——644 的 self_check_runs CHECK 就因此
# 在 252 上停在 09-03 旧版清单，运行时写新类别全被 23514 拒绝（2026-09-21
# 复核轮 F4，当时靠手工重放解围）。本清单登记"内容已加固、必须在下次部署
# 重放"的文件，格式 "basename|当前内容 sha256"：
#   - 台账行 sha 为 NULL（遗留记账）且文件在清单中 → 按当前内容重放一次，
#     然后台账记录 sha，此后进入常规指纹管理；
#   - 台账行 sha 非空且与文件当前 sha 不一致 → 内容在应用后又变更，自动
#     重放（幂等契约兜底安全性）；
#   - 台账行 sha 与文件一致 → 正常跳过。
# 清单条目与文件内容由契约测试（apply-db-revision-sequence_test.sh）核对，
# 文件再改动而条目未同步时门禁变红——白名单自清洁，与 sqlreadguard 同款。
legacy_content_replays=(
  # 2026-09-21 复核轮 F4：§C DO 块重建 self_check_runs CHECK 为 canonical
  # errorsx 类别清单（09-03 应用的是缺 concurrent 等类别的旧版）。文件自带
  # definition-aware 守卫（canonical CHECK 已在位时不再重跑 ADD CONSTRAINT），
  # 重放对已修复库是幂等 no-op，对未修复库补齐加固。
  '644_tuning_views_selfcheck_and_candidate_failure_cache.sql|5dc5731fb58af65039a26536d7867ffba55c77169d0e5cc4db84aa1e0ea4553c'
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
  # 829 (2026-10-05, Owner 拍板补登通道腿) 重定义
  # ensure_request_logs_bodies_partition 为**纯 heap**，去掉 765 引入的
  # citus_columnar/USING columnar 分支。链序必须是 694 → 765 → 829：
  # 694 是 Asia/Shanghai 时区钉扎版，765 叠加列存分支，829 撤掉列存分支，
  # **829 必须是最后一项**——它是活库里应有的最终体。少了这一行，
  # 部署会在应用任何文件之前就 abort（exit 5），因为一个函数被三个
  # 通道文件重定义而未登记为有意链。
  'ensure_request_logs_bodies_partition|694_partition_ensure_timezone.sql|765_bodies_columnar_storage.sql|829_bodies_columnar_rollback.sql|'
  # 534 (24h 审计第二十八轮 canonical 收编) 引入 ensure_handoff_logs_partition
  # 的 columnar 体；714 的 handoff_logs 腿是同一函数的 Asia/Shanghai 时区钉扎版
  # （DECLARE 初始化器移入函数体）。714 必须保持为后项——与 699/703 的
  # V371-track 同型：钉扎版必须是活库里的最终体。
  'ensure_handoff_logs_partition|534_handoff_logs_hot_columnar.sql|714_partition_timezone_pin_remaining.sql|'
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
  # 739 (R56) re-derives the body on top of 698 adding credits_rate_multiplier
  # to the three explicit column lists; 739 must stay the later entry.
  'promote_request_logs_hot_to_partition|695_request_logs_promote_final_success_self_heal.sql|697_request_logs_promote_system_fingerprint.sql|698_promote_hot_partition_timezone_pin.sql|739_promote_functions_rate_multiplier.sql|'
  # 699 re-pins ensure_supplier_errors_partition (V371 deployed the original
  # out-of-repo-family body) to Asia/Shanghai; the pin must stay the later entry.
  # 813 (R21 SQL-log audit round) re-derives the body dropping columnar
  # (supplier_errors partitions become heap; TTL is whole-partition DROP so
  # columnar has no read benefit); 813 must stay the later entry — landed
  # without this registration and failed the canonical delivery gate on the
  # merge with 第三十轮 (2026-10-02).
    # 836 (R26 re-assert round) 再次断言同一函数的 heap 体。**必须排在最后**：
  # 813 自身的台账 sha 与文件一致，幂等通道永远跳过它，所以链里名义上的
  # 「最后一项」在活库上从未执行过 —— 而那个守卫只校验登记、不校验执行。
  'ensure_supplier_errors_partition|V371__supplier_errors_hot_and_stats.sql|699_supplier_errors_ensure_timezone_pin.sql|813_supplier_errors_partitions_heap.sql|836_supplier_errors_heap_reassert.sql|'
  # 703 re-pins promote_supplier_errors_hot_to_partition month grouping to
  # Asia/Shanghai on top of V371's original body; the pin must stay the later
  # entry (same V371-track pattern as 699; 703 landed without this
  # registration and aborted every later deploy at the pre-flight guard,
  # 2026-09-14 deploy-local incident).
  'promote_supplier_errors_hot_to_partition|V371__supplier_errors_hot_and_stats.sql|703_supplier_errors_promote_timezone_pin.sql|'
  # 801（原 759 撞号让位后改号）以有界无损版本重定义 733 建的
  # promote_session_turn_details_hot_to_partition：733 的 parent anti-join 在
  # backfill 已插入同一 request key 时滞留旧 hot 行。801 必须保持为后项。
  'promote_session_turn_details_hot_to_partition|733_session_turn_details.sql|801_session_turn_details_duplicate_drain.sql|'
  # 705 rewrote ensure_request_logs_partition as the attached-aware body
  # (detached-shell re-attach + default-gap self-heal) on top of 694's
  # timezone-pinned body; the rewrite must stay the later entry. 705 landed
  # without this registration — same pre-flight guard abort class as 703
  # (caught 2026-09-14 when S1a files joined the sequence).
  'ensure_request_logs_partition|694_partition_ensure_timezone.sql|705_request_logs_reattach_detached_partitions.sql|'
  # 2026-10-07（838 → 839）：同一个函数 analyze_llm_gateway_table_stats 被两条
  # 通道文件先后重定义。838 让往月分区「只在从未被分析过时补」（省掉整趟 pass 的
  # 25.7%）；839 进一步把当月**堆**分区交回 autovacuum 并下调其 scale_factor 到
  # 0.005，同时**保留**对列存分区与首次覆盖的强制分析。
  # ★ 839 必须是最后一项——它是活库里应有的最终体（生产实测已 applied 839-B）。
  # ⚠ 注意：db/db.go ensurePartitionAutovacuumSchema 还有一份内联副本，每次启动
  #   CREATE OR REPLACE 同名函数；它不在通道文件里，故不进本链，但必须与 839 同步
  #   （839 的 .down.sql 头已把这条写成回滚判据）。
  'analyze_llm_gateway_table_stats|838_analyze_skip_frozen_month.sql|839_autovac_current_month_heap_handoff.sql|'
  # 765 (R16 bodies columnar round) redefines 694's ensure_request_logs_bodies_partition
  # so new month partitions are created USING citus_columnar when the extension is
  # present (heap fallback otherwise); 765 must stay the later entry. 765 landed
  # without this registration and aborted every later deploy at the pre-flight
  # guard (caught 2026-10-01 during the R35-N1 critical-review deploy).
  'ensure_request_logs_bodies_partition|694_partition_ensure_timezone.sql|765_bodies_columnar_storage.sql|'
  # 659 rewrote these seven promote bodies as the single atomic CTE form;
  # 688 later aligned their defaults to the Go scheduler (not in this array,
  # so invisible to the scanner) and 698 re-derives each body from the
  # sql/objects/ canonical (byte-identical to 688's live body, verified
  # per-function) plus the Asia/Shanghai pin. 698 must stay the later entry.
  'promote_tool_usage_stats_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_credit_ledger_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  'promote_request_logs_bodies_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|'
  # 739 (R56) re-derives the body on top of 698 adding rate_multiplier to the
  # three explicit column lists; 739 must stay the later entry.
  'promote_usage_ledger_hot_to_partition|659_legacy_promote_atomic_cte.sql|698_promote_hot_partition_timezone_pin.sql|739_promote_functions_rate_multiplier.sql|'
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
  base="$(basename "$file")"
  marker="${sequence_name}:${base}"
  file_sha="$(sha256_file "$file")"
  # 本文件在 legacy_content_replays 中登记的期望指纹（格式严格的
  # "basename|sha" 全等匹配；登记过期由契约测试先红）。
  replay_expected_sha=""
  for entry in "${legacy_content_replays[@]}"; do
    if [[ "$entry" == "${base}|${file_sha}" ]]; then replay_expected_sha="$file_sha"; break; fi
  done
  stored_sha="$(psql_query "SELECT content_sha256 FROM public.gateway_db_revision_sequences WHERE sequence_name='${marker}' LIMIT 1")"
  if [[ -n "$stored_sha" ]]; then
    if [[ "$stored_sha" == "$file_sha" ]]; then
      printf 'already applied: %s\n' "${file#"$ROOT_DIR/"}"
      continue
    fi
    # 台账已记指纹且与当前文件不同：文件在应用后被内容加固过。通道契约
    # （每个迁移幂等）兜底重放安全性，重放使库状态收敛到当前文件内容。
    printf 'content replay (ledger %.12s -> file %.12s): applying %s\n' "$stored_sha" "$file_sha" "${file#"$ROOT_DIR/"}"
    psql_file "$file"
    psql_query "UPDATE public.gateway_db_revision_sequences SET content_sha256='${file_sha}' WHERE sequence_name='${marker}'"
    continue
  fi
  if [[ "$(psql_query "SELECT 1 FROM public.gateway_db_revision_sequences WHERE sequence_name='${marker}' LIMIT 1")" == 1 ]]; then
    if [[ -n "$replay_expected_sha" ]]; then
      # 遗留记账（指纹通道上线前应用）且登记在重放清单：按当前内容重放
      # 一次，之后进入常规指纹管理。
      printf 'content replay (legacy, registered): applying %s\n' "${file#"$ROOT_DIR/"}"
      psql_file "$file"
      psql_query "UPDATE public.gateway_db_revision_sequences SET content_sha256='${file_sha}' WHERE sequence_name='${marker}'"
    else
      printf 'already applied (pre-fingerprint legacy marker): %s\n' "${file#"$ROOT_DIR/"}"
    fi
    continue
  fi
  printf 'applying %s\n' "${file#"$ROOT_DIR/"}"
  psql_file "$file"
  psql_query "INSERT INTO public.gateway_db_revision_sequences (sequence_name, content_sha256) VALUES ('${marker}', '${file_sha}') ON CONFLICT (sequence_name) DO NOTHING"
done
# Retire the legacy sequence-wide marker so it cannot mask future appends.
psql_query "DELETE FROM public.gateway_db_revision_sequences WHERE sequence_name='${sequence_name}'"
printf 'database revision sequence completed successfully: %s\n' "$sequence_name"
