package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

type DB struct {
	pool *pgxpool.Pool

	// migrationsPinned（2026-09-23 252 SQL 日志审计轮）：true 时 BeforeAcquire
	// 给每条取出的连接 SET statement_timeout='5min'，抬开角色级 30s 对 boot
	// 迁移链 DDL 锁等待的击杀；ApplyMigrations 结束后清零并 pool.Reset()
	// 丢弃被抬过的连接——serving 池不残留任何超时修改（D1 禁止的全池
	// RuntimeParams 方案的 scoped 替代）。
	migrationsPinned *atomic.Bool

	// lifecycleMu serializes the lazy stdlib bridge creation with shutdown so
	// callers never receive a bridge after its owning DB has been closed.
	lifecycleMu sync.Mutex
	closed      bool

	// stdlibDBOnce lazily creates the shared *sql.DB bridge returned by
	// Stdlib(). Exactly one bridge is constructed for the lifetime of *DB;
	// all callers share the underlying pgxpool.Pool instead of spawning
	// independent connection pools on every invocation.
	stdlibDBOnce sync.Once
	stdlibDB     *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*DB, error) {
	if databaseURL == "" {
		return nil, nil
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	// 2026-06-26: raised from 16 → 32 to match 184 PG max_connections=1000 budget.
	// 31 PG-consumer pods × 32 = 992 connections (8 reserved for replication/stats).
	// 2026-09-18: default stays 32 for shared-PG clusters; LLM_GATEWAY_DB_MAX_CONNS
	// overrides per deployment when the PG headroom allows (e.g. single-pod local
	// dev against a dedicated PG with max_connections=1000). This is the pool the
	// full-mode gateway actually uses — StorageConfig.Full.MaxConnections only
	// governs the storage factory, which initStorageMode constructs in lite mode
	// only (see cmd/gateway/storage_mode_init.go), so it never caps this pool.
	cfg.MaxConns = poolMaxConnsFromEnv(32)
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	// 2026-07-15 P0 fix: disable pgx statement cache to prevent stale prepared
	// statements after schema changes (provider_model_bindings → credential_model_bindings).
	// When a table is renamed but old prepared statements remain cached in long-lived
	// connections, queries fail with "relation does not exist". Disabling the cache
	// forces re-preparation on every query, trading ~5% perf for correctness.
	// See docs/changelogs/2026-07-15-provider-model-bindings-fix.md for details.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	// 2026-09-23 252 SQL 日志审计轮：迁移期 statement_timeout 钉住（D1 残余
	// 根修）。ApplyMigrations 期间置位 migrationsPinned，BeforeAcquire 给每条
	// 取出的连接抬 statement_timeout 到 5min——boot 链对 credentials /
	// request_logs 等热表的 ALTER ... ADD COLUMN IF NOT EXISTS 即使全部列已
	// 在位也要 ACCESS EXCLUSIVE（逐列 no-op），共享库持续写流下锁等待稳定
	// 超角色级 30s 被 57014 击杀且烧点不推进（245 seq 2193-2202 十连败实证，
	// 07:23-08:16）。迁移结束 pool.Reset() 丢弃被抬连接，serving 期每条连接
	// 回到角色默认 30s（catalog 守卫族 + 本钉双保险，守卫管"能不能不跑"，
	// 钉管"跑的时候别被杀"）。
	var migrationsPinned atomic.Bool
	cfg.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		if migrationsPinned.Load() {
			_, _ = conn.Exec(ctx, "SET statement_timeout = '5min'")
		}
		return true
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Defer pool cleanup - only close if we're returning an error
	var success bool
	defer func() {
		if !success && pool != nil {
			pool.Close()
		}
	}()

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		return nil, err
	}
	slog.Info("postgres connected", "max_conns", cfg.MaxConns, "min_conns", cfg.MinConns)
	db := &DB{pool: pool, migrationsPinned: &migrationsPinned}
	if err := db.ApplyMigrations(ctx); err != nil {
		return nil, err
	}
	success = true // Mark success to prevent defer from closing pool
	return db, nil
}

// poolMaxConnsFromEnv 解析 LLM_GATEWAY_DB_MAX_CONNS 为 pgxpool 池上限：
// 未设置返回 def；非法值（非正整数/溢出）记 Warn 并保持 def——与
// config.applyPositiveIntEnv 同样的容错语义，坏值不阻断启动。
func poolMaxConnsFromEnv(def int32) int32 {
	v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_DB_MAX_CONNS"))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n <= 0 {
		slog.Warn("postgres: invalid LLM_GATEWAY_DB_MAX_CONNS, keeping default",
			"value", v, "default", def)
		return def
	}
	return int32(n)
}

// ApplyMigrations runs all idempotent schema migrations.
// Idempotent: safe to run repeatedly. Auto-called by Open() at startup.
// Also called by `gateway migrate` subcommand so launcher can run
// migrations while old version still serves traffic.
//
// 2026-08-06 retry: production PG (252) sets statement_timeout=30s and the
// shared DB is contended by a second gateway's hourly promote cron. A single
// ensure* statement can be canceled with SQLSTATE 57014 while it waits on a
// lock; treating that as fatal permanently bricks the process into
// no-DB mode ("postgres disabled") and trips deploy auto-rollback. Retrying
// once after a short backoff (bounded well under systemd TimeoutStartSec=90s:
// worst case ≈ 30s statement_timeout + 5s + 30s) lets a transient lock window
// resolve instead of killing the boot.
func (db *DB) ApplyMigrations(ctx context.Context) error {
	const maxAttempts = 2
	// 2026-09-23 审计轮：置位迁移期超时钉（BeforeAcquire 抬 5min），结束/失败
	// 均清零并 Reset 池——被抬的连接不回流 serving，角色默认 30s 即时恢复。
	if db.migrationsPinned != nil {
		db.migrationsPinned.Store(true)
		defer func() {
			db.migrationsPinned.Store(false)
			if db.pool != nil {
				db.pool.Reset()
			}
		}()
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			slog.Warn("schema migrations failed transiently, retrying",
				"attempt", attempt, "max", maxAttempts, "error", lastErr)
		}
		lastErr = db.applyMigrationsOnce(ctx)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// columnsAllPresent —— 2026-09-23 252 SQL 日志审计轮（D1 残余根修通用件）：
// credentials/request_logs 等热表上的 ALTER ... ADD COLUMN IF NOT EXISTS，
// 即使全部列已存在也要取 ACCESS EXCLUSIVE 锁才能逐列 no-op；共享库持续写
// 流下锁等待超过角色级 statement_timeout=30s 即被 57014 击杀，boot 重试撞
// 同一堵墙（烧点不可推进）直至 retry_budget 烧穿 → postgres disabled →
// 部署安全网回滚（245 07:23/07:26/07:35 三连败实证，PG 日志逐字归因）。
// 守卫语义：全部列在位 → true（调用方跳过 DDL，幂等 stamp 类语句照常执行）；
// 表不存在 / 任一列缺失 / 探测出错 → false（走原 ensure 路径，自含幂等）。
func (db *DB) columnsAllPresent(ctx context.Context, table string, cols []string) bool {
	if db == nil || db.pool == nil || len(cols) == 0 {
		return false
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = "'" + c + "'"
	}
	q := fmt.Sprintf(
		`SELECT count(*) FROM unnest(ARRAY[%s]) AS want(name)
		 WHERE to_regclass('public.%s') IS NOT NULL
		   AND NOT EXISTS (SELECT 1 FROM information_schema.columns
		     WHERE table_schema='public' AND table_name='%s'
		       AND column_name = want.name)`,
		strings.Join(quoted, ","), table, table)
	var missing int
	if err := db.pool.QueryRow(ctx, q).Scan(&missing); err != nil {
		return false
	}
	return missing == 0
}

func (db *DB) applyMigrationsOnce(ctx context.Context) error {
	// Use the parent ctx (no 3s timeout) for schema migrations. The
	// pingCtx above is only for the initial Ping() check; reusing it
	// for the migrations makes a real DB with many tables (15+ ALTER/
	// CREATE INDEX / MATERIALIZED VIEW statements) time out at boot.
	// EnsureSchema on production PG (252) can exceed 60s when the disk is
	// under pressure or autovacuum holds locks. Boot without DB bricks the
	// admin UI ("database not configured") while /healthz still returns 200.
	migCtx, migCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer migCancel()
	if err := db.ensureRequestLogSchema(migCtx); err != nil {
		return err
	}
	// 2026-09-07 migration 680 self-heal: /api/logs 读路径的 canonical 视图
	// request_logs_with_current_month 被带外操作删除后没有任何机制重建，
	// 所有列表/详情/聚合请求持续 42P01。幂等 ensure：视图健康时零 DDL。
	// 必须在 ensureRequestLogSchema 之后（视图依赖 hot/parent 表存在）。
	if err := db.ensureRequestLogsCurrentMonthView(migCtx); err != nil {
		return err
	}
	if err := db.ensureRequestJourneyObservationSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureJournalSnapshotReceiptSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureQualityFixModeSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureProviderSoftDelete(migCtx); err != nil {
		return err
	}
	// 2026-09-13 migration 701: 余额/套餐下限列。balance_floor_guard worker
	// 启动早于流量，缺列会让它的候选 SELECT 直接 42703 并永久空转。
	if err := db.ensureCredentialBalanceFloor(migCtx); err != nil {
		return err
	}
	// 2026-09-14 migration 704: 套餐探测失败退避戳。R28 #12a 的扫描 SQL 与
	// 失败分支都读写 credentials.plan_quota_probe_failed_at，缺列时 5 分钟
	// sweep 每 轮 42703（096141ecc 漏补 ensure 链，09-14 审计实测）。
	if err := db.ensureCredentialPlanQuotaProbeBackoff(migCtx); err != nil {
		return err
	}
	// api_key_auto_profile was created without the unique identity required by
	// DBProfileStore.Put's ON CONFLICT (api_key_id). Run this independently of
	// unrelated schema self-heals so normal startup repairs existing databases.
	if err := db.ensureApiKeyAutoProfileIdentity(migCtx); err != nil {
		return err
	}
	// 2026-09-17 (087 自愈): provider_templates 健康反馈三列。freediscovery
	// 扫描调度器的失败计数/自动禁用分支每周期读写，缺列即 42703 空转。
	if err := db.ensureFreediscoveryTemplateHealth(migCtx); err != nil {
		return err
	}
	// 2026-09-11 migration 693: provider_models.canonical_cleared_at 是管理员
	// 解绑持久化标记，clear_canonical PATCH / discovery upsert / 健康检查都在
	// 读它。升级库缺列时这些路径整体 42703（实测），必须在服务流量前补齐。
	if err := db.ensureProviderModelsCanonicalClearedAt(migCtx); err != nil {
		return err
	}
	// 2026-09-05 migration 655: session_summaries 表结构对账（memora 覆盖事件
	// 修复）。触发器 trg_update_session_summary 与 admin 读路径都按 canonical
	// 列访问该表，缺列时请求日志写入与 /api/admin/sessions/:id/snapshot 等
	// 端点整体 42703。幂等补列，必须先于任何会话读路径生效。
	if err := db.ensureSessionSummariesCanonical(migCtx); err != nil {
		return err
	}
	// 2026-09-10 (handoff-20260910): 471/690 drifted out of the applied set
	// (a '471' ledger row from 2026-08-07 belongs to older content), leaving
	// session_summaries without archived_at — the TTL trimmer then dies with
	// 42703 on every boot and rows can never be archived.
	if err := db.ensureSessionSummariesArchivalSchema(migCtx); err != nil {
		return err
	}
	// 2026-09-05 migration 656 (audit D-2#4/H-2): auto_route_selections_hot
	// 网关侧幂等 ensure。只升二进制未重跑 656 的存量库上，AUTO 路由 selection
	// 写入（telemetry selection_writer 批量 INSERT）整批静默丢弃、settle/affinity
	// worker 每 sweep 报错——学习闭环空转。与 655 同位置生效。
	if err := db.ensureAutoRouteSelectionsHotSchema(migCtx); err != nil {
		return err
	}
	// Routing analytics reads these columns while creating its source view.
	// Keep this small table-only ensure ahead of the analytics materialized
	// views; the broader recent-success-rate ensure runs later because it
	// also updates credential binding state.
	if err := db.ensureRoutingAnalyticsColumns(migCtx); err != nil {
		return err
	}
	// 2026-08-31 migration 632: routing analytics materialized views.
	// Must run before the gateway serves /api/admin/auto-route/analytics/*
	// traffic; on failure the views are absent and handlers fall back to
	// the (slow but correct) base-view queries.
	if err := db.ensureRoutingAnalyticsMaterializedViews(migCtx); err != nil {
		return err
	}
	// 2026-09-06 migration 664: orchestration_runtime_instances 表由外部编排服务在启动后立即写入，
	// 必须在网关开始服务流量前就绪，否则后台 worker 会反复报错 "relation does not exist"。
	if err := db.ensureOrchestrationRuntimeInstancesSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureApplicationsTable(migCtx); err != nil {
		return err
	}
	if err := db.ensureCredentialColumns(migCtx); err != nil {
		return err
	}
	if err := db.ensureFpSlotLimit(migCtx); err != nil {
		return err
	}
	if err := db.ensureConcurrencyMode(migCtx); err != nil {
		return err
	}
	if err := db.ensureCredentialGovernorRevision(migCtx); err != nil {
		return err
	}
	if err := db.ensureRoutingRecentSuccessRate(migCtx); err != nil {
		return err
	}
	if err := db.ensureUnavailableRecoverAtSchema(migCtx); err != nil {
		return err
	}

	if err := db.ensureWorkTypeSchema(migCtx); err != nil {
		return err
	}
	// 2026-09-14 migration 709: work_type 路由补齐 4 个无路由任务类
	// (code_audit/function_call/intent_classification/planning)。V2 漏斗只给
	// 有路由的类全量候选池,无路由类必走 48h 兜底池(auto-matching 审计 O1′-c,
	// 人工已确认)。幂等 seed,管理员已配置的路由集不被回改。
	if err := db.ensureWorkTypeRouteCoverage(migCtx); err != nil {
		return err
	}
	if err := db.EnsureTenantsTable(migCtx); err != nil {
		return err
	}
	if err := db.ensureTuningSignalsStrategyColumn(migCtx); err != nil {
		return err
	}
	if err := db.ensureSessionMemoraExtractionLog(migCtx); err != nil {
		return err
	}
	if err := db.ensureSessionTitles(migCtx); err != nil {
		return err
	}
	if err := db.ensureSessionTitleStates(migCtx); err != nil {
		return err
	}
	if err := db.ensureTuningSignalsViews(migCtx); err != nil {
		return err
	}
	// Ensure get_current_tenant() function exists before MaaS schema
	// (007_maas_billing.sql / 008_billing_orders.sql depend on it for RLS policies).
	// The function is also defined in 001_users_table.sql / usersSchemaSQL,
	// but those run after db.Open() returns (in main.go). On fresh databases
	// this ordering would cause the POLICY CREATE to fail. CREATE OR REPLACE
	// makes this idempotent regardless of order.
	if _, err := db.pool.Exec(migCtx, `
		CREATE OR REPLACE FUNCTION public.get_current_tenant()
		RETURNS text
		LANGUAGE sql
		STABLE
		AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;
	`); err != nil {
		return err
	}
	if err := db.EnsureMaasSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureRoutingOverridesTable(migCtx); err != nil {
		return err
	}
	if err := db.ensureRoutingOverridesAudit(migCtx); err != nil {
		return err
	}
	if err := db.ensurePassiveProbeStateSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureProbeWatchdogIndex(migCtx); err != nil {
		return err
	}
	if err := db.ensureProbeStateFunctionFixes(migCtx); err != nil {
		return err
	}
	// 2026-09-17 (promote 函数在位修复，ensureProbeStateFunctionFixes 同族):
	//   - dashboard_access_events promote 装回了 579 的坏列投影（714 钉扎
	//     从 579 原体复制所致），每 promote 周期 42703、hot 只进不出；
	//   - session_bodies promote 的 (id, partition_date) 仲裁覆盖不到父表
	//     (tenant_id, request_id, partition_date) 唯一键，final_full 毒丸行
	//     每周期 23505、整批晋升永远失败。
	// PartitionManager 在 db.Open 之后才启动，两个修复必须在启动路径生效。
	if err := db.ensureDashboardAccessEventsPromotePinned(migCtx); err != nil {
		return err
	}
	if err := db.ensureSessionBodiesPromoteDrain(migCtx); err != nil {
		return err
	}
	if err := db.ensureNodeProbeTriggerKindSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureTenantModelPoliciesSchema(migCtx); err != nil {
		return err
	}
	// TODO(credentialquota): bootstrap re-enabled when dispatch path is wired
	// (see AUDIT_24H_20260817.md B1). The credential_client_quota table has no
	// production reader/writer yet: domains/credentialquota has zero importers
	// and credentialfpslot.Manager.AcquireWithQuota (the alleged exposure per
	// commit 7b086ed52) is test-only and does not import domains/credentialquota.
	// Auto-creating the RLS-protected table on 245/154 would land a permanently
	// empty orphan schema that cannot be cleaned up without a real drop
	// migration (>=530).
	//
	// if err := db.ensureCredentialClientQuotaSchema(migCtx); err != nil {
	// 	return err
	// }
	if err := db.ensureResponseFormatAnomaliesSchema(migCtx); err != nil {
		return err
	}
	// 2026-07-28: per-request / per-(cred,model) integrity events
	// (model identity mismatch, finish_refusal, finish_truncation, token_arith
	// failure, empty_response, repeated_content, fingerprint_drift). See
	// migration 462 and admin/model_integrity.go.
	if err := db.ensureModelIntegrityEventsSchema(migCtx); err != nil {
		return err
	}
	// 2026-07-28: fingerprint drift baseline state. Stored separately
	// from model_integrity_events so the drift worker can compare today's
	// dominant fingerprint against a stable historical reference and
	// dedup alerts across hourly ticks.
	if err := db.ensureIntegrityFingerprintBaselineSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureSupplementalRLS(migCtx); err != nil {
		return err
	}
	if err := db.ensureAnalysisEventsRLS(migCtx); err != nil {
		return err
	}
	// Product modules, license modules, and VibeCoding schema (Phase 1).
	// These are startup-level equivalents of 371-373 migration files.
	if err := db.ensureProductModulesSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureLicenseModulesSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureLicenseDevicesSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureFaultManagementSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureAutoUpdateSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureCenterOpsSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureRuntimeMetricsSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureRouteIncidentSchema(migCtx); err != nil {
		return err
	}
	// 2026-09-16 migration 715: route_incidents.state 增加 'pending'。修复
	// DecideState 引入 StatePending（阈值前不可见）后，旧 CHECK 约束只认
	// active/recovering/recovered，首条 'pending' 写入即 23514，observer
	// 重试耗尽后整个事件追踪静默失效（见 bba08b922 事件修复）。必须先于
	// 任何流量落库，且在 ensureRouteIncidentSchema 之后保证表已存在。
	if err := db.ensureRouteIncidentPendingState(migCtx); err != nil {
		return err
	}
	if err := db.ensureRouteIncidentPhase2Schema(migCtx); err != nil {
		return err
	}
	// 2026-09-18 migration 724: taskprofile 模块的 task_type_corrections
	// 表（auto 任务类型逐请求人工修正）。启动即自愈，保证 admin
	// task-profile 端点与优化器修正混入在任何部署形态下都有表可用。
	if err := db.ensureTaskTypeCorrections(migCtx); err != nil {
		return err
	}
	// R50 审计 F17 收口: 730 的 role_task_llm_mapping（role_llm_router
	// refresher 每分钟轮询、SelectLLM DB 覆盖源）主通道 ensure——缺表环境
	// 此前每分钟 42P01 Warn 且 DB 覆盖静默失效。
	if err := db.ensureRoleTaskLLMMapping(migCtx); err != nil {
		return err
	}
	// R43 (2026-09-18): task_type_tier_config（ApplySuggestions 落盘目标）——
	// 原迁移 202609_02 表级表达式 UNIQUE 在 PG 上不可执行，表从未被建出。
	if err := db.ensureTaskTypeTierConfig(migCtx); err != nil {
		return err
	}
	if err := db.ensureVibeCodingSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureDistributionSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensurePartitionAutovacuumSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureHandoffLogsHotColumnarSchema(migCtx); err != nil {
		return err
	}
	// OmniFree schema (2026-08-07): 4 tables + extensions + RLS + triggers.
	// Equivalent to sql/migrations/075-omnifree-schema.sql but idempotent
	// and startup-safe.
	if err := db.ensureOmniFreeSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureCredentialKeysSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureWebCookieSessionsSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureUrsmKeyMigrationLedgerSchema(migCtx); err != nil {
		return err
	}
	// 2026-08-11: model IQ system — standard_iq column on models_canonical
	// plus model_iq_runs / node_iq_latest. Mirrors migration 350.
	if err := db.ensureModelsCanonicalStandardIQ(migCtx); err != nil {
		return err
	}
	if err := db.ensureModelIQSchema(migCtx); err != nil {
		return err
	}
	// Dashboard views are derived data for the admin UI, not critical-path.
	// A failure here logs a warning but does NOT block startup — the gateway
	// must still serve traffic even if /probe-health renders empty.
	if err := db.ensureApprovalResumeClaimSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureGoalClientSignalSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureProxyManagementCanonicalSchema(migCtx); err != nil {
		return err
	}
	// 2026-09-10 (handoff-20260910): 689 (columnar→heap monthly partitions +
	// per-table TTL drop functions) never reached ensure-chain environments,
	// so the opslog trimmer's row-level DELETE kept dying on ColumnarScan.
	if err := db.ensureCandidateFailureLogsHeapPartitions(migCtx); err != nil {
		return err
	}
	db.ensureProbeHealthDashboardViews(migCtx)
	return nil
}

// sessionSummariesArchival backfill bounds: 2000-row chunks keep per-statement
// row-lock windows in the milliseconds range (far below the shared PG's 30s
// statement_timeout), and the per-boot wall-clock slice means a leftover
// backfill simply resumes on the next boot — NULL last_accessed_at is
// archival-safe (predicates treat it as "not accessed recently"), so boot
// never waits on convergence.
const (
	sessionSummariesBackfillBatch = 2000
	sessionSummariesBackfillSlice = 20 * time.Second
)

// ensureSessionSummariesArchivalSchema mirrors sql/migrations/startup/
// 471_session_summaries_archival.sql + 690_session_summaries_archived_ttl_index.sql.
//
// 2026-09-10: the live DB's schema_migrations carried a '471' row written on
// 2026-08-07, but that number had since been re-pointed at the archival
// migration — numeric-file migrations are never re-applied once their version
// is in the ledger, so the columns below never materialised. Without them the
// session_summaries TTL trimmer fails with "column archived_at does not
// exist" on every boot and domains/sessionarchive cannot archive rows.
//
// 2026-09-11 (deploy-lib RUNBOOK-zstd-deploy §7.7 gate finding): replaying
// the migration verbatim — one whole-table `UPDATE … WHERE last_accessed_at
// IS NULL` plus two plain CREATE INDEX in a single batch — is a structural
// boot blocker on live databases: with active traffic the shared PG's 30s
// statement_timeout (SQLSTATE 57014) kills the batch, retries fail
// identically, and the "postgres disabled" latch turns readyz 503 on every
// boot. End state is unchanged; only the execution strategy is lock-safe:
//   - columns: nullable ADD COLUMN (metadata-only) under a short lock_timeout
//     with bounded retries against transient lock-queue contention;
//   - backfill: chunked by the session_key watermark (2k rows per statement,
//     per-statement lock/statement timeouts) inside a wall-clock slice; the
//     watermark only ever advances (chunk_max computed with DB collation in
//     the same statement), so the loop terminates even while traffic inserts
//     new NULL rows, and a contended or exhausted slice degrades to "resume
//     next boot" instead of failing boot;
//   - indexes: CREATE INDEX CONCURRENTLY (never blocks DML), after dropping
//     an INVALID leftover of a previously cancelled build — plain
//     IF NOT EXISTS would skip the rebuild forever.
//
// Idempotent, and a no-op on databases where 471/690 already applied.
func (d *DB) ensureSessionSummariesArchivalSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	// 1) Columns. Nullable ADD COLUMN is metadata-only, but it still needs a
	// momentary ACCESS EXCLUSIVE lock; cap the lock-queue wait at 2s per
	// attempt so a busy writer cannot stack every request behind the DDL wait.
	var colErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		_, colErr = d.pool.Exec(ctx, `
			SET LOCAL lock_timeout = '2s';
			ALTER TABLE public.session_summaries ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ;
			ALTER TABLE public.session_summaries ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
		`)
		if colErr == nil {
			break
		}
		var pgErr *pgconn.PgError
		if errors.As(colErr, &pgErr) && pgErr.Code == "55P03" {
			continue
		}
		return fmt.Errorf("ensure session_summaries archival columns: %w", colErr)
	}
	if colErr != nil {
		return fmt.Errorf("ensure session_summaries archival columns: %w", colErr)
	}

	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// 2) Backfill, chunked by the session_key watermark. Rows predating the
	// column read as "last touched at last request". Converges across boots:
	// the summary writer leaves new rows NULL and the archival predicates
	// treat NULL as "not accessed recently". chunk_max is computed by the
	// database (window function) so the watermark advances in DB collation
	// order — a Go-side byte-wise max could skip keys under a punctuation-
	// aware collation.
	backfillDeadline := time.Now().Add(sessionSummariesBackfillSlice)
	watermark := ""
	backfilled := 0
	// 2026-09-22 252 SQL 日志审计轮：backfill 段先收紧 statement_timeout。
	// 252 生产 llm_gateway 角色级 rolconfig=30s：chunk 在锁竞争下烧满 30s 才被
	// 57014 击杀，每次 boot（ApplyMigrations ×2 attempts）各烧 30s，烧穿
	// LLM_GATEWAY_DB_BOOT_RETRY_SECONDS 预算 → "postgres disabled" DB-less →
	// 蓝绿部署 readyz 永不过（245 部署 blocker 实锤，600s 探针两轮不复收敛）。
	// chunk 的设计语义本就是"竞争即快失败、下个 boot 续跑"——5s 快失败把
	// miss 代价从 30s 降到 5s，静默 boot 上 chunk 照常完成推进水位。
	if _, err := conn.Exec(ctx, `SET statement_timeout = '5s'`); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(backfillDeadline) {
			slog.Warn("session_summaries last_accessed_at backfill slice exhausted; resumes next boot",
				"rows_backfilled", backfilled, "watermark", watermark)
			break
		}
		rows, err := conn.Query(ctx, `
			WITH chunk AS (
				SELECT session_key, max(session_key) OVER () AS chunk_max
				FROM public.session_summaries
				WHERE session_key > $1
				  AND last_accessed_at IS NULL
				  AND last_request_at IS NOT NULL
				ORDER BY session_key
				LIMIT $2
			)
			UPDATE public.session_summaries ss
			SET last_accessed_at = ss.last_request_at
			FROM chunk c
			WHERE ss.session_key = c.session_key
			RETURNING c.chunk_max
		`, watermark, sessionSummariesBackfillBatch)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "57014") {
				slog.Warn("session_summaries backfill chunk contended; resumes next boot",
					"pgcode", pgErr.Code, "rows_backfilled", backfilled, "watermark", watermark)
				break
			}
			return fmt.Errorf("session_summaries archival backfill chunk: %w", err)
		}
		watermarks, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("session_summaries archival backfill chunk: %w", err)
		}
		if len(watermarks) == 0 {
			break
		}
		watermark = watermarks[0]
		backfilled += len(watermarks)
	}

	// 3) Archival indexes, built CONCURRENTLY so hot-path DML never blocks.
	// The pinned conn raises statement_timeout for the build (shared PG
	// default is 30s) and restores it before release. The backfill phase
	// above deliberately ran at 5s fast-fail; see the comment there.
	if _, err := conn.Exec(ctx, `SET statement_timeout = '10min'`); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SET statement_timeout = DEFAULT`)
	}()

	for _, idx := range []struct{ name, ddl string }{
		{
			name: "idx_session_summaries_archival",
			ddl: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_archival
				ON public.session_summaries (archived_at, last_accessed_at, last_request_at)
				WHERE archived_at IS NULL`,
		},
		{
			name: "idx_session_summaries_archived",
			ddl: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_summaries_archived
				ON public.session_summaries (archived_at, last_request_at)
				WHERE archived_at IS NOT NULL`,
		},
	} {
		var valid bool
		err := conn.QueryRow(ctx, `
			SELECT i.indisvalid
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_class t ON t.oid = i.indrelid
			WHERE c.relname = $1 AND t.relname = 'session_summaries'
			  AND t.relnamespace = 'public'::regnamespace
		`, idx.name).Scan(&valid)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// index absent: build it below
		case err != nil:
			return fmt.Errorf("inspect %s: %w", idx.name, err)
		case valid:
			continue
		default:
			// A cancelled CONCURRENTLY build leaves an INVALID index behind;
			// IF NOT EXISTS would skip the rebuild forever. DROP INDEX
			// CONCURRENTLY cannot run inside a transaction block — both
			// statements run standalone on this pinned conn.
			slog.Warn("dropping INVALID index left by an interrupted build", "index", idx.name)
			if _, err := conn.Exec(ctx, `DROP INDEX CONCURRENTLY IF EXISTS public.`+idx.name); err != nil {
				return fmt.Errorf("drop invalid %s: %w", idx.name, err)
			}
		}
		if _, err := conn.Exec(ctx, idx.ddl); err != nil {
			return fmt.Errorf("create %s: %w", idx.name, err)
		}
	}

	if _, err := d.pool.Exec(ctx, `
		INSERT INTO public.schema_migrations (version, description)
		VALUES ('471', 'session_summaries_archival'),
		       ('690', 'session_summaries archived ttl index')
		ON CONFLICT (version) DO NOTHING;
	`); err != nil {
		return fmt.Errorf("ensure session_summaries archival schema: %w", err)
	}
	slog.Info("session_summaries archival schema ensured", "backfilled_rows", backfilled)
	return nil
}

// ensureCandidateFailureLogsHeapPartitionsSQL mirrors the executable body of
// sql/migrations/startup/689_candidate_failure_logs_partitions_heap.sql
// (function replacements, columnar→heap conversion with row-count
// conservation, and the post-conversion verification), wrapped in one
// transaction exactly like the migration file.
//
// 2026-09-12 (migration 694 follow-up): the candidate ensure function here
// converges to 694's final body — all month derivation moved out of DECLARE
// initializers and behind SET LOCAL TIME ZONE 'Asia/Shanghai'. This ensure
// reruns on every binary startup; without the pin it would overwrite the
// timezone-corrected function that migration 694 installed, resurrecting the
// 473-class 8h gap on UTC sessions (declaration initializers evaluate before
// any in-body SET LOCAL could take effect).
const ensureCandidateFailureLogsHeapPartitionsSQL = `
BEGIN;
SET LOCAL statement_timeout = '10min';

CREATE OR REPLACE FUNCTION public.ensure_candidate_failure_logs_partition(target_ts timestamp with time zone)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'candidate_failure_logs_' || to_char(month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 689: heap (was columnar). Row-level DELETE (the 7d TTL trim path
        -- in bg/opslog_trimmer.go) and the hot→monthly promote chain both
        -- need UPDATE/DELETE-capable storage; columnar partitions are
        -- append-only (established by migration 562).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF candidate_failure_logs
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_candidate_failure_logs_partition: created % as heap', partition_name;
    END IF;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION ensure_candidate_failure_logs_partition(timestamp with time zone) IS
'Ensure monthly partition for candidate_failure_logs (heap since 689).
Columnar partitions are append-only, which broke the 7d TTL row-level DELETE
path (bg/opslog_trimmer.go) and the hot→monthly promote chain.';

CREATE OR REPLACE FUNCTION public.drop_old_state_partition_table(
    p_parent_name text,
    p_retention_days integer
)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    total_dropped bigint := 0;
    cutoff_ts timestamptz;
    r record;
    partition_name text;
    partition_year int;
    partition_month int;
    partition_first_day date;
BEGIN
    IF p_retention_days < 1 THEN
        RAISE WARNING 'drop_old_state_partition_table: retention_days=% < 1, clamping to 1', p_retention_days;
        p_retention_days := 1;
    END IF;
    cutoff_ts := NOW() - (p_retention_days || ' days')::interval;

    FOR r IN
        SELECT c.relname AS partname
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE p.relname = p_parent_name
          AND c.relname ~ ('^' || p_parent_name || '_\d{4}_\d{2}$')
    LOOP
        partition_name := r.partname;
        BEGIN
            partition_year := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1) - 1)::int;
            partition_month := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1))::int;
            partition_first_day := make_date(partition_year, partition_month, 1);

            IF (partition_first_day + INTERVAL '1 month' - INTERVAL '1 day') < cutoff_ts THEN
                EXECUTE format('DROP TABLE IF EXISTS %I', partition_name);
                total_dropped := total_dropped + 1;
                RAISE DEBUG 'drop_old_state_partition_table: dropped %', partition_name;
            END IF;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'drop_old_state_partition_table: failed to parse % (%)', partition_name, SQLERRM;
        END;
    END LOOP;

    RETURN total_dropped;
END;
$function$;

COMMENT ON FUNCTION drop_old_state_partition_table(text, int) IS
    'Drops monthly partitions older than the given retention for ONE parent
table (per-table TTL). Added by migration 689 so candidate_failure_logs can
drop at its own 7d TTL while other state tables keep 30d. Used by
bg.partition_manager. Idempotent.';

CREATE OR REPLACE FUNCTION public.drop_old_state_partitions(p_retention_days int DEFAULT 30)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    total_dropped bigint := 0;
    target_tables text[] := ARRAY[
        'routing_decision_log',
        'candidate_failure_logs',
        'handoff_logs',
        'model_probe_runs',
        'credential_model_index'
    ];
    parent_name text;
BEGIN
    IF p_retention_days < 1 THEN
        RAISE WARNING 'drop_old_state_partitions: retention_days=% < 1, clamping to 1', p_retention_days;
        p_retention_days := 1;
    END IF;

    FOREACH parent_name IN ARRAY target_tables LOOP
        total_dropped := total_dropped + drop_old_state_partition_table(parent_name, p_retention_days);
    END LOOP;

    RETURN total_dropped;
END;
$function$;

COMMENT ON FUNCTION drop_old_state_partitions(int) IS
    'Drops monthly partitions older than the given retention for state/routing
tables (single retention applied to all tables). Since 689 this delegates to
drop_old_state_partition_table(); bg.partition_manager calls the per-table
function directly with each table''s own lifecycle TTL. Idempotent.';

-- Convert every remaining columnar monthly partition to heap, preserving
-- data: DETACH → rename bak → rebuild same-bound heap partition → copy back
-- → row-count conservation check → drop bak. Heap partitions are skipped.
DO $$
DECLARE
    r record;
    v_part   text;
    v_bak    text;
    v_bound  text;
    v_src    bigint;
    v_dst    bigint;
    v_moved  bigint;
BEGIN
    FOR r IN
        SELECT c.relname::text AS part,
               pg_get_expr(c.relpartbound, c.oid) AS bound
          FROM pg_class c
          JOIN pg_am am ON am.oid = c.relam
          JOIN pg_namespace n ON n.oid = c.relnamespace
          JOIN pg_inherits i ON i.inhrelid = c.oid
          JOIN pg_class p ON p.oid = i.inhparent
         WHERE n.nspname = 'public'
           AND p.relname = 'candidate_failure_logs'
           AND am.amname = 'columnar'
         ORDER BY c.relname
    LOOP
        v_part  := r.part;
        v_bound := r.bound;
        v_bak   := v_part || '_col2heap_bak';

        IF v_bound IS NULL THEN
            RAISE EXCEPTION '689: % has no partition bound — manual investigation needed', v_part;
        END IF;

        EXECUTE format('SELECT count(*) FROM public.%I', v_part) INTO v_src;

        RAISE NOTICE '689: converting % (storage=columnar, rows=%, bound=%)',
                     v_part, v_src, v_bound;

        EXECUTE format('ALTER TABLE public.candidate_failure_logs DETACH PARTITION public.%I', v_part);
        EXECUTE format('ALTER TABLE public.%I RENAME TO %I', v_part, v_bak);

        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.candidate_failure_logs %s',
            v_part, v_bound);

        EXECUTE format('INSERT INTO public.%I SELECT * FROM public.%I', v_part, v_bak);
        GET DIAGNOSTICS v_moved = ROW_COUNT;
        EXECUTE format('SELECT count(*) FROM public.%I', v_part) INTO v_dst;

        IF v_dst <> v_src OR v_moved <> v_src THEN
            RAISE EXCEPTION '689: row conservation FAILED for % (src=%, moved=%, dst=%) — aborting, transaction will roll back',
                            v_part, v_src, v_moved, v_dst;
        END IF;

        EXECUTE format('DROP TABLE public.%I', v_bak);

        RAISE NOTICE '689: converted % to heap, rows conserved (%)', v_part, v_dst;
    END LOOP;

    IF NOT FOUND THEN
        RAISE NOTICE '689: no columnar partitions found under candidate_failure_logs — nothing to convert';
    END IF;
END $$;

-- Post-conversion verification: no columnar partitions, no leftover bak
-- tables, no detached monthly tables. Any failure rolls the whole ensure back.
DO $$
DECLARE
    v_columnar int;
    v_baks     int;
    v_detached int;
BEGIN
    SELECT count(*) INTO v_columnar
      FROM pg_class c
      JOIN pg_am am ON am.oid = c.relam
      JOIN pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_inherits i ON i.inhrelid = c.oid
      JOIN pg_class p ON p.oid = i.inhparent
     WHERE n.nspname = 'public'
       AND p.relname = 'candidate_failure_logs'
       AND am.amname = 'columnar';

    SELECT count(*) INTO v_baks
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname LIKE 'candidate_failure_logs_%_col2heap_bak';

    SELECT count(*) INTO v_detached
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname ~ '^candidate_failure_logs_\d{4}_\d{2}$'
       AND c.relkind = 'r'
       AND NOT EXISTS (
           SELECT 1 FROM pg_inherits i
            WHERE i.inhrelid = c.oid
              AND i.inhparent = 'public.candidate_failure_logs'::regclass);

    IF v_columnar > 0 THEN
        RAISE EXCEPTION '689: VERIFY FAIL — % columnar partition(s) remain', v_columnar;
    END IF;
    IF v_baks > 0 THEN
        RAISE EXCEPTION '689: VERIFY FAIL — % leftover _col2heap_bak table(s)', v_baks;
    END IF;
    IF v_detached > 0 THEN
        RAISE EXCEPTION '689: VERIFY FAIL — % candidate_failure_logs table(s) detached from parent', v_detached;
    END IF;
END $$;

INSERT INTO public.schema_migrations (version, description)
VALUES ('689', 'candidate_failure_logs partitions heap')
ON CONFLICT (version) DO NOTHING;

COMMIT;
`

// ensureCandidateFailureLogsHeapPartitions mirrors
// sql/migrations/startup/689_candidate_failure_logs_partitions_heap.sql.
//
// 2026-09-10: 689 shipped only as a numeric migration file, which binary-managed
// environments never execute — every monthly partition stayed columnar, the
// 7d TTL DELETE in bg/opslog_trimmer.go kept failing with "UPDATE and CTID
// scans not supported for ColumnarScan", and drop_old_state_partition_table
// (which bg/partition_manager.go calls) did not exist. Idempotent: the
// conversion loop is a no-op once all partitions are heap.
func (d *DB) ensureCandidateFailureLogsHeapPartitions(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	if _, err := d.pool.Exec(ctx, ensureCandidateFailureLogsHeapPartitionsSQL); err != nil {
		return fmt.Errorf("ensure candidate_failure_logs heap partitions: %w", err)
	}
	slog.Info("candidate_failure_logs heap partitions ensured")
	return nil
}

func (d *DB) ensureRequestJourneyObservationSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS request_state_transitions (
			id BIGSERIAL PRIMARY KEY,
			request_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT 'default',
			transition_type TEXT,
			from_state TEXT,
			to_state TEXT,
			metadata JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE request_state_transitions
			ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default',
			ALTER COLUMN transition_type DROP NOT NULL,
			ADD COLUMN IF NOT EXISTS seq BIGINT,
			ADD COLUMN IF NOT EXISTS gateway_instance_id TEXT,
			ADD COLUMN IF NOT EXISTS event_type TEXT,
			ADD COLUMN IF NOT EXISTS stage TEXT,
			ADD COLUMN IF NOT EXISTS requested_model TEXT,
			ADD COLUMN IF NOT EXISTS resolved_model TEXT,
			ADD COLUMN IF NOT EXISTS model TEXT,
			ADD COLUMN IF NOT EXISTS provider_id BIGINT,
			ADD COLUMN IF NOT EXISTS provider TEXT,
			ADD COLUMN IF NOT EXISTS credential_id BIGINT,
			ADD COLUMN IF NOT EXISTS from_model TEXT,
			ADD COLUMN IF NOT EXISTS to_model TEXT,
			ADD COLUMN IF NOT EXISTS from_credential_id BIGINT,
			ADD COLUMN IF NOT EXISTS to_credential_id BIGINT,
			ADD COLUMN IF NOT EXISTS attempt_id TEXT,
			ADD COLUMN IF NOT EXISTS attempt_no INTEGER,
			ADD COLUMN IF NOT EXISTS outcome TEXT,
			ADD COLUMN IF NOT EXISTS error_kind TEXT,
			ADD COLUMN IF NOT EXISTS http_status INTEGER,
			ADD COLUMN IF NOT EXISTS retry_reason TEXT,
			ADD COLUMN IF NOT EXISTS switch_reason TEXT,
			ADD COLUMN IF NOT EXISTS observation_status TEXT,
			ADD COLUMN IF NOT EXISTS node_health_status TEXT,
			ADD COLUMN IF NOT EXISTS retry_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS occurred_at TIMESTAMPTZ`,
		`DROP INDEX IF EXISTS uq_state_transitions_request_seq`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_legacy_request_seq
			ON request_state_transitions (request_id, seq)
			WHERE event_type IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_tenant_request_seq
			ON request_state_transitions (tenant_id, request_id, seq)
			WHERE event_type IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_state_transitions_journey_retry_at
			ON request_state_transitions (tenant_id, retry_at)
			WHERE event_type = 'retry_scheduled' AND retry_at IS NOT NULL`,
		// 2026-09-05: definition-guarded. The previous unconditional DROP+ADD
		// re-validated the whole table (ACCESS EXCLUSIVE for the scan) on every
		// boot; on the shared 252 DB this grew past the 20s boot budget (5×
		// 245 deploy aborts on 2026-09-05, live INSERTs queued behind it).
		// If the definition ever changes, update the expected pg_get_constraintdef
		// string in the same commit so the guard still self-heals drift.
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'request_state_transitions_retry_at_event_chk'
				  AND conrelid = 'request_state_transitions'::regclass
				  AND pg_get_constraintdef(oid) = 'CHECK (((retry_at IS NULL) OR (event_type = ''retry_scheduled''::text)))'
			) THEN
				ALTER TABLE request_state_transitions
					DROP CONSTRAINT IF EXISTS request_state_transitions_retry_at_event_chk;
				ALTER TABLE request_state_transitions
					ADD CONSTRAINT request_state_transitions_retry_at_event_chk CHECK (
						retry_at IS NULL OR event_type = 'retry_scheduled'
					);
			END IF;
		END $$`,
		`CREATE TABLE IF NOT EXISTS request_journey_observation_outbox (
			id BIGSERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			seq BIGINT NOT NULL,
			payload JSONB NOT NULL,
			payload_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			attempts INTEGER NOT NULL DEFAULT 0,
			next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			claim_owner TEXT,
			claim_until TIMESTAMPTZ,
			claim_fencing_token BIGINT NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT request_journey_observation_outbox_identity_uq UNIQUE (tenant_id, request_id, seq),
			CONSTRAINT request_journey_observation_outbox_seq_chk CHECK (seq > 0),
			CONSTRAINT request_journey_observation_outbox_attempts_chk CHECK (attempts >= 0),
			CONSTRAINT request_journey_observation_outbox_claim_fence_chk CHECK (claim_fencing_token >= 0),
			CONSTRAINT request_journey_observation_outbox_status_chk CHECK (status IN ('pending', 'processing', 'failed')),
			CONSTRAINT request_journey_observation_outbox_processing_lease_chk CHECK (
				status <> 'processing' OR (claim_owner IS NOT NULL AND claim_until IS NOT NULL)
			)
		)`,
		`ALTER TABLE request_journey_observation_outbox
			ADD COLUMN IF NOT EXISTS claim_fencing_token BIGINT NOT NULL DEFAULT 0`,
		// Definition-guarded (see request_state_transitions_retry_at_event_chk
		// above): the outbox takes per-request writes on the shared DB, so
		// re-adding these CHECKs every boot is not acceptable.
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'request_journey_observation_outbox_claim_fence_chk'
				  AND conrelid = 'request_journey_observation_outbox'::regclass
				  AND pg_get_constraintdef(oid) = 'CHECK ((claim_fencing_token >= 0))'
			) THEN
				ALTER TABLE request_journey_observation_outbox
					DROP CONSTRAINT IF EXISTS request_journey_observation_outbox_claim_fence_chk;
				ALTER TABLE request_journey_observation_outbox
					ADD CONSTRAINT request_journey_observation_outbox_claim_fence_chk
					CHECK (claim_fencing_token >= 0);
			END IF;
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'request_journey_observation_outbox_processing_lease_chk'
				  AND conrelid = 'request_journey_observation_outbox'::regclass
				  AND pg_get_constraintdef(oid) = 'CHECK (((status <> ''processing''::text) OR ((claim_owner IS NOT NULL) AND (claim_until IS NOT NULL))))'
			) THEN
				ALTER TABLE request_journey_observation_outbox
					DROP CONSTRAINT IF EXISTS request_journey_observation_outbox_processing_lease_chk;
				ALTER TABLE request_journey_observation_outbox
					ADD CONSTRAINT request_journey_observation_outbox_processing_lease_chk CHECK (
						status <> 'processing' OR (claim_owner IS NOT NULL AND claim_until IS NOT NULL)
					);
			END IF;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_request_journey_observation_outbox_due
			ON request_journey_observation_outbox (next_retry_at, created_at)
			WHERE status IN ('pending', 'failed')`,
		`CREATE INDEX IF NOT EXISTS idx_request_journey_observation_outbox_lease
			ON request_journey_observation_outbox (claim_until, created_at)
			WHERE status = 'processing'`,
		`CREATE INDEX IF NOT EXISTS idx_request_journey_observation_outbox_tenant
			ON request_journey_observation_outbox (tenant_id, created_at)`,
		`ALTER TABLE request_journey_observation_outbox ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE request_journey_observation_outbox FORCE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS request_journey_observation_outbox_tenant_isolation
			ON request_journey_observation_outbox`,
		`CREATE POLICY request_journey_observation_outbox_tenant_isolation
			ON request_journey_observation_outbox
			USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT)`,
		`DROP POLICY IF EXISTS request_journey_observation_outbox_super_admin_bypass
			ON request_journey_observation_outbox`,
		`CREATE POLICY request_journey_observation_outbox_super_admin_bypass
			ON request_journey_observation_outbox
			USING (current_setting('app.current_role', true) = 'super_admin'
				OR current_setting('app.bypass_rls', true) = 'true')
			WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
				OR current_setting('app.bypass_rls', true) = 'true')`,
	}
	for _, statement := range statements {
		if _, err := d.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("ensure request journey observation schema: %w", err)
		}
	}
	return nil
}

func (d *DB) ensureJournalSnapshotReceiptSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS public.journal_snapshot_receipts (
			tenant_id TEXT NOT NULL,
			request_id TEXT NOT NULL,
			snapshot_version BIGINT NOT NULL,
			payload_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'processing',
			claim_owner TEXT,
			claim_until TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT journal_snapshot_receipts_identity_uq UNIQUE (tenant_id, request_id, snapshot_version),
			CONSTRAINT journal_snapshot_receipts_version_chk CHECK (snapshot_version > 0),
			CONSTRAINT journal_snapshot_receipts_status_chk CHECK (status IN ('processing', 'completed')),
			CONSTRAINT journal_snapshot_receipts_processing_lease_chk CHECK (
				status <> 'processing' OR (claim_owner IS NOT NULL AND claim_until IS NOT NULL)
			)
		)`,
		// projection_base_seq semantics (2026-08-31, audit P1-4 followup):
		//   * 0  : first-time projection (no prior receipt row) — legacy
		//          first-claim sentinel. Two replicas that both pass 0 are
		//          not distinguishable to the durable store; callers MUST
		//          serialise per-owner delivery before issuing a same-base
		//          0 claim (see cmd/gateway/main_dispatch_observation.go).
		//   * >0 : retry attempt pinned to an existing projection base so
		//          event identities remain stable across partial projection
		//          failures. ClaimWithProjectionBase rejects reclaim paths
		//          that disagree on the stored base with
		//          ErrSnapshotReceiptLeaseLost; non-zero bases are the
		//          cross-process safe choice.
		// Concurrent retries that pass different base values must be rejected by
		// the application layer (ClaimWithProjectionBase); the schema check below
		// only enforces non-negative, leaving 0 as a legal first-claim marker.
		`ALTER TABLE public.journal_snapshot_receipts
			ADD COLUMN IF NOT EXISTS projection_base_seq BIGINT NOT NULL DEFAULT 0`,
		// Definition-guarded (see ensureRequestJourneyObservationSchema): receipts
		// are written on the hot path of the shared DB.
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'journal_snapshot_receipts_projection_base_seq_chk'
				  AND conrelid = 'public.journal_snapshot_receipts'::regclass
				  AND pg_get_constraintdef(oid) = 'CHECK ((projection_base_seq >= 0))'
			) THEN
				ALTER TABLE public.journal_snapshot_receipts
					DROP CONSTRAINT IF EXISTS journal_snapshot_receipts_projection_base_seq_chk;
				ALTER TABLE public.journal_snapshot_receipts
					ADD CONSTRAINT journal_snapshot_receipts_projection_base_seq_chk
					CHECK (projection_base_seq >= 0);
			END IF;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_journal_snapshot_receipts_claim
			ON public.journal_snapshot_receipts (claim_until, updated_at)
			WHERE status = 'processing'`,
		`CREATE INDEX IF NOT EXISTS idx_journal_snapshot_receipts_tenant
			ON public.journal_snapshot_receipts (tenant_id, updated_at DESC)`,
		`ALTER TABLE public.journal_snapshot_receipts ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.journal_snapshot_receipts FORCE ROW LEVEL SECURITY`,
		`DROP POLICY IF EXISTS journal_snapshot_receipts_tenant_isolation ON public.journal_snapshot_receipts`,
		`CREATE POLICY journal_snapshot_receipts_tenant_isolation
			ON public.journal_snapshot_receipts
			USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT)`,
		`DROP POLICY IF EXISTS journal_snapshot_receipts_super_admin_bypass ON public.journal_snapshot_receipts`,
		`CREATE POLICY journal_snapshot_receipts_super_admin_bypass
			ON public.journal_snapshot_receipts
			USING (current_setting('app.current_role', true) = 'super_admin'
				OR current_setting('app.bypass_rls', true) = 'true')
			WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
				OR current_setting('app.bypass_rls', true) = 'true')`,
	}
	for _, statement := range statements {
		if _, err := d.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("ensure journal snapshot receipt schema: %w", err)
		}
	}
	return nil
}

func (d *DB) ensureRequestLogSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// 2026-09-23 252 SQL 日志审计轮（D1 残余根修，07:23/07:26 245 蓝绿两连败
	// 实证）：下方 ADD COLUMN IF NOT EXISTS 即使全部列已存在，也要对
	// request_logs 父表 + 全部月分区取 ACCESS EXCLUSIVE 锁才能逐列 no-op。
	// 晨间负载下锁等待超角色级 statement_timeout=30s 被 57014 击杀，boot 重试
	// 撞同一堵墙（烧点不可推进）→ retry_budget 烧穿 → postgres disabled →
	// readyz 永不过 → 部署安全网回滚。information_schema/pg_indexes 短路：
	// 期望列与索引全部在位 = 零 DDL 零锁。清单必须与下方 ensure 体逐项同步
	// （新增列/索引时两处一起改）；任一缺失走原路径，新环境语义不变。
	const catalogShortCircuitSQL = `
		SELECT
		  (SELECT count(*) FROM unnest(ARRAY[
		     'gw_session_id','gw_task_id','request_status','api_key_prefix',
		     'api_key_owner_user','application_code','parent_request_id',
		     'compression_reason','compression_strategy','compression_meta',
		     'outbound_body','outbound_msg_count','outbound_token_est',
		     'outbound_msg_hashes','quality_flags','quality_fix_actions',
		     'quality_score','client_request_id','upstream_finish_reason',
		     'tool_calls','t0_arrived_at','t1_total_enqueued_at',
		     't2_total_dequeued_at','t3_model_enqueued_at','t4_model_dequeued_at',
		     't5_cred_enqueued_at','t6_cred_dequeued_at','t7_forward_start_at',
		     't8_response_start_at','t9_response_end_at','is_final_success'
		   ]) AS want(name)
		   WHERE to_regclass('public.request_logs') IS NOT NULL
		     AND NOT EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema='public' AND table_name='request_logs'
		         AND column_name = want.name))
		+
		  (SELECT count(*) FROM unnest(ARRAY[
		     't0_arrived_at','t1_total_enqueued_at','t2_total_dequeued_at',
		     't3_model_enqueued_at','t4_model_dequeued_at','t5_cred_enqueued_at',
		     't6_cred_dequeued_at','t7_forward_start_at','t8_response_start_at',
		     't9_response_end_at','is_final_success'
		   ]) AS want(name)
		   WHERE to_regclass('public.request_logs_hot') IS NOT NULL
		     AND NOT EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema='public' AND table_name='request_logs_hot'
		         AND column_name = want.name))
		+
		  (SELECT count(*) FROM unnest(ARRAY[
		     'idx_request_logs_gw_session_ts','idx_request_logs_gw_task_ts',
		     'idx_request_logs_status_ts','idx_request_logs_parent_ts',
		     'idx_request_logs_client_request_id','idx_request_logs_session_outbound',
		     'idx_request_logs_outbound_msg_count','idx_request_logs_quality_flags',
		     'idx_request_logs_provider_quality','idx_request_logs_upstream_finish_reason',
		     'idx_request_logs_tool_calls','idx_request_logs_provider_tool_calls',
		     'uq_request_logs_hot_final_success_session'
		   ]) AS want(name)
		   WHERE NOT EXISTS (
		     SELECT 1 FROM pg_indexes
		     WHERE schemaname='public' AND indexname = want.name))
		`
	var missing int
	if err := d.pool.QueryRow(ctx, catalogShortCircuitSQL).Scan(&missing); err == nil && missing == 0 {
		slog.Info("request_logs schema ensured (catalog short-circuit: all columns and indexes present, zero DDL)")
		return nil
	} else if err != nil {
		// 探测失败不阻断：回落原 ensure 路径（其自含幂等）。
		slog.Warn("request_logs ensure catalog probe failed; falling back to full ensure", "error", err)
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE request_logs
		    ADD COLUMN IF NOT EXISTS gw_session_id TEXT,
		    ADD COLUMN IF NOT EXISTS gw_task_id TEXT,
		    ADD COLUMN IF NOT EXISTS request_status TEXT,
		    ADD COLUMN IF NOT EXISTS api_key_prefix TEXT,
		    ADD COLUMN IF NOT EXISTS api_key_owner_user TEXT,
		    ADD COLUMN IF NOT EXISTS application_code TEXT,
		    -- Round 47 (2026-06-18) compression v7 T1: parent-child chain tracking.
		    -- See db/migrations/013_compression_columns.sql and
		    -- docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.1.
		    ADD COLUMN IF NOT EXISTS parent_request_id TEXT,
		    ADD COLUMN IF NOT EXISTS compression_reason TEXT,
		    ADD COLUMN IF NOT EXISTS compression_strategy TEXT,
		    ADD COLUMN IF NOT EXISTS compression_meta JSONB,
		    -- v3 (2026-06-19) session-level outbound body T23.
		    -- See db/migrations/016_outbound_body.sql.
		    ADD COLUMN IF NOT EXISTS outbound_body       JSONB,
		    ADD COLUMN IF NOT EXISTS outbound_msg_count  INT,
		    ADD COLUMN IF NOT EXISTS outbound_token_est  INT,
		    ADD COLUMN IF NOT EXISTS outbound_msg_hashes JSONB,
		    -- 2026-06-26: client-provided X-Request-Id is preserved here
		    -- for debug / cross-system tracing while the primary
		    -- request_id (request_logs.request_id) is forced server-side
		    -- to prevent client retries from collapsing into a single
		    -- audit row. See db/migrations/054_request_logs_client_request_id.sql.
		    ADD COLUMN IF NOT EXISTS client_request_id TEXT;
		CREATE INDEX IF NOT EXISTS idx_request_logs_gw_session_ts
		    ON request_logs (gw_session_id, ts DESC)
		    WHERE gw_session_id IS NOT NULL AND gw_session_id <> '';
		CREATE INDEX IF NOT EXISTS idx_request_logs_gw_task_ts
		    ON request_logs (gw_task_id, ts DESC)
		    WHERE gw_task_id IS NOT NULL AND gw_task_id <> '';
		CREATE INDEX IF NOT EXISTS idx_request_logs_status_ts
		    ON request_logs (request_status, ts DESC)
		    WHERE request_status IS NOT NULL AND request_status <> '';
		CREATE INDEX IF NOT EXISTS idx_request_logs_parent_ts
		    ON request_logs (parent_request_id, ts DESC)
		    WHERE parent_request_id IS NOT NULL;
		-- 2026-06-26: lookup by client-provided X-Request-Id (debug).
		CREATE INDEX IF NOT EXISTS idx_request_logs_client_request_id
		    ON request_logs (client_request_id, ts DESC)
		    WHERE client_request_id IS NOT NULL;
		-- v3 T23: session outbound lookup (used by SessionCache L3 fallback).
		CREATE INDEX IF NOT EXISTS idx_request_logs_session_outbound
		    ON public.request_logs (gw_session_id, ts DESC)
		  WHERE gw_session_id IS NOT NULL
		      AND outbound_body IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_request_logs_outbound_msg_count
		    ON public.request_logs (tenant_id, ts DESC)
		  WHERE outbound_msg_count IS NOT NULL
		      AND outbound_msg_count > 0;
		-- 2026-06-19: quality fix mode (db/migrations/017_quality_fix_mode.sql).
		-- Per-request tool_call quality signal columns. quality_flags is GIN-
		-- indexed for cheap "which provider emits empty_tool_name most" lookups.
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS quality_flags        TEXT[]    NOT NULL DEFAULT '{}';
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS quality_fix_actions JSONB    NOT NULL DEFAULT '{}'::jsonb;
		ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS quality_score      NUMERIC(3,2);
		CREATE INDEX IF NOT EXISTS idx_request_logs_quality_flags
		    ON request_logs USING GIN (quality_flags)
		    WHERE cardinality(quality_flags) > 0;
		CREATE INDEX IF NOT EXISTS idx_request_logs_provider_quality
		    ON request_logs (provider_id, quality_score, ts DESC)
		    WHERE quality_score IS NOT NULL;
	-- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
	-- See db/migrations/018_upstream_finish_reason.sql. The new column is
	-- the SOLE home for the upstream finish_reason (stop, tool_calls,
	-- length, end_turn, …). failure_detail_code now keeps only the
	-- actual failure code (interruption, 5xx, etc.).
	ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS upstream_finish_reason TEXT;
	CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_finish_reason
	    ON request_logs (upstream_finish_reason, ts DESC)
	    WHERE upstream_finish_reason IS NOT NULL
	      AND upstream_finish_reason <> '';
	-- 2026-06-23: structured tool_calls (042_tool_calls_column.sql).
	-- Populated from both streaming and non-streaming responses.
	ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS tool_calls JSONB;
	CREATE INDEX IF NOT EXISTS idx_request_logs_tool_calls
	    ON request_logs USING GIN (tool_calls)
	    WHERE tool_calls IS NOT NULL AND tool_calls != '[]'::jsonb;
	CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
	    ON request_logs (provider_id, ts DESC)
	    WHERE tool_calls IS NOT NULL
	      AND jsonb_typeof(tool_calls) = 'array'
	      AND jsonb_array_length(tool_calls) > 0;
`)
	if err != nil {
		return err
	}

	// V3.1 (migration 491): 9-stage dispatch queue timestamps on hot + parent.
	// Startup ensure so environments that have not yet run 491 still accept INSERTs.
	_, err = d.pool.Exec(ctx, `
		ALTER TABLE request_logs_hot
		    ADD COLUMN IF NOT EXISTS t0_arrived_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t1_total_enqueued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t2_total_dequeued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t3_model_enqueued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t4_model_dequeued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t5_cred_enqueued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t6_cred_dequeued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t7_forward_start_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t8_response_start_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t9_response_end_at TIMESTAMPTZ;
		ALTER TABLE request_logs
		    ADD COLUMN IF NOT EXISTS t0_arrived_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t1_total_enqueued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t2_total_dequeued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t3_model_enqueued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t4_model_dequeued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t5_cred_enqueued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t6_cred_dequeued_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t7_forward_start_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t8_response_start_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS t9_response_end_at TIMESTAMPTZ;
	`)
	if err != nil {
		return err
	}

	// 会话优化 v4 (migration 532): 会话级最终成功标记。hot 侧部分唯一索引
	// 保证同 gw_session_id 至多一条 final success（历史存量行全 FALSE，谓词
	// 空集，054 时代重复数据不会阻塞索引构建）；分区侧由 SQL migration 的
	// ensure_request_logs_partition 为新分区补建（columnar AM 可能拒建唯一
	// 索引，故此处只做 hot 侧 Go 镜像）。
	_, err = d.pool.Exec(ctx, `
		ALTER TABLE request_logs_hot
		    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN NOT NULL DEFAULT FALSE;
		ALTER TABLE request_logs
		    ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN NOT NULL DEFAULT FALSE;
		CREATE UNIQUE INDEX IF NOT EXISTS uq_request_logs_hot_final_success_session
		    ON request_logs_hot (gw_session_id)
		    WHERE is_final_success AND gw_session_id IS NOT NULL AND gw_session_id <> '';
	`)
	if err != nil {
		return err
	}

	slog.Info("request_logs schema ensured (gw_session_id, gw_task_id, request_status, api_key_prefix, api_key_owner_user, application_code, parent_request_id, compression_reason, compression_strategy, compression_meta, outbound_body, outbound_msg_count, outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, quality_score, client_request_id, is_final_success)")

	// Validate request_logs_hot is heap (not columnar) — UPDATE-heavy table
	// Citus Columnar does not support UPDATE/CTID scans (SQLSTATE 0A000).
	// See: https://github.com/citusdata/citus/issues/... (ColumnarScan UPDATE limitation)
	var storage string
	err = d.pool.QueryRow(ctx, `
		SELECT am.amname
		FROM pg_class c
		JOIN pg_am am ON am.oid = c.relam
		WHERE c.relname = 'request_logs_hot'
	`).Scan(&storage)
	if err != nil {
		slog.Warn("request_logs_hot storage validation failed",
			"error", err,
			"hint", "ensure request_logs_hot exists and is accessible")
	} else if storage != "heap" {
		slog.Error("request_logs_hot storage is NOT heap - UPDATEs will fail!",
			"actual_storage", storage,
			"expected", "heap",
			"action_required", "Convert to heap: ALTER TABLE request_logs_hot SET (storage = heap) or recreate as heap")
	} else {
		slog.Debug("request_logs_hot storage validation passed", "storage", storage)
	}

	// Validate current month partition of request_logs is heap (DETACHED, supports UPDATE)
	// The current month partition must be heap because it receives UPDATEs from claimSessionFinalSuccess
	// via the NOT EXISTS check against request_logs. If it's columnar, UPDATEs routed to it will fail.
	currentMonthPartition := "request_logs_" + time.Now().Format("2006_01")
	err = d.pool.QueryRow(ctx, `
		SELECT am.amname
		FROM pg_class c
		JOIN pg_am am ON am.oid = c.relam
		WHERE c.relname = $1
	`, currentMonthPartition).Scan(&storage)
	if err != nil {
		// Partition might not exist yet (first day of month) - log as debug
		slog.Debug("current month partition storage validation skipped",
			"partition", currentMonthPartition,
			"reason", err.Error())
	} else if storage != "heap" {
		slog.Error("current month partition is NOT heap - UPDATEs may fail!",
			"partition", currentMonthPartition,
			"actual_storage", storage,
			"expected", "heap",
			"action_required", "DETACH partition and ensure it uses heap storage. See rule 33.")
	} else {
		slog.Debug("current month partition storage validation passed",
			"partition", currentMonthPartition, "storage", storage)
	}

	return nil
}

// ensureQualityFixModeSchema mirrors db/migrations/017_quality_fix_mode.sql
// for the providers table. Idempotent.  quality_fix_mode defaults to 'off'
// so existing providers keep their current passthrough behavior.
func (d *DB) ensureQualityFixModeSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE providers
		    ADD COLUMN IF NOT EXISTS quality_fix_mode TEXT NOT NULL DEFAULT 'off'
		        CHECK (quality_fix_mode IN ('off', 'detect_only', 'fix'));

		CREATE TABLE IF NOT EXISTS provider_quality_rollup (
		    provider_id       INT  NOT NULL,
		    bucket_start      TIMESTAMPTZ NOT NULL,
		    total_requests    INT  NOT NULL DEFAULT 0,
		    bad_requests      INT  NOT NULL DEFAULT 0,
		    fixed_requests    INT  NOT NULL DEFAULT 0,
		    avg_quality_score NUMERIC(3,2),
		    top_flag          TEXT,
		    PRIMARY KEY (provider_id, bucket_start)
		);
		CREATE INDEX IF NOT EXISTS idx_provider_quality_rollup_bucket
		    ON provider_quality_rollup (bucket_start DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("quality_fix_mode + provider_quality_rollup schema ensured")
	return nil
}

// ensureProviderSoftDelete mirrors sql/migrations/startup/631_provider_credential_soft_delete.sql.
// 2026-08-31 凭据/供应商软删除：
//   - providers.deleted_at（NULL = 存活行）+ 存活行部分索引
//   - credentials.status CHECK 增加 'deleted' 终态值
//
// 幂等：ADD COLUMN IF NOT EXISTS / 索引 IF NOT EXISTS / 约束先 DROP 再
// ADD（PG 无法 IF NOT EXISTS 约束，重复执行等价重建，值集不变时无副作用）。
// 编号 SQL 文件供 DBA 同步流程使用；本函数保证二进制启动即生效，
// 否则 admin 的 listProviders（WHERE p.deleted_at IS NULL）会在未同步的
// 库上直接 500。
func (d *DB) ensureProviderSoftDelete(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE providers
		    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

		CREATE INDEX IF NOT EXISTS idx_providers_live
		    ON providers (id)
		    WHERE deleted_at IS NULL;

		-- 2026-09-05: credentials 是共享库最热表，此前这里的无条件 DROP+ADD 每次
		-- 启动都对全表做验证扫描并持 ACCESS EXCLUSIVE。守卫：约束存在且定义一致
		-- 则跳过；状态列表变更时同步更新期望的 pg_get_constraintdef 串即可自愈。
		DO $$
		BEGIN
		    IF NOT EXISTS (
		        SELECT 1 FROM pg_constraint
		        WHERE conname = 'credentials_status_check'
		          AND conrelid = 'credentials'::regclass
		          AND pg_get_constraintdef(oid) = 'CHECK ((status = ANY (ARRAY[''active''::text, ''cooling''::text, ''degraded''::text, ''quarantine''::text, ''quota_expired''::text, ''disabled''::text, ''deleted''::text])))'
		    ) THEN
		        ALTER TABLE credentials
		            DROP CONSTRAINT IF EXISTS credentials_status_check;
		        ALTER TABLE credentials
		            ADD CONSTRAINT credentials_status_check
		            CHECK (status = ANY (ARRAY[
		                'active'::text, 'cooling'::text, 'degraded'::text,
		                'quarantine'::text, 'quota_expired'::text,
		                'disabled'::text, 'deleted'::text
		            ]));
		    END IF;
		END $$;
	`)
	if err != nil {
		return err
	}
	slog.Info("provider/credential soft-delete schema ensured (migration 631)")
	return nil
}

// ensureCredentialBalanceFloor mirrors sql/migrations/startup/701_credential_balance_floor.sql.
// 2026-09-13 余额下限 + 订阅套餐额度感知列：
//   - balance_floor_usd / quota_floor_tokens / quota_floor_percent：操作员
//     配置的下限（NULL = 不启用）。bg/balance_floor_guard 周期评估，低于下限
//     写 quota_state='balance_exhausted'（reason='balance_floor'）摘出路由池，
//     充值/窗口重置后自动恢复；绝不写 manual_disabled。
//   - plan_quota_*：zhipu/minimax 套餐探测结果的展示列。
//
// 幂等：纯 ADD COLUMN IF NOT EXISTS（与 631 同级的元数据变更，热表安全），
// 末尾按 693 先例补记 schema_migrations 账本 stamp（已 stamp 时为 no-op）。
func (d *DB) ensureCredentialBalanceFloor(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// D1 残余：8 列全部在位时跳过 credentials ALTER（ACCESS EXCLUSIVE 锁
	// 等待是 boot 烧点，见 columnsAllPresent 注）；701 stamp 幂等照常执行。
	if d.columnsAllPresent(ctx, "credentials", []string{
		"balance_floor_usd", "quota_floor_tokens", "quota_floor_percent",
		"plan_quota_kind", "plan_quota_windows", "plan_quota_remaining_tokens",
		"plan_quota_used_percent", "plan_quota_checked_at",
	}) {
		if _, err := d.pool.Exec(ctx, `
			INSERT INTO public.schema_migrations (version, description)
			VALUES ('701', 'credential balance-floor + plan quota sensing columns')
			ON CONFLICT (version) DO NOTHING;
		`); err != nil {
			return err
		}
		slog.Info("credential balance-floor schema ensured (migration 701, catalog short-circuit)")
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS balance_floor_usd numeric(14,6);

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS quota_floor_tokens bigint;

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS quota_floor_percent numeric(5,2);

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS plan_quota_kind text;

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS plan_quota_windows jsonb;

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS plan_quota_remaining_tokens bigint;

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS plan_quota_used_percent numeric(5,2);

		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS plan_quota_checked_at timestamptz;

		INSERT INTO public.schema_migrations (version, description)
		VALUES ('701', 'credential balance-floor + plan quota sensing columns')
		ON CONFLICT (version) DO NOTHING;
	`)
	if err != nil {
		return err
	}
	slog.Info("credential balance-floor schema ensured (migration 701)")
	return nil
}

// ensureCredentialPlanQuotaProbeBackoff mirrors sql/migrations/startup/704_plan_quota_probe_backoff.sql.
// 2026-09-14 (R28 #12a, 096141ecc)：bg/balance_floor_guard 的套餐探测失败退避。
//   - plan_quota_probe_failed_at：最近一次探测失败时刻。失败分支写 now()，
//     扫描 SQL 凭它冷却 15 分钟并沉底排序；成功探测（persistPlanState）置 NULL。
//     该列绝不参与 plan_quota_checked_at 的 #4 逃生门新鲜度语义，两者必须独立。
//
// 幂等：单列 ADD COLUMN IF NOT EXISTS（credentials 热表，与 701 同级元数据变
// 更），末尾按 701/703 定式补记 schema_migrations 账本 stamp（已 stamp 时为 no-op）。
func (d *DB) ensureCredentialPlanQuotaProbeBackoff(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// D1 残余：同 701——列在位时跳过 credentials ALTER，704 stamp 照常。
	if d.columnsAllPresent(ctx, "credentials", []string{"plan_quota_probe_failed_at"}) {
		if _, err := d.pool.Exec(ctx, `
			INSERT INTO public.schema_migrations (version, description)
			VALUES ('704', 'plan quota probe failure backoff stamp (credentials.plan_quota_probe_failed_at)')
			ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
		`); err != nil {
			return err
		}
		slog.Info("credential plan-quota probe backoff schema ensured (migration 704, catalog short-circuit)")
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS plan_quota_probe_failed_at timestamp with time zone;

		INSERT INTO public.schema_migrations (version, description)
		VALUES ('704', 'plan quota probe failure backoff stamp (credentials.plan_quota_probe_failed_at)')
		ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
	`)
	if err != nil {
		return err
	}
	slog.Info("credential plan-quota probe backoff schema ensured (migration 704)")
	return nil
}

// ensureTaskTypeCorrections mirrors sql/migrations/startup/
// 724_task_type_corrections.sql — taskprofile 模块（2026-09-18）的逐请求
// auto 任务类型人工修正表。独立新表、无既有对象改动；与 ensureRouteIncident
// 同一"二进制启动即生效"的自愈模式，保证 admin /api/admin/task-profile 端点
// 与 routingopt 修正混入在全新安装与存量升级库上都可用。
// 幂等：CREATE TABLE / INDEX IF NOT EXISTS，已应用库上为 no-op。
func (d *DB) ensureTaskTypeCorrections(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS public.task_type_corrections (
			    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			    request_id            text NOT NULL UNIQUE,
			    auto_task_type        text NOT NULL,
			    human_task_type       text NOT NULL,
			    agrees                boolean NOT NULL,
			    classifier_confidence double precision,
			    profile               text,
			    annotator             text NOT NULL,
			    reason                text NOT NULL,
			    created_at            timestamptz NOT NULL DEFAULT NOW()
			);

			CREATE INDEX IF NOT EXISTS idx_task_type_corrections_auto_type
			    ON public.task_type_corrections (auto_task_type, created_at DESC);

			COMMENT ON TABLE public.task_type_corrections IS
			    'taskprofile: 人工对 auto 任务类型分配的逐请求修正（agrees=auto与human一致）';

			COMMENT ON COLUMN public.task_type_corrections.agrees IS
			    'auto_task_type = human_task_type（写入时冻结预计算，聚合免函数）';

			INSERT INTO public.schema_migrations (version, description)
			VALUES ('724', 'taskprofile per-request human task-type corrections')
			ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
		`)
	if err != nil {
		return err
	}
	slog.Info("taskprofile task_type_corrections schema ensured (migration 724)")
	return nil
}

// ensureTaskTypeTierConfig mirrors deploy/sql/migrations/V370__create_tier_config_table.sql
// — taskprofile ApplySuggestions 的落盘目标表的**真实所有者**（deploy 通道，
// tenant_id BIGINT + COALESCE(tenant_id,0) 哨兵唯一索引）。R43 (2026-09-18)：
// sql/migrations/202609_02 是同表的另一份 TEXT 型 DDL 且表级表达式 UNIQUE
// 在 PG 上不可执行（从未在任何库生效）；ApplySuggestions 原 ON CONFLICT
// 按 202609_02 的 COALESCE(tenant_id,”) 推断，在 V370 真表上 42P10——
// apply 端点在有表库上报 500、无表库上报 503，两头都死。本 ensure 与
// ensureRoleTaskLLMMapping mirrors sql/migrations/startup/
// 730_session_role_hierarchy.sql §2 —— R48 会话角色×任务类型 LLM 路由配置表。
// R50 审计（F17 收口）：bg/role_llm_router_refresher 每分钟轮询该表，但
// ApplyMigrations 主通道此前零 ensure——缺表环境（如只跑过旧快照的库）每分钟
// 42P01 Warn 且 DB 覆盖静默失效（SelectLLM 回退 builtin 表）。对齐 724
// ensureTaskTypeCorrections 先例补"二进制启动即生效"的自愈。
// 注意：刻意**不**写 schema_migrations('730')——本 ensure 只覆盖迁移的
// 第 2 节（映射表），sessions 三列/种子/provider_models 修正仍属 730 通道
// 职责；标记整迁移已应用会让通道跳过 730 文件造成缺列。
// 幂等：CREATE TABLE / INDEX IF NOT EXISTS，已应用库上为 no-op（种子不在此
// 重复——730 的 ON CONFLICT DO NOTHING 种子由通道负责）。
func (d *DB) ensureRoleTaskLLMMapping(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS public.role_task_llm_mapping (
			    id                 BIGSERIAL PRIMARY KEY,
			    tenant_id          VARCHAR(255),
			    agent_role         TEXT NOT NULL,
			    task_kind          TEXT NOT NULL,
			    llm_canonical_name TEXT NOT NULL,
			    priority           INT  NOT NULL DEFAULT 100,
			    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
			    note               TEXT,
			    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			    CONSTRAINT role_task_llm_mapping_role_check
			        CHECK (agent_role IN ('main', 'orchestrator', 'planner', 'worker', 'unknown')),
			    CONSTRAINT role_task_llm_mapping_kind_check
			        CHECK (task_kind IN ('search', 'summarize', 'git_ops', 'ops', 'analysis', 'planning', 'solution', 'unknown')),
			    CONSTRAINT role_task_llm_mapping_unique
			        UNIQUE NULLS NOT DISTINCT (tenant_id, agent_role, task_kind, priority)
			);

			CREATE INDEX IF NOT EXISTS idx_role_task_llm_mapping_lookup
			    ON public.role_task_llm_mapping (tenant_id, agent_role, task_kind, enabled, priority)
			    WHERE enabled = TRUE;

			COMMENT ON TABLE public.role_task_llm_mapping IS
			    '730/R48: 按 agent_role × task_kind 的二维 LLM 路由配置（priority 升序为偏好顺序，SelectLLM 依次尝试首个在候选池中的模型）。';
		`)
	if err != nil {
		return err
	}
	slog.Info("role_task_llm_mapping schema ensured (migration 730 §2)")
	return nil
}

// ensureTaskTypeCorrections 同"二进制启动即生效"模式补齐 V370 形态。
// 幂等：CREATE ... IF NOT EXISTS / DO NOTHING，已有行与运维改动不被覆盖。
func (d *DB) ensureTaskTypeTierConfig(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS public.task_type_tier_config (
			    id BIGSERIAL PRIMARY KEY,
			    task_type TEXT NOT NULL,
			    preferred_tier TEXT NOT NULL CHECK (preferred_tier IN ('tier-a', 'tier-b', 'tier-c')),
			    fallback_tiers TEXT[],
			    tenant_id BIGINT,
			    enabled BOOLEAN DEFAULT TRUE,
			    description TEXT,
			    created_at TIMESTAMPTZ DEFAULT NOW(),
			    updated_at TIMESTAMPTZ DEFAULT NOW()
			);

			CREATE UNIQUE INDEX IF NOT EXISTS task_type_tier_config_unique
			    ON public.task_type_tier_config (task_type, COALESCE(tenant_id, 0));

			-- R43: V370 的真表缺 min_confidence 列，而 taskprofile 写面与
			-- autoroute.TierSelector 读面（SELECT min_confidence）都以它为
			-- 契约——补列使两侧在 V370 库上可用。
			ALTER TABLE public.task_type_tier_config
			    ADD COLUMN IF NOT EXISTS min_confidence DECIMAL(3,2) DEFAULT 0.70;
			DO $chk$
			BEGIN
			    IF NOT EXISTS (SELECT 1 FROM pg_constraint
			                   WHERE conname = 'task_type_tier_config_min_confidence_check'
			                   AND conrelid = 'public.task_type_tier_config'::regclass) THEN
			        ALTER TABLE public.task_type_tier_config
			            ADD CONSTRAINT task_type_tier_config_min_confidence_check
			            CHECK (min_confidence >= 0 AND min_confidence <= 1) NOT VALID;
			    END IF;
			END $chk$;

			CREATE INDEX IF NOT EXISTS idx_task_type_tier_config_lookup
			    ON public.task_type_tier_config(task_type, tenant_id, enabled)
			    WHERE enabled = TRUE;

			CREATE INDEX IF NOT EXISTS idx_task_type_tier_config_tenant
			    ON public.task_type_tier_config(tenant_id, enabled)
			    WHERE enabled = TRUE;

			COMMENT ON TABLE public.task_type_tier_config IS
			    '任务类型到模型档位的映射配置表，支持全局和租户级别配置';

			DO $trig$
			BEGIN
			    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trigger_task_type_tier_config_updated_at') THEN
			        CREATE FUNCTION update_task_type_tier_config_updated_at() RETURNS TRIGGER AS $f$
			        BEGIN
			            NEW.updated_at = NOW();
			            RETURN NEW;
			        END;
			        $f$ LANGUAGE plpgsql;
			        CREATE TRIGGER trigger_task_type_tier_config_updated_at
			            BEFORE UPDATE ON public.task_type_tier_config
			            FOR EACH ROW EXECUTE FUNCTION update_task_type_tier_config_updated_at();
			    END IF;
			END $trig$;

			INSERT INTO public.task_type_tier_config
			    (task_type, preferred_tier, fallback_tiers, min_confidence, description)
			VALUES
			    ('architecture', 'tier-a', '{tier-b}', 0.70, 'System design, API design, technical proposals, architecture reviews'),
			    ('audit', 'tier-a', '{tier-b}', 0.70, 'Code review, security audit, PR review, vulnerability analysis'),
			    ('debugging', 'tier-a', '{tier-b}', 0.65, 'Bug investigation, root cause analysis, stack trace debugging'),
			    ('coding', 'tier-b', '{tier-a,tier-c}', 0.75, 'Greenfield development, feature implementation, API integration'),
			    ('refactoring', 'tier-b', '{tier-a,tier-c}', 0.70, 'Code restructuring, optimization, clean-up'),
			    ('testing', 'tier-b', '{tier-c}', 0.75, 'Unit/integration test generation, test coverage'),
			    ('devops', 'tier-c', '{tier-b}', 0.80, 'CI/CD, deployment, infrastructure scripting, container config'),
			    ('documentation', 'tier-c', '{}', 0.85, 'Comments, README, API docs, inline documentation'),
			    ('summary', 'tier-c', '{}', 0.85, 'Code summarization, session recap, overview generation'),
			    ('dependency', 'tier-c', '{tier-b}', 0.75, 'Dependency analysis, upgrade planning, package management')
			ON CONFLICT (task_type, COALESCE(tenant_id, 0)) DO NOTHING;
		`)
	if err != nil {
		return err
	}
	slog.Info("taskprofile task_type_tier_config schema ensured (V370 shape)")
	return nil
}

// ensureApiKeyAutoProfileIdentity repairs a long-standing DDL gap on
// api_key_auto_profile: the table was created without a PK/UNIQUE constraint
// on api_key_id, so autoroute.DBProfileStore.Put's
// `INSERT ... ON CONFLICT (api_key_id)` failed with 42P10 on every sticky
// write (logged-and-dropped at the caller → the sticky-profile feature was
// silently inert) and the decision-path point read seq-scanned.
//
// Unlike the migration-mirroring ensures above there is no numbered
// migration file behind this index (per repo discipline new migrations must
// run on a live DB before registration), so this ensure is the registration
// channel: idempotent via pg_index probe + CREATE UNIQUE INDEX IF NOT
// EXISTS, lock-capped at 2s, tiny table so the build is instant.
func (d *DB) ensureApiKeyAutoProfileIdentity(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	var existing bool
	if err := d.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_index i
				JOIN pg_class c ON c.oid = i.indrelid
				WHERE c.relname = 'api_key_auto_profile'
				  AND i.indisunique
				  AND i.indisvalid
				  AND (SELECT array_agg(attname ORDER BY attname)
				       FROM pg_attribute
				       WHERE attrelid = c.oid AND attnum = ANY(i.indkey::smallint[]))
				      = ARRAY['api_key_id'::name]
			)
		`).Scan(&existing); err != nil {
		return fmt.Errorf("probe api_key_auto_profile unique index: %w", err)
	}
	if existing {
		return nil
	}
	// Acquire a dedicated connection to set session-level lock_timeout for
	// this DDL, then guarantee cleanup regardless of success. Do not use
	// SET LOCAL on the pooled connection the caller acquired — without an
	// explicit BEGIN, pgx auto-commits each Exec, so the SET LOCAL would
	// revert before the CREATE INDEX executes (seen in startup tests).
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		_, lastErr = conn.Exec(ctx, `SET lock_timeout = '2s'`)
		if lastErr != nil {
			return fmt.Errorf("set lock_timeout: %w", lastErr)
		}
		_, lastErr = conn.Exec(ctx, `
				CREATE UNIQUE INDEX IF NOT EXISTS api_key_auto_profile_api_key_id_key
				    ON public.api_key_auto_profile (api_key_id)
			`)
		// R43 (2026-09-18): RESET must run even when ctx was cancelled mid-
		// CREATE — with the caller's ctx a cancelled ensure returns before
		// RESET, leaking session-level lock_timeout='2s' onto a pooled
		// connection (later startup DDL could then 55P03 spuriously).
		_, resetErr := conn.Exec(context.WithoutCancel(ctx), `RESET lock_timeout`)
		if resetErr != nil {
			slog.Warn("api_key_auto_profile ensure: lock_timeout reset failed", "error", resetErr)
		}
		if lastErr == nil {
			slog.Info("api_key_auto_profile identity index ensured (sticky-profile ON CONFLICT repair)")
			return nil
		}
		var pgErr *pgconn.PgError
		if errors.As(lastErr, &pgErr) && pgErr.Code == "55P03" {
			continue
		}
		return fmt.Errorf("ensure api_key_auto_profile unique index: %w", lastErr)
	}
	return fmt.Errorf("ensure api_key_auto_profile unique index: %w", lastErr)
}

// ensureProviderModelsCanonicalClearedAt mirrors sql/migrations/startup/
// 693_provider_models_canonical_cleared_at.sql.
//
// 2026-09-11 部署缺口实测：693 只进了仓库文件与 installer 全新安装路径，
// 升级库没有任何通道应用它（revision sequence 止于 686），新二进制的
// clear_canonical PATCH、modelcatalog.UpsertCredentialModel 与
// routing_health_checker 的 canonical_id_null 查询每个周期报
// SQLSTATE 42703，直到手工补列。本 ensure 让网关启动即自愈，与
// ensureProviderSoftDelete（631）同一"二进制启动即生效"的兜底模式。
// 幂等：ADD COLUMN IF NOT EXISTS，已应用库上为 no-op。
func (d *DB) ensureProviderModelsCanonicalClearedAt(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE public.provider_models
		    ADD COLUMN IF NOT EXISTS canonical_cleared_at TIMESTAMPTZ;

		COMMENT ON COLUMN public.provider_models.canonical_cleared_at IS
		    '管理员解绑标记。非空表示运营者已显式解绑 canonical_id，discovery 等自动路径不得写回 canonical_id；显式重新关联时置回 NULL。';

		INSERT INTO public.schema_migrations (version, description)
		VALUES ('693', 'provider_models canonical_cleared_at admin-unbind marker')
		ON CONFLICT (version) DO NOTHING;
	`)
	if err != nil {
		return fmt.Errorf("ensure provider_models canonical_cleared_at: %w", err)
	}
	slog.Info("provider_models canonical_cleared_at ensured (migration 693)")
	return nil
}

// ensureFreediscoveryTemplateHealth mirrors sql/migrations/087-freediscovery-health-feedback.sql.
//
// 2026-09-17 PG 日志审计（本机 llm_gateway 库 184 次 42703 实测）：087 的
// provider_templates 健康反馈三列只进了仓库根迁移文件，而根迁移没有自动
// 投递通道——bg/scan_scheduler 的失败计数/自动禁用分支与
// domains/freediscovery 的模板详情查询每个周期 42703，扫描健康反馈闭环
// 空转。本 ensure 与 693 同一"二进制启动即自愈"兜底模式。
// 幂等：纯 ADD COLUMN IF NOT EXISTS + CREATE INDEX IF NOT EXISTS，
// 已应用库上为 no-op。
func (d *DB) ensureFreediscoveryTemplateHealth(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// 2026-09-17 deploy blocker (245 shared DB): provider_templates is
	// provisioned by the freediscovery feature's own bootstrap, which some
	// deployments never ran — observed on the shared 252 llm_gateway DB
	// where every other ensure target exists but this table does not. The
	// ALTER below then fails 42P01 on every boot attempt, the
	// openDBWithBootRetry loop misreads it as "postgres unreachable" and
	// burns the whole boot budget, and the deploy's healthz window (60s)
	// expires before the gateway ever listens. A missing base table means
	// the feature is not provisioned here — nothing to ALTER, skip quietly.
	// R39: record the verdict process-wide (ProviderTemplatesProvisioned)
	// so main unwires free-discovery routes (503 instead of 42P01 500s)
	// and never starts the scan scheduler against a missing table.
	var present bool
	if err := d.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = 'provider_templates'
			  AND c.relkind IN ('r', 'p') -- ordinary or partitioned table only
		)`).Scan(&present); err != nil {
		return fmt.Errorf("ensure provider_templates health feedback columns (presence check): %w", err)
	}
	if !present {
		providerTemplatesProvisioned.Store(false)
		slog.Info("provider_templates absent (freediscovery not provisioned); skipping 087 health-feedback ensure")
		return nil
	}
	providerTemplatesProvisioned.Store(true)
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE public.provider_templates
		    ADD COLUMN IF NOT EXISTS consecutive_scan_failures INT DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS last_scan_failure_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS auto_disabled_at TIMESTAMPTZ;

		CREATE INDEX IF NOT EXISTS idx_provider_templates_health
		    ON public.provider_templates(consecutive_scan_failures)
		    WHERE enabled = FALSE AND auto_disabled_at IS NOT NULL;

		COMMENT ON COLUMN public.provider_templates.consecutive_scan_failures IS
		    'Health feedback: consecutive scan failure count; resets to 0 on success, increments on failure, auto-disables at >=3';

		INSERT INTO public.schema_migrations (version, description)
		VALUES ('087', 'freediscovery template health feedback columns (consecutive_scan_failures/last_scan_failure_at/auto_disabled_at)')
		ON CONFLICT (version) DO NOTHING;
	`)
	if err != nil {
		return fmt.Errorf("ensure provider_templates health feedback columns: %w", err)
	}
	slog.Info("provider_templates health feedback schema ensured (migration 087)")
	return nil
}

// providerTemplatesProvisioned records (process-wide) the boot ensure
// chain's verdict on whether public.provider_templates exists as a table.
// Default false; stored true/false by ensureFreediscoveryTemplateHealth.
// Consumers: cmd/gateway wiring — when false, free-discovery admin routes
// stay unwired (requests get the existing 503 not-wired response instead
// of a 500 carrying a raw 42P01) and the FD scan scheduler never starts
// (it would otherwise log a 42P01 on every sweep, forever).
var providerTemplatesProvisioned atomic.Bool

// ProviderTemplatesProvisioned reports the boot ensure chain's verdict on
// provider_templates. True means the table exists (feature may be wired);
// false means the 087 ensure skipped and free-discovery must stay unwired.
// A startup-time decision: creating the table later requires a gateway
// restart to re-evaluate.
func ProviderTemplatesProvisioned() bool { return providerTemplatesProvisioned.Load() }

// IsSchemaMismatchError reports whether err is a PostgreSQL catalog error
// meaning "this database lacks expected schema objects": undefined
// table/column/function (42P01/42703/42883), wrong object type (42809), or
// a feature not supported here (0A000 — e.g. column rewrites blocked by
// view dependencies). These are NOT connectivity problems: boot connection
// retries cannot fix them, they only burn the retry budget (the 245
// incident shape). Callers should fast-fail with an actionable log instead.
func IsSchemaMismatchError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01", "42703", "42883", "42809", "0A000":
			return true
		}
	}
	return false
}

// ensureDashboardAccessEventsPromotePinned repairs
// public.promote_dashboard_access_events_hot_to_partition in place.
//
// 2026-09-17 PG 日志审计（本机 llm_gateway 库 108 次 42703 实测）：迁移 579
// 安装本函数时列投影写的是一张并不存在的 v2 形态（dashboard_id/widget_id/
// occurred_at 等，Go 写端 telemetry/dashboard_events.go 从未写过这些列），
// 607 已修正为 383/451 的真实 23 列形态；而 714 的时区钉扎从 579 原体
// 复制、把坏投影装了回去，于是每个 promote 周期 SELECT 第一句即
// 42703，dashboard_access_events_hot 只进不出。本 ensure 取 607 正体 +
// 714 的 Asia/Shanghai 钉扎（date_trunc 月份分组必须与
// ensure_dashboard_events_partition 的 +08 边界一致，703 同理）。
// 幂等：CREATE OR REPLACE FUNCTION，函数体与目标一致时为 no-op。
// 投递通道：纯 ensure（ensureProbeStateFunctionFixes 的 301/302 先例），
// 不新增 startup 迁移文件，避免双通道登记漂移。
func (d *DB) ensureDashboardAccessEventsPromotePinned(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.promote_dashboard_access_events_hot_to_partition(
		    p_retention interval DEFAULT '8 hours',
		    p_batch_size integer DEFAULT 5000
		)
		RETURNS bigint
		LANGUAGE plpgsql
		AS $function$
		DECLARE
		    v_moved bigint := 0;
		    v_month_value timestamptz;
		BEGIN
		    SET LOCAL TIME ZONE 'Asia/Shanghai';
		    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
		        RAISE EXCEPTION 'p_retention must be positive';
		    END IF;
		    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 100000 THEN
		        RAISE EXCEPTION 'p_batch_size must be between 1 and 100000';
		    END IF;

		    PERFORM pg_advisory_xact_lock(
		        hashtextextended('public.promote_dashboard_access_events_hot_to_partition', 0)
		    );

		    CREATE TEMP TABLE _dae_promotion_batch ON COMMIT DROP AS
		    SELECT event_id, event_type, timestamp, tenant_id, user_id, user_role,
		           session_id, api_path, api_method, api_version, query_params,
		           status_code, response_time_ms, cache_hit, data_size, error_code,
		           error_message, client_ip, user_agent, referer, db_query_time_ms,
		           cache_query_time_ms, created_at
		    FROM public.dashboard_access_events_hot
		    WHERE created_at < statement_timestamp() - p_retention
		    ORDER BY created_at, event_id
		    LIMIT p_batch_size
		    FOR UPDATE SKIP LOCKED;

		    IF NOT EXISTS (SELECT 1 FROM _dae_promotion_batch) THEN
		        RETURN 0;
		    END IF;

		    FOR v_month_value IN
		        SELECT DISTINCT date_trunc('month', created_at)::timestamptz
		        FROM _dae_promotion_batch
		    LOOP
		        PERFORM public.ensure_dashboard_events_partition(v_month_value::date);
		    END LOOP;

		    WITH moved_rows AS (
		        DELETE FROM public.dashboard_access_events_hot h
		        USING _dae_promotion_batch b
		        WHERE h.event_id = b.event_id
		          AND h.created_at = b.created_at
		        RETURNING h.event_id, h.event_type, h.timestamp, h.tenant_id,
		                  h.user_id, h.user_role, h.session_id, h.api_path,
		                  h.api_method, h.api_version, h.query_params, h.status_code,
		                  h.response_time_ms, h.cache_hit, h.data_size, h.error_code,
		                  h.error_message, h.client_ip, h.user_agent, h.referer,
		                  h.db_query_time_ms, h.cache_query_time_ms, h.created_at
		    ), inserted_rows AS (
		        INSERT INTO public.dashboard_access_events (
		            event_id, event_type, timestamp, tenant_id, user_id, user_role,
		            session_id, api_path, api_method, api_version, query_params,
		            status_code, response_time_ms, cache_hit, data_size, error_code,
		            error_message, client_ip, user_agent, referer, db_query_time_ms,
		            cache_query_time_ms, created_at
		        )
		        SELECT event_id, event_type, timestamp, tenant_id, user_id, user_role,
		               session_id, api_path, api_method, api_version, query_params,
		               status_code, response_time_ms, cache_hit, data_size, error_code,
		               error_message, client_ip, user_agent, referer, db_query_time_ms,
		               cache_query_time_ms, created_at
		        FROM moved_rows
		        RETURNING 1
		    )
		    SELECT count(*) INTO v_moved FROM inserted_rows;

		    RETURN v_moved;
		END;
		$function$;

		COMMENT ON FUNCTION public.promote_dashboard_access_events_hot_to_partition(INTERVAL, INTEGER) IS
		    'hot -> partitioned parent drain for dashboard_access_events (607-corrected legacy projection + Asia/Shanghai pin, gateway in-place repair 2026-09-17). Invoked by bg.PartitionManager.promoteSpecs() every promote tick; batched via p_batch_size.';
	`)
	if err != nil {
		return fmt.Errorf("ensure dashboard_access_events promote function: %w", err)
	}
	slog.Info("dashboard_access_events promote function repaired (607 body + Asia/Shanghai pin)")
	return nil
}

// ensureSessionBodiesPromoteDrain repairs
// public.promote_session_bodies_hot_to_partition in place (708 body + drain).
//
// 2026-09-17 PG 日志审计（本机 llm_gateway 库同一 (tenant_id, request_id,
// partition_date) 键 65 轮 23505 实测）：708 的晋升走
// ON CONFLICT (id, partition_date) DO NOTHING，但父表还有
// session_bodies_tenant_request_partition_key UNIQUE
// (tenant_id, request_id, partition_date)。final_full 行被晋升进父表后，
// 写端 bodies_writer.WriteFinalFullInTx 的 upsert 在 hot 找不到行会以新 id
// 重插；下轮晋升撞父表的 tenant/request 唯一键——整批回滚，该行永远晋升
// 不出去，每个 promote 周期报错一次，hot 窗口被毒丸行卡死。
//
// 修复语义（承 626/708 的守卫、advisory lock、列契约校验，动态列清单不变）：
//   - 插入改为不带仲裁目标的 ON CONFLICT DO NOTHING，任何唯一键冲突都跳过；
//   - 增加 reconciled CTE：父表已有同 (tenant_id, request_id,
//     partition_date) 异 id 行时，按写端 upsert 语义把父行内容刷新为 hot
//     载荷（只刷内容列与 ts，不触碰任何唯一键列）；
//   - deleted 覆盖"本轮插入成功"与"本轮已 reconcile"两类，hot 窗口必排干。
//
// 无需 Asia/Shanghai 钉扎：本函数不做 date_trunc 月份分组，分区路由由行
// 自带 partition_date 决定。投递通道：纯 ensure（同
// ensureDashboardAccessEventsPromotePinned）。
func (d *DB) ensureSessionBodiesPromoteDrain(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition(
		    retention_window interval DEFAULT '8 hours',
		    batch_size integer DEFAULT 5000
		)
		RETURNS TABLE(moved_count bigint)
		LANGUAGE plpgsql
		AS $function$
		DECLARE
		    cutoff_ts timestamptz;
		    v_parent_shape TEXT;
		    v_hot_shape TEXT;
		    v_cols TEXT;
		BEGIN
		    IF retention_window IS NULL OR retention_window <= interval '0 seconds' THEN
		        RAISE EXCEPTION 'retention_window must be positive';
		    END IF;
		    IF batch_size IS NULL OR batch_size < 1 THEN
		        RAISE EXCEPTION 'batch_size must be >= 1';
		    END IF;

		    IF to_regclass('public.session_bodies_hot') IS NULL
		       OR to_regclass('public.session_bodies') IS NULL THEN
		        RAISE EXCEPTION 'session_bodies hot and parent tables must both exist';
		    END IF;

		    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
		                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
		      INTO v_parent_shape
		      FROM pg_attribute
		     WHERE attrelid = 'public.session_bodies'::regclass
		       AND attnum > 0 AND NOT attisdropped;
		    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
		                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
		      INTO v_hot_shape
		      FROM pg_attribute
		     WHERE attrelid = 'public.session_bodies_hot'::regclass
		       AND attnum > 0 AND NOT attisdropped;
		    IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN
		        RAISE EXCEPTION 'session_bodies hot/parent column contract has drifted (column set mismatch)';
		    END IF;

		    SELECT string_agg(quote_ident(attname), ',' ORDER BY attnum)
		      INTO v_cols
		      FROM pg_attribute
		     WHERE attrelid = 'public.session_bodies_hot'::regclass
		       AND attnum > 0 AND NOT attisdropped;

		    cutoff_ts := now() - retention_window;

		    IF NOT pg_try_advisory_xact_lock(hashtext('public.promote_session_bodies_hot_to_partition')) THEN
		        RETURN QUERY SELECT 0::bigint;
		        RETURN;
		    END IF;

		    EXECUTE format(
		        'WITH to_move AS (
		            SELECT %1$s
		            FROM public.session_bodies_hot
		            WHERE ts < %3$L::timestamptz
		            ORDER BY ts
		            LIMIT %2$s
		            FOR UPDATE SKIP LOCKED
		        ),
		        inserted AS (
		            INSERT INTO public.session_bodies (%1$s)
		            SELECT %1$s FROM to_move
		            ON CONFLICT DO NOTHING
		            RETURNING id, partition_date
		        ),
		        reconciled AS (
		            UPDATE public.session_bodies s
		            SET request_delta = m.request_delta,
		                response_delta = m.response_delta,
		                outbound_body = m.outbound_body,
		                request_attachments = m.request_attachments,
		                response_attachments = m.response_attachments,
		                ts = m.ts
		            FROM to_move m
		            WHERE s.tenant_id = m.tenant_id
		              AND s.request_id = m.request_id
		              AND s.partition_date = m.partition_date
		              AND s.id <> m.id
		            RETURNING m.id AS hot_id
		        ),
		        deleted AS (
		            DELETE FROM public.session_bodies_hot h
		            WHERE EXISTS (SELECT 1 FROM inserted i
		                          WHERE i.id = h.id AND i.partition_date = h.partition_date)
		               OR EXISTS (SELECT 1 FROM reconciled r WHERE r.hot_id = h.id)
		            RETURNING 1
		        )
		        SELECT count(*) FROM deleted', v_cols, batch_size::text, cutoff_ts::text)
		    INTO moved_count;

		    RETURN QUERY SELECT moved_count;
		END;
		$function$;

		COMMENT ON FUNCTION public.promote_session_bodies_hot_to_partition(interval, integer) IS
		    'Atomically move old rows from session_bodies_hot to monthly partitions (708 contract checks + tenant/request-key conflict reconcile so hot window always drains). Rejects NULL or non-positive retention_window/batch_size (638 semantics kept).';
	`)
	if err != nil {
		return fmt.Errorf("ensure session_bodies promote function: %w", err)
	}
	slog.Info("session_bodies promote function repaired (conflict-drain semantics)")
	return nil
}

// ensureRoutingAnalyticsColumns provides the small, dependency-free schema
// prerequisite for migration 632/649. It must run before the analytics source
// view is created because older databases may predate the probe-origin fields.
func (d *DB) ensureRoutingAnalyticsColumns(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// D1 残余（2026-09-23 审计轮）：request_logs / request_logs_hot 上的
	// ADD COLUMN IF NOT EXISTS，列全在位时跳过（245 seq 2199 boot 在此 57014
	// 实锤；同 columnsAllPresent 注）。
	if d.columnsAllPresent(ctx, "request_logs_hot", []string{"task_type", "origin_stage", "origin_actor"}) &&
		d.columnsAllPresent(ctx, "request_logs", []string{"task_type", "origin_stage", "origin_actor"}) {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE IF EXISTS request_logs_hot
		    ADD COLUMN IF NOT EXISTS task_type TEXT,
		    ADD COLUMN IF NOT EXISTS origin_stage VARCHAR(32),
		    ADD COLUMN IF NOT EXISTS origin_actor VARCHAR(255);

		ALTER TABLE IF EXISTS request_logs
		    ADD COLUMN IF NOT EXISTS task_type TEXT,
		    ADD COLUMN IF NOT EXISTS origin_stage VARCHAR(32),
		    ADD COLUMN IF NOT EXISTS origin_actor VARCHAR(255);
	`)
	if err != nil {
		return fmt.Errorf("ensure routing analytics columns: %w", err)
	}
	return nil
}

// routingAnalyticsMVSQL is the shared definition of the routing analytics
// materialized views (migration 632/649). It owns a narrow source view so
// analytics does not depend on the frozen request-log wrapper column contract.
// Kept as one statement batch because CREATE MATERIALIZED VIEW cannot be
// re-run with CREATE OR REPLACE; the IF NOT EXISTS guards make the batch idempotent.
//
// NULL-safety (2026-08-31 audit): is_auto_request is COALESCEd to FALSE in
// the view. Historical rows carry NULL; without normalization the view
// would split identical traffic into NULL and FALSE buckets (GROUP BY
// treats them as distinct), diverging from the base queries, which read
// NULL as FALSE everywhere (`is_auto_request IS NOT TRUE`).
//
// tenant_id is TEXT in this schema (not bigint — verified on prod 252 PG:
// request_logs_hot.tenant_id → text). The unique indexes therefore use
// plain tenant_id columns with no COALESCE placeholder: PG rejects
// expression indexes for REFRESH MATERIALIZED VIEW CONCURRENTLY
// (SQLSTATE 55000, seen on prod 2026-09-01), and plain-column uniqueness
// is safe because GROUP BY collapses NULL keys into a single row.
//
// effective_provider_id bakes in the same COALESCE(provider_id,
// credential lookup) fallback that buildFlowL23Query (admin/analytics.go)
// applies, so the L2→L3 Sankey shows the same 'unknown' provider share on
// both the materialized and base paths.
//
// auto_profile is exposed because the admin auto-route profile distribution
// (admin/auto_route.go) reads COALESCE(auto_profile, 'unknown') directly from
// this source view. It must stay the LAST column in both UNION branches:
// CREATE OR REPLACE VIEW can only append columns, never reorder existing ones.
const routingAnalyticsMVSQL = `
		-- Keep analytics isolated from the frozen request-log wrapper view. The
		-- narrow source has stable types across hot and parent partitions and
		-- explicitly exposes origin_stage for probe filtering.
		-- CREATE OR REPLACE (not DROP+CREATE): the routing matviews depend on
		-- this view, so a plain DROP fails with SQLSTATE 2BP01 whenever this
		-- batch runs on the index-repair path (views current, ukey missing).
		CREATE OR REPLACE VIEW routing_analytics_source AS
		SELECT
		  ts,
		  task_type::text AS task_type,
		  outbound_model::text AS outbound_model,
		  client_model::text AS client_model,
		  work_type::text AS work_type,
		  provider_id::bigint AS provider_id,
		  credential_id::bigint AS credential_id,
		  is_auto_request::boolean AS is_auto_request,
		  tenant_id::text AS tenant_id,
		  request_id::text AS request_id,
		  success::boolean AS success,
		  latency_ms::numeric AS latency_ms,
		  cost_usd::numeric AS cost_usd,
		  origin_stage::text AS origin_stage,
		  auto_profile::text AS auto_profile
		FROM request_logs_hot
		UNION ALL
		SELECT
		  ts,
		  task_type::text AS task_type,
		  outbound_model::text AS outbound_model,
		  client_model::text AS client_model,
		  work_type::text AS work_type,
		  provider_id::bigint AS provider_id,
		  credential_id::bigint AS credential_id,
		  is_auto_request::boolean AS is_auto_request,
		  tenant_id::text AS tenant_id,
		  request_id::text AS request_id,
		  success::boolean AS success,
		  latency_ms::numeric AS latency_ms,
		  cost_usd::numeric AS cost_usd,
		  origin_stage::text AS origin_stage,
		  auto_profile::text AS auto_profile
		FROM request_logs;

		CREATE MATERIALIZED VIEW IF NOT EXISTS routing_analytics_7d AS
	SELECT
	  DATE_TRUNC('hour', ts) AS time_bucket,
	  COALESCE(NULLIF(task_type, ''), CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END) AS effective_task_type,
	  COALESCE(NULLIF(outbound_model, ''), client_model) AS effective_model,
	  COALESCE(NULLIF(work_type, ''), 'unknown') AS effective_work_type,
	  COALESCE(provider_id, (SELECT cr.provider_id FROM credentials cr WHERE cr.id = credential_id LIMIT 1)) AS effective_provider_id,
	  COALESCE(is_auto_request, FALSE) AS is_auto_request,
	  tenant_id,
	  COUNT(*) AS request_count,
	  COUNT(*) FILTER (WHERE success) AS success_count,
	  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,
	  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count,
	  percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms) AS p50_latency_ms,
	  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS p95_latency_ms,
	  percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) AS p99_latency_ms,
	  COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
	  NOW() AS refreshed_at
	FROM routing_analytics_source
	WHERE ts >= NOW() - INTERVAL '7 days'
	  AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
	  AND COALESCE(task_type, '') <> 'probe_triggered'
	  AND COALESCE(request_id, '') NOT LIKE 'probe-%'
	  AND (
	    is_auto_request = TRUE
	    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
	  )
	  AND COALESCE(NULLIF(outbound_model, ''), client_model) IS NOT NULL
	GROUP BY
	  time_bucket,
	  effective_task_type,
	  effective_model,
	  effective_work_type,
	  effective_provider_id,
	  is_auto_request,
	  tenant_id;

	-- Unique index for REFRESH ... CONCURRENTLY. PG rejects expression
	-- indexes for concurrent refresh (SQLSTATE 55000, verified on prod
	-- PG17), so this must be plain columns. NULL keys are safe: GROUP BY
	-- collapses NULLs into a single row per key, so the btree's
	-- "duplicate NULLs allowed" semantics never sees two rows with the
	-- same key. The old expression index (_pkey) is dropped and replaced
	-- by _ukey; the rename makes the swap idempotent under IF [NOT] EXISTS.
	DROP INDEX IF EXISTS routing_analytics_7d_pkey;
	CREATE UNIQUE INDEX IF NOT EXISTS routing_analytics_7d_ukey
	  ON routing_analytics_7d (
	    time_bucket,
	    effective_task_type,
	    effective_model,
	    effective_work_type,
	    effective_provider_id,
	    is_auto_request,
	    tenant_id
	  );

	CREATE INDEX IF NOT EXISTS routing_analytics_7d_task_model_idx
	  ON routing_analytics_7d (effective_task_type, effective_model);

	CREATE INDEX IF NOT EXISTS routing_analytics_7d_time_idx
	  ON routing_analytics_7d (time_bucket DESC);

	CREATE INDEX IF NOT EXISTS routing_analytics_7d_tenant_idx
	  ON routing_analytics_7d (tenant_id)
	  WHERE tenant_id IS NOT NULL;

	CREATE MATERIALIZED VIEW IF NOT EXISTS routing_audit_summary_7d AS
	SELECT
	  tenant_id,
	  COUNT(*) AS total_requests,
	  COUNT(*) FILTER (WHERE success) AS success_count,
	  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,
	  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS specified_request_count,
	  NOW() AS refreshed_at
	FROM routing_analytics_source
	WHERE ts >= NOW() - INTERVAL '7 days'
	  AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health', 'probe_direct', 'probe_v2', 'model_probe', 'passive_probe', 'manual')
	  AND COALESCE(task_type, '') <> 'probe_triggered'
	  AND COALESCE(request_id, '') NOT LIKE 'probe-%'
	  AND (
	    is_auto_request = TRUE
	    OR (is_auto_request IS NOT TRUE AND client_model IS NOT NULL AND client_model <> '')
	  )
	GROUP BY tenant_id;

	DROP INDEX IF EXISTS routing_audit_summary_7d_pkey;
	CREATE UNIQUE INDEX IF NOT EXISTS routing_audit_summary_7d_ukey
	  ON routing_audit_summary_7d (tenant_id);
`

// ensureRoutingAnalyticsMaterializedViews mirrors
// sql/migrations/startup/up/632_routing_analytics_materialized_view.sql.
// 2026-08-31: pre-aggregates the 7-day window that the admin analytics
// endpoints (matrix / flow / audit) aggregate on demand — those queries
// Seq-Scan 314K+ rows and blew the 15s handler timeout. Views are refreshed
// every 10 minutes by bg.MaterializedViewRefresher; admin handlers fall
// back to the base-view queries whenever the views are missing or stale.
//
// CREATE MATERIALIZED VIEW populates the view as part of creation, so no
// initial REFRESH is needed here. The statement can take tens of seconds
// on the production dataset while the shared PG sets statement_timeout=30s,
// so it runs on a pinned connection with the timeout raised and restored.
func (d *DB) ensureRoutingAnalyticsMaterializedViews(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	// Fast idempotent path: views present AND the plain-column unique
	// indexes exist → nothing to build or repair. The index check is part
	// of the gate because the original 632 deploy created expression
	// indexes (…_pkey) that REFRESH ... CONCURRENTLY rejects; instances
	// running this ensure must still swap them for …_ukey.
	var upToDate bool
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname='public' AND matviewname='routing_analytics_7d')
		   AND EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname='public' AND matviewname='routing_audit_summary_7d')
		   AND EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='routing_analytics_7d_ukey')
			   AND EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='routing_audit_summary_7d_ukey')
			   AND POSITION('origin_stage' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_source'), true), '')) > 0
			   AND POSITION('auto_profile' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_source'), true), '')) > 0
			   AND POSITION('origin_stage' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_7d'), true), '')) > 0
		   AND POSITION('origin_stage' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_audit_summary_7d'), true), '')) > 0
	`).Scan(&upToDate); err == nil && upToDate {
		return nil
	}

	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// CREATE aggregates the full 7-day partition; the default 30s
	// statement_timeout on prod would cancel it mid-boot.
	if _, err := conn.Exec(ctx, `SET statement_timeout = '10min'`); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SET statement_timeout = DEFAULT`)
	}()

	var staleDefinition bool
	if err := conn.QueryRow(ctx, `
		SELECT CASE
				WHEN to_regclass('public.routing_analytics_7d') IS NOT NULL
				 AND to_regclass('public.routing_audit_summary_7d') IS NOT NULL
				 AND to_regclass('public.routing_analytics_source') IS NOT NULL
				THEN NOT (
					POSITION('origin_stage' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_source'), true), '')) > 0
					AND POSITION('origin_stage' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_7d'), true), '')) > 0
					AND POSITION('origin_stage' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_audit_summary_7d'), true), '')) > 0
				)
				WHEN to_regclass('public.routing_analytics_7d') IS NOT NULL
				  OR to_regclass('public.routing_audit_summary_7d') IS NOT NULL
				  OR to_regclass('public.routing_analytics_source') IS NOT NULL
				THEN TRUE
				ELSE FALSE
			END
	`).Scan(&staleDefinition); err != nil {
		return err
	}
	if staleDefinition {
		if _, err := conn.Exec(ctx, `
			DROP MATERIALIZED VIEW IF EXISTS routing_analytics_7d CASCADE;
			DROP MATERIALIZED VIEW IF EXISTS routing_audit_summary_7d CASCADE;
		`); err != nil {
			return err
		}
	}

	// Migration 649 performs the same one-time rebuild for SQL-driven
	// deployments; this ensure path covers Go-driven startup upgrades.
	if _, err := conn.Exec(ctx, routingAnalyticsMVSQL); err != nil {
		return err
	}
	slog.Info("routing analytics materialized views ensured (migration 632)")
	return nil
}

// ensureOrchestrationRuntimeInstancesSchema mirrors
// sql/migrations/startup/664_orchestration_and_stats_tables.sql.
// This table is used by external orchestration services to register and track runtime instances.
// It must exist at startup because background workers (e.g., orchestration registration)
// attempt to write to it immediately after boot.
func (d *DB) ensureOrchestrationRuntimeInstancesSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS orchestration_runtime_instances (
		  id BIGSERIAL PRIMARY KEY,
		  tenant_id TEXT NOT NULL,
		  runtime_id TEXT NOT NULL,
		  instance_id TEXT NOT NULL,
		  host_id TEXT,
		  endpoint TEXT,
		  status TEXT,
		  capabilities JSONB,
		  registration_revision INTEGER NOT NULL DEFAULT 0,
		  lease_epoch BIGINT,
		  credential_id TEXT,
		  last_heartbeat_at TIMESTAMPTZ,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		  CONSTRAINT uq_orchestration_runtime_instance UNIQUE (tenant_id, runtime_id, instance_id)
		);

		CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_tenant
		  ON orchestration_runtime_instances(tenant_id);

		CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_runtime
		  ON orchestration_runtime_instances(runtime_id);

		CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_status
		  ON orchestration_runtime_instances(status) WHERE status IS NOT NULL;

		CREATE INDEX IF NOT EXISTS idx_orchestration_runtime_instances_heartbeat
		  ON orchestration_runtime_instances(last_heartbeat_at DESC NULLS LAST);

		-- Trigger to update updated_at timestamp
		CREATE OR REPLACE FUNCTION update_orchestration_runtime_instances_updated_at()
		RETURNS TRIGGER AS $$
		BEGIN
		  NEW.updated_at = NOW();
		  RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS trg_orchestration_runtime_instances_updated_at
		  ON orchestration_runtime_instances;

		CREATE TRIGGER trg_orchestration_runtime_instances_updated_at
		  BEFORE UPDATE ON orchestration_runtime_instances
		  FOR EACH ROW
		  EXECUTE FUNCTION update_orchestration_runtime_instances_updated_at();
	`)
	if err != nil {
		return fmt.Errorf("ensure orchestration_runtime_instances schema: %w", err)
	}
	slog.Info("orchestration_runtime_instances schema ensured (migration 664)")
	return nil
}

func (d *DB) ensureWorkTypeSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, workTypeSchemaSQL)
	if err != nil {
		return err
	}
	slog.Info("work_type_config schema ensured (22 seed rows idempotent)")
	return nil
}

// ensureWorkTypeRouteCoverage mirrors sql/migrations/startup/
// 709_work_type_route_coverage.sql. 2026-09-14 auto-matching audit O1′-c
// (human-confirmed): the V2 funnel (WorkTypeRouteStore) only grants the full
// candidate pool to l1_task_type values with enabled routes; classes without
// routes always draw from the 48h fallback pool. Production had no routes for
// code_audit / function_call / intent_classification / planning.
//
// Idempotent: config rows use ON CONFLICT DO NOTHING; route blocks only seed
// when the work_type_key has NO routes at all (491 "administrator-managed
// route sets remain untouched" convention), so rebooting never reverts
// operator edits. Stamps schema_migrations version 706 (dual-ledger
// convention, 701/703/704 style).
func (d *DB) ensureWorkTypeRouteCoverage(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		INSERT INTO work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order)
		VALUES
		  ('code_audit',            '代码审计', '研发', 'code_audit',            'smart',       ARRAY['code','audit'],        ARRAY['审计','审查','安全','漏洞'],     25),
		  ('intent_classification', '意图分类', '通用', 'intent_classification', 'speed_first', ARRAY['classification','intent'], ARRAY['意图','分类','路由'],     26),
		  ('planning',              '任务规划', '研发', 'planning',              'smart',       ARRAY['planning','plan'],     ARRAY['规划','计划','拆解','步骤'],     27)
		ON CONFLICT (key) DO NOTHING;

		INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
		SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
		FROM (VALUES
		  ('fn_call', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
		  ('fn_call', 'minimax-m2.7',      0.85::numeric, 'secondary'),
		  ('fn_call', 'glm-5.2',           0.80::numeric, 'secondary')
		) AS v(work_type_key, canonical_name, weight, tier)
		WHERE NOT EXISTS (
		  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
		)
		ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

		INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
		SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
		FROM (VALUES
		  ('code_audit', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
		  ('code_audit', 'glm-5.2',           0.80::numeric, 'secondary')
		) AS v(work_type_key, canonical_name, weight, tier)
		WHERE NOT EXISTS (
		  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
		)
		ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

		INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
		SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
		FROM (VALUES
		  ('intent_classification', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
		  ('intent_classification', 'glm-5.2',           0.80::numeric, 'secondary')
		) AS v(work_type_key, canonical_name, weight, tier)
		WHERE NOT EXISTS (
		  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
		)
		ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

		INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
		SELECT v.work_type_key, v.canonical_name, v.weight, 0, TRUE, v.tier
		FROM (VALUES
		  ('planning', 'deepseek-v4-flash', 1.00::numeric, 'primary'),
		  ('planning', 'glm-5.2',           0.80::numeric, 'secondary')
		) AS v(work_type_key, canonical_name, weight, tier)
		WHERE NOT EXISTS (
		  SELECT 1 FROM work_type_model_route r WHERE r.work_type_key = v.work_type_key
		)
		ON CONFLICT (work_type_key, canonical_name) DO NOTHING;

		INSERT INTO public.schema_migrations (version, description)
		VALUES ('709', 'work_type route coverage for code_audit/function_call/intent_classification/planning (auto-matching audit O1''-c)')
		ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
	`)
	if err != nil {
		return err
	}
	slog.Info("work_type route coverage ensured (migration 709)")
	return nil
}

// EnsureUsersTable creates the users table for multi-tenant admin authentication.
func (d *DB) EnsureUsersTable(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, usersSchemaSQL)
	if err != nil {
		return err
	}
	slog.Info("users schema ensured")
	return nil
}

// usersSchemaSQL mirrors db/migrations/001_users_table.sql for startup apply.
const usersSchemaSQL = `
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    username VARCHAR(128) NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    email VARCHAR(256) NOT NULL DEFAULT '',
    role VARCHAR(32) NOT NULL DEFAULT 'tenant_admin',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_users ON public.users;
CREATE POLICY tenant_isolation_users ON public.users
  USING ((tenant_id)::text = (public.get_current_tenant())::text);
`

// workTypeSchemaSQL mirrors db/migrations/002_work_types.sql for startup apply.
const workTypeSchemaSQL = `
CREATE TABLE IF NOT EXISTS work_type_config (
    key                 TEXT PRIMARY KEY,
    label               TEXT NOT NULL,
    category            TEXT NOT NULL,
    l1_task_type        TEXT NOT NULL,
    default_profile     TEXT NOT NULL DEFAULT 'smart'
                            CHECK (default_profile IN ('smart', 'speed_first', 'cost_first')),
    tags                TEXT[] NOT NULL DEFAULT '{}',
    prompt_keywords     TEXT[] NOT NULL DEFAULT '{}',
    acc_task_type       TEXT,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order          INT NOT NULL DEFAULT 0,
    synced_from_acc_at  TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    system_prompt       TEXT
);
CREATE INDEX IF NOT EXISTS idx_work_type_config_category ON work_type_config (category, sort_order);
CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config (l1_task_type);

ALTER TABLE work_type_config ADD COLUMN IF NOT EXISTS system_prompt TEXT;

CREATE TABLE IF NOT EXISTS work_type_model_route (
    id              SERIAL PRIMARY KEY,
    work_type_key   TEXT NOT NULL REFERENCES work_type_config(key) ON DELETE CASCADE,
    canonical_name  TEXT NOT NULL,
    weight          NUMERIC(5,2) NOT NULL DEFAULT 1.0,
    min_score       NUMERIC(8,4) NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    tier            TEXT NOT NULL DEFAULT 'secondary'
                    CHECK (tier IN ('primary', 'secondary', 'fallback')),
    task_quality_score NUMERIC(5,2) NOT NULL DEFAULT 0
                    CHECK (task_quality_score >= 0 AND task_quality_score <= 100),
    UNIQUE (work_type_key, canonical_name)
);
ALTER TABLE work_type_model_route
    ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'secondary';
ALTER TABLE work_type_model_route
    ADD COLUMN IF NOT EXISTS task_quality_score NUMERIC(5,2) NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_wtmr_work_type ON work_type_model_route (work_type_key);
CREATE INDEX IF NOT EXISTS idx_wtmr_tier ON work_type_model_route (work_type_key, tier, weight DESC);
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'work_type_model_route'::regclass
          AND conname = 'work_type_model_route_work_type_key_fkey'
    ) THEN
        ALTER TABLE work_type_model_route
            ADD CONSTRAINT work_type_model_route_work_type_key_fkey
            FOREIGN KEY (work_type_key) REFERENCES work_type_config(key)
            ON DELETE CASCADE NOT VALID;
    END IF;
END $$;

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS work_type TEXT;
CREATE INDEX IF NOT EXISTS idx_request_logs_work_type
    ON request_logs (work_type, ts DESC)
    WHERE work_type IS NOT NULL AND work_type <> '';

INSERT INTO work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order)
VALUES
  ('general_chat',        '通用对话',   '通用',   'chat',          'smart',       ARRAY['chat','general'],           ARRAY['对话','聊天','问答'],                    1),
  ('reasoning',           '逻辑推理',   '通用',   'reasoning',     'smart',       ARRAY['reasoning','logic'],        ARRAY['推理','逻辑','数学','证明'],              2),
  ('long_doc',            '长文档处理', '通用',   'long_context',  'smart',       ARRAY['long_context','document'],  ARRAY['长文档','全文','摘要','PDF'],             3),
  ('code_gen',            '代码生成',   '研发',   'code',          'speed_first', ARRAY['code','programming'],       ARRAY['代码','编程','实现','函数'],              4),
  ('code_review',         '代码审查',   '研发',   'code',          'smart',       ARRAY['code','review'],            ARRAY['审查','review','重构','bug'],            5),
  ('agent_workflow',      '多步Agent',  '研发',   'agent',         'smart',       ARRAY['agent','workflow'],         ARRAY['agent','多步','工作流','工具'],           6),
  ('fn_call',             '函数调用',   '研发',   'function_call', 'speed_first', ARRAY['function_call','tools'],    ARRAY['function','tool','调用','API'],          7),
  ('copywriting',         '文案创作',   '营销',   'creative',      'smart',       ARRAY['creative','copy'],          ARRAY['文案','标题','广告语','营销'],            8),
  ('social_post',         '社媒发帖',   '营销',   'creative',      'speed_first', ARRAY['social','post'],            ARRAY['发帖','微博','小红书','朋友圈'],          9),
  ('video_script',        '短视频脚本', '营销',   'creative',      'smart',       ARRAY['video','script'],           ARRAY['脚本','短视频','分镜','口播'],           10),
  ('brand_strategy',      '品牌策略',   '营销',   'reasoning',     'smart',       ARRAY['brand','strategy'],         ARRAY['品牌','策略','定位','竞品'],             11),
  ('web_scrape',          '网页采集',   '采集',   'agent',         'cost_first',  ARRAY['scrape','crawl'],           ARRAY['采集','爬虫','抓取','网页'],             12),
  ('social_monitor',      '自媒体监测', '采集',   'agent',         'cost_first',  ARRAY['monitor','social'],         ARRAY['监测','舆情','评论','热搜'],             13),
  ('short_video_collect', '短视频采集', '采集',   'agent',         'cost_first',  ARRAY['video','collect'],          ARRAY['短视频','下载','采集','抖音'],           14),
  ('news_digest',         '资讯摘要',   '采集',   'creative',      'speed_first', ARRAY['news','digest'],            ARRAY['资讯','新闻','摘要','日报'],             15),
  ('competitor_intel',    '竞品情报',   '采集',   'reasoning',     'smart',       ARRAY['competitor','intel'],       ARRAY['竞品','情报','对比','市场'],             16),
  ('image_understand',    '图像理解',   '多媒体', 'vision',        'smart',       ARRAY['vision','image'],           ARRAY['图像','识图','OCR','视觉'],              17),
  ('image_gen_prompt',    '生图Prompt', '多媒体', 'creative',      'smart',       ARRAY['image','prompt'],           ARRAY['生图','prompt','Stable','Midjourney'],   18),
  ('crm_followup',        'CRM跟进',    '企业',   'chat',          'smart',       ARRAY['crm','followup'],           ARRAY['CRM','跟进','客户','销售'],              19),
  ('doc_translate',       '文档翻译',   '企业',   'creative',      'cost_first',  ARRAY['translate','document'],     ARRAY['翻译','文档','双语','本地化'],           20),
  ('meeting_summary',     '会议纪要',   '企业',   'creative',      'speed_first', ARRAY['meeting','summary'],        ARRAY['会议','纪要','总结','行动项'],           21),
  ('compliance_audit',    '合规审计',   '企业',   'reasoning',     'smart',       ARRAY['compliance','audit'],       ARRAY['合规','审计','风控','政策'],             22)
ON CONFLICT (key) DO NOTHING;

INSERT INTO work_type_config (key, label, category, l1_task_type, default_profile, tags, prompt_keywords, sort_order, system_prompt)
VALUES
  (
    'session_title',
    '会话标题生成',
    '企业',
    'creative',
    'cost_first',
    ARRAY['session','title','admin','gateway'],
    ARRAY['标题','会话','总结','主题'],
    23,
    '你是会话标题生成助手。根据下方完整多轮会话日志，用中文生成一个简短准确的标题（不超过18字），概括用户目标与会话结果。只输出标题纯文本：不要引号、编号、解释、XML/HTML 标签、thinking/redacted 标记或英文占位符。'
  ),
  (
    'session_summary',
    '会话日志总结',
    '企业',
    'creative',
    'cost_first',
    ARRAY['session','summary','admin','gateway'],
    ARRAY['总结','摘要','会话','日志'],
    24,
    '你是会话日志分析助手。请严格输出 JSON，格式如下：
{"title":"简短准确的中文会话标题（12-20字）","summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"],"user_intent":"用户核心目标"}
要求：
- title 概括用户当前目标与已取得的结果，不要使用引号或解释
- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何
- key_points 提取 3-5 个关键事实或决策点，每条 15-40 字
- 不要输出 JSON 以外的任何文本
- 如果语料中包含错误信息，务必在总结中提及'
  )
ON CONFLICT (key) DO NOTHING;

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled)
VALUES
  ('session_title',   'minimax-m2.7',       1.00, 0, TRUE),
  ('session_title',   'glm-5.1',            0.95, 0, TRUE),
  ('session_title',   'deepseek-v4-flash',  0.90, 0, TRUE),
  ('session_summary', 'minimax-m2.7',       1.00, 0, TRUE),
  ('session_summary', 'glm-5.1',            0.95, 0, TRUE),
  ('session_summary', 'deepseek-v4-flash',  0.90, 0, TRUE)
ON CONFLICT (work_type_key, canonical_name) DO NOTHING;
`

func (d *DB) Enabled() bool {
	if d == nil {
		return false
	}
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	return !d.closed && d.pool != nil
}

func (d *DB) Pool() *pgxpool.Pool {
	if d == nil {
		return nil
	}
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.closed {
		return nil
	}
	return d.pool
}

// Stdlib 返回一个 database/sql.DB，用于需要 *sql.DB 接口的场景。
//
// 2026-09-01 修复：之前通过 stdlib.OpenDB(*d.pool.Config().ConnConfig) 在每次
// 调用时构造一个全新的 *sql.DB，会在 database/sql 层各自建立独立的连接池，
// 导致：
//   - 每条 dbConn.Stdlib() 调用站点都会泄漏一个连接池（11 处调用 → 11+ 个
//     独立池，与主 pgxpool 互相争抢 PG 连接）；
//   - 注释中"共享底层连接池"的承诺不成立；
//   - 资源生命周期与 db.DB.Close() 不挂钩：调用方关闭 db.DB 时这些孤儿池
//     不会被回收。
//
// 修复方案：使用 stdlib.OpenDBFromPool(d.pool) —— pgx 通过 connector 代理
// 从主 pgxpool.Pool 借/还连接，*sql.DB 自身不持有物理连接（pgx 已强制
// SetMaxIdleConns(0) 防止 database/sql 把池里的连接全部缓存走）。
//
// 所有权：返回的 *sql.DB 由 *DB 独占管理，调用方**不得**调用 Close。
// *DB.Close() 会负责关闭它；重复调用 Close() 是幂等且安全的。
//
// 并发安全：sync.Once 保证整个进程生命周期内只创建一次 *sql.DB，多 goroutine
// 同时调用 Stdlib() 会拿到同一个实例指针（database/sql 内部连接池本身支持并发）。
func (d *DB) Stdlib() *sql.DB {
	if d == nil {
		return nil
	}
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.closed || d.pool == nil {
		return nil
	}
	d.stdlibDBOnce.Do(func() {
		// OpenDBFromPool 的副作用：
		//   - 内部创建 *sql.DB 但不分配任何 PG 连接（连接全部从 pool 借）；
		//   - 自动 SetMaxIdleConns(0)，避免 database/sql 缓存连接挤占 pgxpool；
		//   - 关闭 *sql.DB 不会关闭 pgxpool（由我们手动管理）。
		d.stdlibDB = stdlib.OpenDBFromPool(d.pool)
	})
	return d.stdlibDB
}

// Close 释放 *DB 持有的所有资源：主 pgxpool.Pool 以及由 Stdlib() 创建的共享
// *sql.DB 桥接。幂等；二次调用是 no-op。
//
// 历史背景：早期实现只关闭 pool，导致 *sql.DB 桥接成为孤儿。本次修复后
// Stdlib() 缓存到 *DB 上，Close() 必须同时释放它，避免在测试与短生命周期
// 调用方中泄漏连接池。
func (d *DB) Close() {
	if d == nil {
		return
	}
	d.lifecycleMu.Lock()
	if d.closed {
		d.lifecycleMu.Unlock()
		return
	}
	d.closed = true
	stdlibDB := d.stdlibDB
	pool := d.pool
	d.stdlibDB = nil
	d.pool = nil
	d.lifecycleMu.Unlock()

	if stdlibDB != nil {
		// *sql.DB.Close() 仅清空 database/sql 自己的连接队列，不会触碰底层
		// pgxpool.Pool（pgx connector 解耦了这两层）。即便 pool 已经先关闭，
		// 这一次 Close() 也是安全的：database/sql 会把残留请求直接返回
		// driver.ErrBadConn。
		_ = stdlibDB.Close()
	}
	if pool != nil {
		pool.Close()
	}
}

// EnsureTenantsTable creates the tenants table and backfills from existing
// tenant_id values in users and api_keys tables. Idempotent.
func (d *DB) EnsureTenantsTable(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	if _, err := d.pool.Exec(ctx, tenantsSchemaSQL); err != nil {
		return err
	}
	// Unconditionally seed the 'default' tenant so it exists even when the
	// users/api_keys tables are still empty (e.g. first boot before
	// EnsureUsersTable creates the seed admin). ON CONFLICT makes this safe
	// to re-run.
	_, _ = d.pool.Exec(ctx, `
		INSERT INTO tenants (code, name, status, description)
		VALUES ('default', '默认租户', 'active', '系统默认租户')
		ON CONFLICT (code) DO NOTHING
	`)
	// Backfill: ensure every distinct tenant_id in users/api_keys has a row in tenants
	// We use 'default' as the name for new backfilled rows (admin can rename later)
	_, _ = d.pool.Exec(ctx, `
		INSERT INTO tenants (code, name, status, description)
		SELECT DISTINCT tenant_id, '默认租户', 'active', '由数据迁移自动创建'
		FROM users
		WHERE NOT EXISTS (SELECT 1 FROM tenants WHERE tenants.code = users.tenant_id)
	`)
	_, _ = d.pool.Exec(ctx, `
		INSERT INTO tenants (code, name, status, description)
		SELECT DISTINCT tenant_id, '默认租户', 'active', '由数据迁移自动创建'
		FROM api_keys
		WHERE NOT EXISTS (SELECT 1 FROM tenants WHERE tenants.code = api_keys.tenant_id)
	`)
	slog.Info("tenants schema ensured and backfilled")
	return nil
}

// tenantsSchemaSQL mirrors db/migrations/006_tenants_table.sql for startup apply.
const tenantsSchemaSQL = `
CREATE TABLE IF NOT EXISTS tenants (
    code VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'trial', 'suspended', 'expired', 'disabled')),
    description TEXT NOT NULL DEFAULT '',
    contact_email VARCHAR(256) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status);
CREATE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);
`

// ensureTuningSignalsStrategyColumn adds the dedicated `strategy`
// column to tuning_signals (P7.1). The strategy was previously
// stored only in signal_payload->>'strategy' (JSONB extract), which
// is slow and not indexable. This migration promotes it to a
// proper TEXT column with two indexes:
//
//	idx_tuning_signals_strategy_ts    (strategy, ts DESC) — A/B summary
//	idx_tuning_signals_strategy_task  (strategy, task_type, ts DESC) — breakdown
//
// Backward compatibility: rows that pre-date this column have
// strategy = 'pattern_layered' (the historical default). The
// handleStrategies endpoint reads from the column directly, but
// still has a JSONB fallback for old data.
func (d *DB) ensureTuningSignalsStrategyColumn(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		-- 1. Create the table if it doesn't exist (idempotent for
		--    fresh deployments that pre-date this column).
		CREATE TABLE IF NOT EXISTS tuning_signals (
		    id                BIGSERIAL PRIMARY KEY,
		    request_id        TEXT NOT NULL,
		    session_id        TEXT,
		    ts                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    task_type         TEXT NOT NULL,
		    classifier        TEXT NOT NULL,
		    confidence        NUMERIC(4,3),
		    chosen_model      TEXT,
		    canonical_id      INT,
		    success_score     NUMERIC(3,2) NOT NULL DEFAULT 0.5,
		    latency_score     NUMERIC(3,2) NOT NULL DEFAULT 0.5,
		    cost_score        NUMERIC(3,2) NOT NULL DEFAULT 0.5,
		    drift_flag        BOOLEAN NOT NULL DEFAULT FALSE,
		    quality_score     NUMERIC(3,2) NOT NULL DEFAULT 0.5,
		    latency_ms        INT,
		    cost_usd          NUMERIC(10,6),
		    prompt_tokens     INT,
		    completion_tokens INT,
		    signal_payload    JSONB,
		    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		-- 2. Add the strategy column if it doesn't exist (the main
		--    migration for deployments that already have the table).
		ALTER TABLE tuning_signals
		    ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'pattern_layered'
		        CHECK (strategy IN ('baseline_heuristic','pattern_layered','llm_fallback'));

		-- 3. Indexes for the A/B breakdown endpoint
		--    (admin/auto_route_tuning.go::handleStrategies)
		CREATE INDEX IF NOT EXISTS idx_tuning_signals_strategy_ts
		    ON tuning_signals (strategy, ts DESC);
		CREATE INDEX IF NOT EXISTS idx_tuning_signals_strategy_task
		    ON tuning_signals (strategy, task_type, ts DESC)
		    WHERE task_type IS NOT NULL;

		-- 4. Backfill from the legacy JSONB field. New rows write
		--    directly to the column; this catches rows from before
		--    P7.1 that had the strategy only in JSONB.
		UPDATE tuning_signals
		SET strategy = COALESCE(
		    NULLIF(signal_payload->>'strategy', ''),
		    'pattern_layered'
		)
		WHERE strategy = 'pattern_layered'
		  AND signal_payload ? 'strategy'
		  AND signal_payload->>'strategy' IN
		    ('baseline_heuristic','pattern_layered','llm_fallback');
	`)
	if err != nil {
		return err
	}
	slog.Info("tuning_signals.strategy column ensured (2 indexes, JSONB backfill)")
	return nil
}

func (d *DB) ensureSessionMemoraExtractionLog(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS session_memora_extraction_log (
		    task_id             TEXT PRIMARY KEY,
		    extracted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    written             INT NOT NULL DEFAULT 0,
		    skipped_noise       INT NOT NULL DEFAULT 0,
		    skipped_duplicate   INT NOT NULL DEFAULT 0,
		    status              TEXT NOT NULL DEFAULT 'ok',
		    detail              JSONB
		);
		CREATE INDEX IF NOT EXISTS idx_session_memora_extraction_at
		    ON session_memora_extraction_log (extracted_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("session_memora_extraction_log schema ensured")
	return nil
}

func (d *DB) ensureSessionTitles(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS session_titles (
		    task_id             TEXT NOT NULL,
		    scoped_session_id   TEXT NOT NULL DEFAULT '',
		    title               TEXT NOT NULL,
		    generated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    model               TEXT,
		    api_key_id          INT,
		    PRIMARY KEY (task_id, scoped_session_id)
		);
		CREATE INDEX IF NOT EXISTS idx_session_titles_generated_at
		    ON session_titles (generated_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("session_titles schema ensured")
	return nil
}

// ensureTuningSignalsViews creates two pre-aggregated views on
// tuning_signals (P7.5). The /tuning/accuracy endpoint's GROUP BY
// (task_type, classifier) over 7 days of data does a full scan
// with a non-trivial aggregation (~30ms on 100k rows). The views
// pre-aggregate into 5-min and 1-day buckets, so the endpoint
// can read a 7-day window in ~3ms (10x speedup).
//
// Two views:
//
//	tuning_signals_5m   — 5-minute buckets, retained 7 days
//	tuning_signals_daily — 1-day buckets, retained 90 days
//
// Both are regular (not materialised) views. The bg worker
// (bg/tuning_view_refresher.go) refreshes them every 5 minutes.
// The refresh cost is bounded (~50ms) and runs out of band.
func (d *DB) ensureSessionTitleStates(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.session_title_states (
		    tenant_id         text NOT NULL,
		    scoped_session_id text NOT NULL,
		    title             text,
		    deleted           boolean NOT NULL DEFAULT false,
		    deleted_at        timestamptz,
		    fencing_token     bigint NOT NULL DEFAULT 0,
		    lease_owner       text,
		    lease_expires_at  timestamptz,
		    source            text NOT NULL DEFAULT 'unknown',
		    source_priority   integer NOT NULL DEFAULT 0,
		    source_task_id    text,
		    created_at        timestamptz NOT NULL DEFAULT now(),
		    updated_at        timestamptz NOT NULL DEFAULT now(),
		    PRIMARY KEY (tenant_id, scoped_session_id)
		);
		ALTER TABLE public.session_title_states
		    ADD COLUMN IF NOT EXISTS title text,
		    ADD COLUMN IF NOT EXISTS deleted boolean NOT NULL DEFAULT false,
		    ADD COLUMN IF NOT EXISTS deleted_at timestamptz,
		    ADD COLUMN IF NOT EXISTS fencing_token bigint NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS lease_owner text,
		    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
		    ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'unknown',
		    ADD COLUMN IF NOT EXISTS source_priority integer NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS source_task_id text,
		    ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now(),
		    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
		DO $$
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.session_title_states'::regclass AND conname = 'session_title_states_token_nonnegative') THEN
		        ALTER TABLE public.session_title_states ADD CONSTRAINT session_title_states_token_nonnegative CHECK (fencing_token >= 0);
		    END IF;
		    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.session_title_states'::regclass AND conname = 'session_title_states_priority_nonnegative') THEN
		        ALTER TABLE public.session_title_states ADD CONSTRAINT session_title_states_priority_nonnegative CHECK (source_priority >= 0);
		    END IF;
		    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.session_title_states'::regclass AND conname = 'session_title_states_deleted_consistency') THEN
		        ALTER TABLE public.session_title_states ADD CONSTRAINT session_title_states_deleted_consistency CHECK ((deleted AND deleted_at IS NOT NULL) OR NOT deleted);
		    END IF;
		END $$;
		CREATE INDEX IF NOT EXISTS idx_session_title_states_active ON public.session_title_states (tenant_id, scoped_session_id) WHERE deleted = false AND title IS NOT NULL AND title <> '';
		CREATE INDEX IF NOT EXISTS idx_session_title_states_tombstone ON public.session_title_states (tenant_id, updated_at DESC) WHERE deleted = true;
		CREATE INDEX IF NOT EXISTS idx_session_title_states_lease ON public.session_title_states (lease_expires_at) WHERE lease_expires_at IS NOT NULL;
	`)
	if err != nil {
		return err
	}
	slog.Info("session_title_states schema ensured")
	return nil
}

func (d *DB) ensureTuningSignalsViews(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		-- 5-minute bucket materialised view.
		--   bucket = date_trunc('hour', ts) + (minute/5) * '5 minutes'
		CREATE MATERIALIZED VIEW IF NOT EXISTS tuning_signals_5m AS
		SELECT
		    date_trunc('hour', ts)
		        + (FLOOR(EXTRACT(MINUTE FROM ts)::int / 5) * interval '5 minutes')
		        AS bucket,
		    task_type,
		    classifier,
		    COUNT(*) AS total,
		    AVG(quality_score) AS avg_quality,
		    AVG(success_score) AS avg_success,
		    AVG(latency_score) AS avg_latency,
		    AVG(cost_score) AS avg_cost,
		    SUM(CASE WHEN drift_flag THEN 1 ELSE 0 END)::float
		        / NULLIF(COUNT(*), 0) AS drift_rate
		FROM tuning_signals
		WHERE ts >= NOW() - INTERVAL '7 days'
		GROUP BY 1, 2, 3;
		-- Indexes on the materialised view itself (no source filter
		-- needed since the view already limits the data).
		CREATE UNIQUE INDEX IF NOT EXISTS idx_tuning_signals_5m_pk
		    ON tuning_signals_5m (bucket, task_type, classifier);
		CREATE INDEX IF NOT EXISTS idx_tuning_signals_5m_task_ts
		    ON tuning_signals_5m (task_type, classifier, bucket DESC);

		-- 1-day bucket materialised view.
		CREATE MATERIALIZED VIEW IF NOT EXISTS tuning_signals_daily AS
		SELECT
		    date_trunc('day', ts) AS bucket,
		    task_type,
		    classifier,
		    COUNT(*) AS total,
		    AVG(quality_score) AS avg_quality,
		    AVG(success_score) AS avg_success,
		    AVG(latency_score) AS avg_latency,
		    AVG(cost_score) AS avg_cost,
		    SUM(CASE WHEN drift_flag THEN 1 ELSE 0 END)::float
		        / NULLIF(COUNT(*), 0) AS drift_rate
		FROM tuning_signals
		WHERE ts >= NOW() - INTERVAL '90 days'
		GROUP BY 1, 2, 3;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_tuning_signals_daily_pk
		    ON tuning_signals_daily (bucket, task_type, classifier);
		CREATE INDEX IF NOT EXISTS idx_tuning_signals_daily_task_ts
		    ON tuning_signals_daily (task_type, classifier, bucket DESC);

		-- No additional source-table indexes needed: the
		-- materialised views carry their own UNIQUE + (task, ts)
		-- indexes, and the view refreshes are full replacements
		-- (CREATE MATERIALIZED VIEW ... then INSERT/UPDATE).
	`)
	if err != nil {
		return err
	}
	slog.Info("tuning_signals views ensured (5m + daily, 2 supporting indexes)")
	return nil
}

// ensureRoutingOverridesTable creates the routing_overrides table used by
// admin CRUD and autoroute OverrideStore (P7.6).
func (d *DB) ensureRoutingOverridesTable(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS routing_overrides (
		    id           BIGSERIAL PRIMARY KEY,
		    task_type    TEXT NOT NULL,
		    profile      TEXT NOT NULL DEFAULT '',
		    mode         TEXT NOT NULL CHECK (mode IN ('pin','ban')),
		    model_chosen TEXT,
		    reason       TEXT NOT NULL DEFAULT '',
		    created_by   TEXT,
		    expires_at   TIMESTAMPTZ,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_routing_overrides_task_profile
		    ON routing_overrides (task_type, profile);
		CREATE INDEX IF NOT EXISTS idx_routing_overrides_expires
		    ON routing_overrides (expires_at)
		    WHERE expires_at IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_overrides_unique
		    ON routing_overrides (task_type, profile, COALESCE(model_chosen, ''), mode);
	`)
	if err != nil {
		return err
	}
	slog.Info("routing_overrides table ensured")
	return nil
}

// ensureRoutingOverridesAudit creates the audit-log table and
// trigger for routing_overrides (P7.9). Every INSERT, UPDATE, and
// DELETE is logged with the actor (from app.current_admin session
// GUC), the action type, and the row state before/after.
//
// Why a trigger: the audit log is correctness-critical. A trigger
// in the same transaction as the DML guarantees atomic audit (no
// missed writes on crash). An application-level log could miss
// writes if the app crashes between DML and log write.
func (d *DB) ensureRoutingOverridesAudit(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS routing_overrides_audit (
		    id              BIGSERIAL PRIMARY KEY,
		    ts              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    action          TEXT NOT NULL
		                    CHECK (action IN ('insert','update','delete')),
		    override_id     BIGINT,
		    task_type       TEXT,
		    profile         TEXT,
		    mode            TEXT,
		    model_chosen    TEXT,
		    reason          TEXT,
		    expires_at      TIMESTAMPTZ,
		    old_expires_at  TIMESTAMPTZ,
		    actor           TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_routing_overrides_audit_ts
		    ON routing_overrides_audit (ts DESC);
		CREATE INDEX IF NOT EXISTS idx_routing_overrides_audit_actor_ts
		    ON routing_overrides_audit (actor, ts DESC)
		    WHERE actor IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_routing_overrides_audit_override_ts
		    ON routing_overrides_audit (override_id, ts DESC)
		    WHERE override_id IS NOT NULL;

		CREATE OR REPLACE FUNCTION routing_overrides_audit_fn()
		RETURNS TRIGGER AS $$
		DECLARE
		    v_actor TEXT := COALESCE(
		        NULLIF(current_setting('app.current_admin', true), ''),
		        'system'
		    );
		BEGIN
		    IF (TG_OP = 'INSERT') THEN
		        INSERT INTO routing_overrides_audit
		            (action, override_id, task_type, profile, mode,
		             model_chosen, reason, expires_at, actor)
		        VALUES
		            ('insert', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
		             NEW.model_chosen, NEW.reason, NEW.expires_at, v_actor);
		        RETURN NEW;
		    ELSIF (TG_OP = 'UPDATE') THEN
		        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at
		           OR NEW.reason IS DISTINCT FROM OLD.reason
		           OR NEW.model_chosen IS DISTINCT FROM OLD.model_chosen
		        THEN
		            INSERT INTO routing_overrides_audit
		                (action, override_id, task_type, profile, mode,
		                 model_chosen, reason, expires_at, old_expires_at,
		                 actor)
		            VALUES
		                ('update', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
		                 NEW.model_chosen, NEW.reason, NEW.expires_at,
		                 OLD.expires_at, v_actor);
		        END IF;
		        RETURN NEW;
		    ELSIF (TG_OP = 'DELETE') THEN
		        INSERT INTO routing_overrides_audit
		            (action, override_id, task_type, profile, mode,
		             model_chosen, reason, expires_at, actor)
		        VALUES
		            ('delete', OLD.id, OLD.task_type, OLD.profile, OLD.mode,
		             OLD.model_chosen, OLD.reason, OLD.expires_at, v_actor);
		        RETURN OLD;
		    END IF;
		    RETURN NULL;
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS routing_overrides_audit_trg ON routing_overrides;
		CREATE TRIGGER routing_overrides_audit_trg
			AFTER INSERT OR UPDATE OR DELETE ON routing_overrides
			FOR EACH ROW EXECUTE FUNCTION routing_overrides_audit_fn();
	`)
	if err != nil {
		return err
	}
	slog.Info("routing_overrides_audit ensured (table + 3 indexes + trigger)")
	return nil
}

// ensurePassiveProbeStateSchema mirrors db/migrations/019_passive_probe_state.sql
// for startup apply. Idempotent. Creates:
//  1. passive_probe_state table for Layer 5 passive observation
//  2. model_probe_state v5 columns (last_unavailable_reason, last_err_code, next_retry_at_override)
//  3. Index for fast reviewing state queries
//
// Without this startup apply, the PassiveProbeListener worker logs
// "relation does not exist" errors every 30s and the /api/routing/
// recent-model-failures endpoint returns 500.
func (d *DB) ensureResponseFormatAnomaliesSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS response_format_anomalies (
			id BIGSERIAL PRIMARY KEY,
			detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			request_id TEXT NOT NULL,
			provider_id INT,
			provider_code TEXT,
			client_model TEXT,
			outbound_model TEXT,
			anomaly_type TEXT NOT NULL,
			severity TEXT NOT NULL DEFAULT 'medium',
			usage_source TEXT,
			expected_tokens INT,
			actual_tokens INT,
			content_size_bytes INT,
			response_structure JSONB,
			response_sample TEXT,
			resolved BOOLEAN NOT NULL DEFAULT false,
			resolved_at TIMESTAMPTZ,
			resolution_notes TEXT,
			tenant_id TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_detected_at
			ON response_format_anomalies(detected_at DESC);
		CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_request_id
			ON response_format_anomalies(request_id);
		CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_provider
			ON response_format_anomalies(provider_code, client_model)
			WHERE provider_code IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_type
			ON response_format_anomalies(anomaly_type, detected_at DESC);
		CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_bridge
			ON response_format_anomalies(resolved, detected_at, anomaly_type, severity)
			WHERE NOT resolved;
		CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_unresolved
			ON response_format_anomalies(detected_at DESC)
			WHERE NOT resolved;
		ALTER TABLE response_format_anomalies ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS response_format_anomalies_tenant_isolation ON public.response_format_anomalies;
			CREATE POLICY response_format_anomalies_tenant_isolation ON public.response_format_anomalies
				USING (tenant_id = public.get_current_tenant())
				WITH CHECK (tenant_id = public.get_current_tenant());
		DROP POLICY IF EXISTS response_format_anomalies_super_admin ON public.response_format_anomalies;
		CREATE POLICY response_format_anomalies_super_admin ON public.response_format_anomalies
			USING (current_setting('app.bypass_rls', true) = 'true')
			WITH CHECK (current_setting('app.bypass_rls', true) = 'true');
		CREATE OR REPLACE VIEW v_format_anomaly_summary AS
		SELECT
			DATE_TRUNC('hour', detected_at) AS hour,
			provider_code,
			client_model,
			anomaly_type,
			severity,
			COUNT(*) AS anomaly_count,
			COUNT(DISTINCT request_id) AS affected_requests,
			AVG(content_size_bytes) AS avg_content_size,
			AVG(expected_tokens) AS avg_expected_tokens,
			AVG(actual_tokens) AS avg_actual_tokens,
			COUNT(*) FILTER (WHERE resolved) AS resolved_count
		FROM response_format_anomalies
		WHERE detected_at > NOW() - INTERVAL '7 days'
		GROUP BY 1, 2, 3, 4, 5;
	`)
	if err != nil {
		return err
	}
	slog.Info("response_format_anomalies schema ensured")
	return nil
}

// ensureModelIntegrityEventsSchema (2026-07-28) mirrors migration 462: the
// model_integrity_events table. Independent of response_format_anomalies
// because its semantics are different (it stores per-request *and* per-
// (cred,model) events like fingerprint drift, where request_id may be NULL).
//
// Idempotent: CREATE TABLE IF NOT EXISTS / DROP+CREATE POLICY.
//
// Sample column holds only PII-safe metadata (provider_response_id,
// system_fingerprint, finish_reason, chunk_count, usage_source) — never
// the user prompt or the model's output. The recorder enforces this
// in domains/streaming/integrity/recorder.go.
func (d *DB) ensureModelIntegrityEventsSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS model_integrity_events (
			id BIGSERIAL PRIMARY KEY,
			ts TIMESTAMPTZ NOT NULL DEFAULT now(),
			request_id TEXT,
			tenant_id TEXT,
			application_id INT,
			api_key_id INT,
			provider_id INT,
			provider_code TEXT,
			credential_id INT,
			client_model TEXT,
			outbound_model TEXT,
			raw_model_name TEXT,
			anomaly_type TEXT NOT NULL,
			severity TEXT NOT NULL DEFAULT 'low',
			expected_value TEXT,
			actual_value TEXT,
			sample TEXT,
			context JSONB,
			resolved BOOLEAN NOT NULL DEFAULT false,
			resolved_at TIMESTAMPTZ,
			resolution_notes TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_model_integrity_events_ts
			ON model_integrity_events(ts DESC);
		CREATE INDEX IF NOT EXISTS idx_model_integrity_events_cred_model_type
			ON model_integrity_events(credential_id, raw_model_name, anomaly_type, ts DESC);
		CREATE INDEX IF NOT EXISTS idx_model_integrity_events_provider_type
			ON model_integrity_events(provider_id, anomaly_type, ts DESC);
		CREATE INDEX IF NOT EXISTS idx_model_integrity_events_request_id
			ON model_integrity_events(request_id)
			WHERE request_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_model_integrity_events_bridge
			ON model_integrity_events(resolved, ts, anomaly_type, severity)
			WHERE NOT resolved;
		ALTER TABLE model_integrity_events ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS model_integrity_events_tenant_isolation ON public.model_integrity_events;
		CREATE POLICY model_integrity_events_tenant_isolation ON public.model_integrity_events
			USING (
				tenant_id IS NULL
				OR tenant_id = public.get_current_tenant()
			)
			WITH CHECK (
				tenant_id IS NULL
				OR tenant_id = public.get_current_tenant()
			);
		DROP POLICY IF EXISTS model_integrity_events_super_admin ON public.model_integrity_events;
		CREATE POLICY model_integrity_events_super_admin ON public.model_integrity_events
			USING (current_setting('app.bypass_rls', true) = 'true')
			WITH CHECK (current_setting('app.bypass_rls', true) = 'true');
	`)
	if err != nil {
		return err
	}
	slog.Info("model_integrity_events schema ensured")
	return nil
}

// ensureIntegrityFingerprintBaselineSchema (2026-07-28) mirrors
// migration 348: integrity_fingerprint_baseline table. Holds the
// historical dominant fingerprint per (cred, model) and the most recent
// dominant fingerprint so the drift worker can detect genuine
// change-point events (A → B rollout) without conflating them with
// current-window fragmentation.
//
// PRIMARY KEY (tenant_id, credential_id, raw_model_name) so the same
// physical credential can host multiple tenants in deployments that
// share a row layout. tenant_id defaults to 'default' to match the
// rest of the integrity surface.
func (d *DB) ensureIntegrityFingerprintBaselineSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS integrity_fingerprint_baseline (
			tenant_id              TEXT NOT NULL DEFAULT 'default',
			provider_id            INT,
			credential_id          INT NOT NULL,
			raw_model_name         TEXT NOT NULL,
			baseline_fingerprint   TEXT,
			baseline_share_pct     INT,
			baseline_sample_count  BIGINT NOT NULL DEFAULT 0,
			baseline_window_start  TIMESTAMPTZ,
			baseline_window_end    TIMESTAMPTZ,
			current_fingerprint    TEXT,
			current_share_pct      INT,
			last_observed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_alerted_fingerprint TEXT,
			last_alerted_at        TIMESTAMPTZ,
			updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, credential_id, raw_model_name)
		);
		CREATE INDEX IF NOT EXISTS idx_integrity_fingerprint_baseline_cred_model
			ON integrity_fingerprint_baseline (credential_id, raw_model_name);
	`)
	if err != nil {
		return err
	}
	slog.Info("integrity_fingerprint_baseline schema ensured")
	return nil
}

// ensureModelsCanonicalStandardIQ (2026-08-11) mirrors migration 350 part 1:
// add standard_iq / standard_iq_source / standard_iq_updated_at columns to
// models_canonical. The value is the 0-100 Artificial Analysis Intelligence
// Index score (or a manual override), populated by cmd/fetch-standard-iq.
// Idempotent — safe to run repeatedly.
func (d *DB) ensureModelsCanonicalStandardIQ(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE models_canonical
		    ADD COLUMN IF NOT EXISTS standard_iq numeric(5,2),
		    ADD COLUMN IF NOT EXISTS standard_iq_source text DEFAULT 'artificialanalysis',
		    ADD COLUMN IF NOT EXISTS standard_iq_updated_at timestamptz;
		COMMENT ON COLUMN models_canonical.standard_iq IS '标准智商值（0-100，来自评测站点，默认 Artificial Analysis Intelligence Index）';
		COMMENT ON COLUMN models_canonical.standard_iq_source IS '标准智商数据来源标签，如 artificialanalysis / artificialanalysis-v4.1.1 / manual';
	`)
	if err != nil {
		return err
	}
	return nil
}

// ensureModelIQSchema (2026-08-11) mirrors migration 350 parts 2-3:
//   - model_iq_runs: append-only per-node IQ test runs (one row per test),
//     supports the "供应商模型列表点击查看不同时点智商值" feature.
//   - node_iq_latest: 1:1 cache of latest + aggregate IQ per node, read by
//     the provider model list and the provider-quality ModelIQ dimension.
//
// Node identity is (credential_id, raw_model_name), matching node_probe_state;
// no FK so binding rename/rewrite paths are unaffected. Idempotent.
func (d *DB) ensureModelIQSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS model_iq_runs (
			id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			credential_id   bigint NOT NULL,
			provider_id     bigint NOT NULL,
			raw_model_name  text NOT NULL,
			canonical_id    bigint,
			benchmark_type  text NOT NULL DEFAULT 'mmlu_lite',
			total_questions integer NOT NULL DEFAULT 0,
			correct_count   integer NOT NULL DEFAULT 0,
			accuracy        numeric(5,2) NOT NULL DEFAULT 0,
			stability       numeric(5,2),
			latency_p95     integer,
			overall_score   numeric(5,2) NOT NULL DEFAULT 0,
			grade           text,
			probe_kind      text NOT NULL DEFAULT 'direct',
			trigger_kind    text NOT NULL DEFAULT 'scheduled',
			status          text NOT NULL DEFAULT 'success',
			error           text,
			tested_at       timestamptz NOT NULL DEFAULT now(),
			created_at      timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT model_iq_runs_probe_kind_check CHECK (probe_kind IN ('gateway','direct','mock')),
			CONSTRAINT model_iq_runs_trigger_kind_check CHECK (trigger_kind IN ('scheduled','on_demand','anomaly')),
			CONSTRAINT model_iq_runs_status_check CHECK (status IN ('success','partial','failed'))
		);
		CREATE INDEX IF NOT EXISTS idx_model_iq_runs_node_time
			ON model_iq_runs(credential_id, raw_model_name, tested_at DESC);
		CREATE INDEX IF NOT EXISTS idx_model_iq_runs_provider_time
			ON model_iq_runs(provider_id, tested_at DESC);
		CREATE INDEX IF NOT EXISTS idx_model_iq_runs_canonical_time
			ON model_iq_runs(canonical_id, tested_at DESC);

		CREATE TABLE IF NOT EXISTS node_iq_latest (
			credential_id   bigint NOT NULL,
			raw_model_name  text NOT NULL,
			overall_score   numeric(5,2),
			grade           text,
			sample_count    integer NOT NULL DEFAULT 0,
			avg_score       numeric(5,2),
			min_score       numeric(5,2),
			max_score       numeric(5,2),
			tested_at       timestamptz,
			updated_at      timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (credential_id, raw_model_name)
		);
	`)
	if err != nil {
		return err
	}
	slog.Info("model_iq schema ensured")
	return nil
}

func (d *DB) ensurePassiveProbeStateSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS passive_probe_state (
		    credential_id       INTEGER NOT NULL,
		    raw_model_name      TEXT NOT NULL,
		    error_kind          TEXT NOT NULL,
		    consecutive_count   INTEGER NOT NULL DEFAULT 0,
		    total_recent_count  INTEGER NOT NULL DEFAULT 0,
		    window_total_count  INTEGER NOT NULL DEFAULT 0,
		    first_seen_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    in_reviewing        BOOLEAN NOT NULL DEFAULT FALSE,
		    reviewing_until     TIMESTAMPTZ,
		    final_marked_at     TIMESTAMPTZ,
		    unavailable_reason  TEXT,
		    last_response_body_preview TEXT,
		    PRIMARY KEY (credential_id, raw_model_name, error_kind)
		);
		CREATE INDEX IF NOT EXISTS idx_passive_probe_reviewing
		    ON passive_probe_state (in_reviewing, reviewing_until)
		    WHERE in_reviewing = TRUE;
		ALTER TABLE model_probe_state
		    ADD COLUMN IF NOT EXISTS last_unavailable_reason TEXT,
		    ADD COLUMN IF NOT EXISTS last_err_code TEXT,
		    ADD COLUMN IF NOT EXISTS next_retry_at_override TIMESTAMPTZ;
		CREATE INDEX IF NOT EXISTS idx_model_probe_state_retry
		    ON model_probe_state (state, next_retry_at)
		    WHERE state = 'recovering';
		CREATE OR REPLACE FUNCTION model_probe_backoff(consecutive_failures INTEGER)
		    RETURNS INTERVAL
		    LANGUAGE SQL
		    IMMUTABLE
		AS $$
		    SELECT CASE
			WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
			WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
			WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
			ELSE                                  INTERVAL '15 minutes'
		    END;
		$$;
	`)
	if err != nil {
		return err
	}
	slog.Info("passive_probe_state schema ensured (table + 1 index + 3 model_probe_state columns)")
	return nil
}

// ensureProbeWatchdogIndex keeps the healthy non-featured watchdog update
// indexed on every startup path, including deployments that do not replay
// historical SQL migration files.
func (d *DB) ensureProbeWatchdogIndex(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_mps_healthy_confirmed_next_retry
		    ON model_probe_state (next_retry_at)
		    WHERE state = 'healthy_confirmed';
	`)
	return err
}

// ensureProbeStateFunctionFixes patches probe state SQL functions from 301/302
// so they update the correct binding without raw_model_name-only LIMIT 1 lookups.
func (d *DB) ensureProbeStateFunctionFixes(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION model_probe_mark_available(
		    p_credential_id BIGINT,
		    p_raw_model_name TEXT,
		    p_latency_ms INTEGER DEFAULT 0
		)
		RETURNS VOID
		LANGUAGE plpgsql
		AS $$
		BEGIN
		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, next_retry_at, last_status,
		         state_expires_at, marked_suspicious_at)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'available',
		         1, 0,
		         NOW(), NOW() + INTERVAL '2 hours', 'ok',
		         NOW() + INTERVAL '2 hours', NULL)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'available',
		        consecutive_successes = model_probe_state.consecutive_successes + 1,
		        consecutive_failures = 0,
		        last_attempt_at = NOW(),
		        next_retry_at = NOW() + INTERVAL '2 hours',
		        last_status = 'ok',
		        state_expires_at = NOW() + INTERVAL '2 hours',
		        marked_suspicious_at = NULL,
		        probing_started_at = NULL;

		    UPDATE credential_model_bindings cmb
		    SET available = TRUE,
		        unavailable_reason = NULL,
		        unavailable_at = NULL,
		        unavailable_recover_at = NULL,
		        updated_at = NOW()
		    FROM provider_models pm
		    WHERE cmb.provider_model_id = pm.id
		      AND cmb.credential_id = p_credential_id
		      AND pm.raw_model_name = p_raw_model_name
		      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		      AND COALESCE(cmb.admin_protected, FALSE) = FALSE;
		END;
		$$;

		CREATE OR REPLACE FUNCTION model_probe_mark_unavailable(
		    p_credential_id BIGINT,
		    p_raw_model_name TEXT,
		    p_error_code TEXT,
		    p_error_message TEXT DEFAULT ''
		)
		RETURNS VOID
		LANGUAGE plpgsql
		AS $$
		BEGIN
		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, next_retry_at, last_status,
		         state_expires_at, marked_suspicious_at,
		         last_unavailable_reason, last_err_code)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'unavailable',
		         0, 1,
		         NOW(), NOW() + INTERVAL '15 minutes', 'http_4xx',
		         NOW() + INTERVAL '15 minutes', NULL,
		         p_error_message, p_error_code)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'unavailable',
		        consecutive_successes = 0,
		        consecutive_failures = model_probe_state.consecutive_failures + 1,
		        last_attempt_at = NOW(),
		        next_retry_at = NOW() + INTERVAL '15 minutes',
		        last_status = 'http_4xx',
		        state_expires_at = NOW() + INTERVAL '15 minutes',
		        marked_suspicious_at = NULL,
		        probing_started_at = NULL,
		        last_unavailable_reason = p_error_message,
		        last_err_code = p_error_code;

		    UPDATE credential_model_bindings cmb
		    SET available = FALSE,
		        unavailable_reason = 'probe_' || p_error_code,
		        unavailable_at = NOW(),
		        unavailable_recover_at = NOW() + INTERVAL '15 minutes',
		        updated_at = NOW()
		    FROM provider_models pm
		    WHERE cmb.provider_model_id = pm.id
		      AND cmb.credential_id = p_credential_id
		      AND pm.raw_model_name = p_raw_model_name
		      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		      AND COALESCE(cmb.admin_protected, FALSE) = FALSE;
		END;
		$$;

		CREATE OR REPLACE FUNCTION unified_probe_mark_healthy(
		    p_credential_id BIGINT,
		    p_raw_model_name TEXT,
		    p_latency_ms INTEGER DEFAULT 0
		)
		RETURNS VOID
		LANGUAGE plpgsql
		AS $$
		DECLARE
		    new_interval INTERVAL;
		BEGIN
		    SELECT CASE
		        WHEN consecutive_watchdog_successes >= 10 THEN '8 hours'::INTERVAL
		        WHEN consecutive_watchdog_successes >= 5 THEN '6 hours'::INTERVAL
		        WHEN consecutive_watchdog_successes >= 2 THEN '4 hours'::INTERVAL
		        ELSE '2 hours'::INTERVAL
		    END INTO new_interval
		    FROM model_probe_state
		    WHERE credential_id = p_credential_id
		      AND raw_model_name = p_raw_model_name;

		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, last_verified_at, next_retry_at,
		         probe_priority, verification_interval,
		         consecutive_watchdog_successes,
		         last_status, probing_started_at)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'healthy',
		         1, 0,
		         NOW(), NOW(), NOW() + COALESCE(new_interval, '4 hours'::INTERVAL),
		         'watchdog', COALESCE(new_interval, '4 hours'::INTERVAL),
		         1,
		         'ok', NULL)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'healthy',
		        consecutive_successes = model_probe_state.consecutive_successes + 1,
		        consecutive_failures = 0,
		        last_attempt_at = NOW(),
		        last_verified_at = NOW(),
		        next_retry_at = NOW() + COALESCE(new_interval, model_probe_state.verification_interval, '4 hours'::INTERVAL),
		        probe_priority = 'watchdog',
		        verification_interval = COALESCE(new_interval, model_probe_state.verification_interval),
		        consecutive_watchdog_successes = CASE
		            WHEN model_probe_state.probe_priority = 'watchdog' THEN model_probe_state.consecutive_watchdog_successes + 1
		            ELSE 1
		        END,
		        last_status = 'ok',
		        probing_started_at = NULL,
		        state_expires_at = NULL,
		        marked_suspicious_at = NULL;

		    UPDATE credential_model_bindings cmb
		    SET available = TRUE,
		        unavailable_reason = NULL,
		        unavailable_at = NULL,
		        unavailable_recover_at = NULL,
		        updated_at = NOW()
		    FROM provider_models pm
		    WHERE cmb.provider_model_id = pm.id
		      AND cmb.credential_id = p_credential_id
		      AND pm.raw_model_name = p_raw_model_name
		      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		      AND COALESCE(cmb.admin_protected, FALSE) = FALSE;
		END;
		$$;

		CREATE OR REPLACE FUNCTION unified_probe_mark_failing(
		    p_credential_id BIGINT,
		    p_raw_model_name TEXT,
		    p_error_code TEXT,
		    p_error_message TEXT DEFAULT '',
		    p_retry_after_seconds INTEGER DEFAULT 60
		)
		RETURNS VOID
		LANGUAGE plpgsql
		AS $$
		DECLARE
		    current_failures INTEGER;
		    backoff_seconds INTEGER;
		BEGIN
		    SELECT COALESCE(consecutive_failures, 0) INTO current_failures
		    FROM model_probe_state
		    WHERE credential_id = p_credential_id
		      AND raw_model_name = p_raw_model_name;

		    backoff_seconds := LEAST(
		        p_retry_after_seconds * POWER(2, LEAST(current_failures, 6)),
		        3600
		    );

		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, next_retry_at,
		         probe_priority, last_status,
		         last_unavailable_reason, last_err_code,
		         probing_started_at, consecutive_watchdog_successes)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'failing',
		         0, 1,
		         NOW(), NOW() + (backoff_seconds || ' seconds')::INTERVAL,
		         'failing', 'http_error',
		         p_error_message, p_error_code,
		         NULL, 0)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'failing',
		        consecutive_successes = 0,
		        consecutive_failures = model_probe_state.consecutive_failures + 1,
		        last_attempt_at = NOW(),
		        next_retry_at = NOW() + (backoff_seconds || ' seconds')::INTERVAL,
		        probe_priority = 'failing',
		        last_status = 'http_error',
		        last_unavailable_reason = p_error_message,
		        last_err_code = p_error_code,
		        probing_started_at = NULL,
		        consecutive_watchdog_successes = 0,
		        state_expires_at = NULL;

		    UPDATE credential_model_bindings cmb
		    SET available              = FALSE,
		        unavailable_reason     = 'probe_' || p_error_code,
		        unavailable_at         = NOW(),
		        unavailable_recover_at = NOW() + LEAST(backoff_seconds, 900) * INTERVAL '1 second',
		        updated_at             = NOW()
		    FROM provider_models pm
		    WHERE cmb.provider_model_id = pm.id
		      AND cmb.credential_id = p_credential_id
		      AND pm.raw_model_name = p_raw_model_name
		      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		      AND COALESCE(cmb.admin_protected, FALSE) = FALSE;
		END;
		$$;
	`)
	if err != nil {
		return err
	}
	slog.Info("probe state function fixes ensured (raw_model-only binding updates removed)")
	return nil
}

// ensureNodeProbeTriggerKindSchema mirrors startup migration 538. Existing
// deployments may have the tables with the old CHECK definitions, while some
// older installations may not have the tables yet; only converge constraints
// when the corresponding table exists.
func (d *DB) ensureNodeProbeTriggerKindSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		DO $$
		BEGIN
			IF to_regclass('public.node_probe_runs') IS NOT NULL THEN
				-- 2026-09-23 审计轮守卫：ADD CONSTRAINT CHECK 每次要全表校验
				-- 且持 ACCESS EXCLUSIVE，node_probe_runs 大表上必超 30s 被
				-- rolconfig 击杀（245 seq 2201 boot 57014 实锤）——约束已存在
				-- 则整段跳过，不再每 boot DROP+ADD 重校验。
				IF NOT EXISTS (
					SELECT 1 FROM pg_constraint
					WHERE conname = 'node_probe_runs_trigger_kind_check'
					  AND conrelid = 'public.node_probe_runs'::regclass
				) THEN
					ALTER TABLE public.node_probe_runs
						DROP CONSTRAINT IF EXISTS node_probe_runs_trigger_kind_check;
					ALTER TABLE public.node_probe_runs
						ADD CONSTRAINT node_probe_runs_trigger_kind_check CHECK (
							trigger_kind IN (
								'request_failure', 'manual', 'credential_recovery',
								'sync_request', 'periodic', 'admin',
								'integrity_probe_planner', 'selfcheck', 'external_async'
							)
						);
				END IF;
			END IF;
			IF to_regclass('public.credential_probe_queue') IS NOT NULL THEN
				IF NOT EXISTS (
					SELECT 1 FROM pg_constraint
					WHERE conname = 'credential_probe_queue_source_check'
					  AND conrelid = 'public.credential_probe_queue'::regclass
				) THEN
					ALTER TABLE public.credential_probe_queue
						DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check;
					ALTER TABLE public.credential_probe_queue
						ADD CONSTRAINT credential_probe_queue_source_check CHECK (
							source IN (
								'request_failure', 'periodic', 'external_async', 'admin',
								'integrity_probe_planner', 'selfcheck'
							)
						);
				END IF;
			END IF;
		END
		$$;
	`)
	if err != nil {
		return fmt.Errorf("ensure migration 538 probe constraints: %w", err)
	}
	return nil
}

// ensureTenantModelPoliciesSchema mirrors
// db/migrations/024_tenant_model_policies.sql for startup apply.
// Idempotent. Creates:
//  1. tenant_model_policies table (Pattern A: tenant_id NOT NULL, RLS)
//  2. tenant_model_policies_active view (excludes soft-deleted rows)
//  3. tenant_model_policies_audit table + trigger
//
// Without this, internal/modelpolicy/Checker.IsForbidden would
// return false (fail-open) for all tenants because the table would
// not exist, masking the gate as if it were never wired.
func (d *DB) ensureTenantModelPoliciesSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS tenant_model_policies (
		    id              BIGSERIAL PRIMARY KEY,
		    tenant_id       VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
		    canonical_name  TEXT NOT NULL,
		    reason          TEXT NOT NULL DEFAULT '',
		    created_by      VARCHAR(128) NOT NULL DEFAULT '',
		    deleted_at      TIMESTAMPTZ,
		    deleted_by      VARCHAR(128),
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    UNIQUE (tenant_id, canonical_name),
		    CHECK (canonical_name <> '')
		);
		CREATE INDEX IF NOT EXISTS idx_tmp_tenant_active
		    ON tenant_model_policies (tenant_id) WHERE deleted_at IS NULL;
		CREATE INDEX IF NOT EXISTS idx_tmp_canonical
		    ON tenant_model_policies (canonical_name);

		ALTER TABLE tenant_model_policies ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS tenant_isolation_tmp ON public.tenant_model_policies;
		CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies
		    USING ((tenant_id)::text = (public.get_current_tenant())::text);

		CREATE OR REPLACE VIEW tenant_model_policies_active AS
		    SELECT id, tenant_id, canonical_name, reason, created_by,
		           created_at, updated_at
		    FROM tenant_model_policies
		    WHERE deleted_at IS NULL;

		CREATE TABLE IF NOT EXISTS tenant_model_policies_audit (
		    id              BIGSERIAL PRIMARY KEY,
		    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
		    action          TEXT NOT NULL CHECK (action IN ('insert','update','delete','undelete')),
		    policy_id       BIGINT,
		    tenant_id       TEXT,
		    canonical_name  TEXT,
		    reason          TEXT,
		    actor           TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_tmp_audit_ts ON tenant_model_policies_audit (ts DESC);
		CREATE INDEX IF NOT EXISTS idx_tmp_audit_tenant_ts ON tenant_model_policies_audit (tenant_id, ts DESC);
		ALTER TABLE tenant_model_policies_audit ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS tenant_isolation_tmp_audit ON public.tenant_model_policies_audit;
		CREATE POLICY tenant_isolation_tmp_audit ON public.tenant_model_policies_audit
		    USING ((tenant_id)::text = (public.get_current_tenant())::text
		           OR (tenant_id) IS NULL);

		CREATE OR REPLACE FUNCTION tenant_model_policies_audit_fn()
		RETURNS TRIGGER AS $$
		DECLARE
		    v_actor TEXT := COALESCE(
		        NULLIF(current_setting('app.current_admin', true), ''),
		        'system'
		    );
		BEGIN
		    IF (TG_OP = 'INSERT') THEN
		        INSERT INTO tenant_model_policies_audit
		            (action, policy_id, tenant_id, canonical_name, reason, actor)
		        VALUES
		            ('insert', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		        RETURN NEW;
		    ELSIF (TG_OP = 'UPDATE') THEN
		        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
		            IF NEW.deleted_at IS NULL THEN
		                INSERT INTO tenant_model_policies_audit
		                    (action, policy_id, tenant_id, canonical_name, reason, actor)
		                VALUES
		                    ('undelete', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		            ELSE
		                INSERT INTO tenant_model_policies_audit
		                    (action, policy_id, tenant_id, canonical_name, reason, actor)
		                VALUES
		                    ('delete', NEW.id, NEW.tenant_id, NEW.canonical_name, OLD.reason, v_actor);
		            END IF;
		        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
		              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
		        THEN
		            INSERT INTO tenant_model_policies_audit
		                (action, policy_id, tenant_id, canonical_name, reason, actor)
		            VALUES
		                ('update', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		        END IF;
		        RETURN NEW;
		    ELSIF (TG_OP = 'DELETE') THEN
		        INSERT INTO tenant_model_policies_audit
		            (action, policy_id, tenant_id, canonical_name, reason, actor)
		        VALUES
		            ('delete', OLD.id, OLD.tenant_id, OLD.canonical_name, OLD.reason, v_actor);
		        RETURN OLD;
		    END IF;
		    RETURN NULL;
		END;
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS tenant_model_policies_audit_trg ON tenant_model_policies;
		CREATE TRIGGER tenant_model_policies_audit_trg
		    AFTER INSERT OR UPDATE OR DELETE ON tenant_model_policies
		    FOR EACH ROW EXECUTE FUNCTION tenant_model_policies_audit_fn();
	`)
	if err != nil {
		return err
	}
	slog.Info("tenant_model_policies schema ensured (table + RLS + active view + audit trigger)")
	return nil
}

// ensureSupplementalRLS — Round 48 (2026-06-21)
//
// Adds RLS policies to tables whose CREATE TABLE statements live in
// earlier migrations owned by other projects (022/023 settings,
// 025 tool_registry). Without this, pg-rls-lint flags L1 for the
// five pre-existing tenant-scoped tables and the cross-tenant
// defense-in-depth guarantee is missing in production.
//
// Idempotent (DROP POLICY IF EXISTS guard).  We do NOT modify the
// original migrations; this function applies the same CREATE
// POLICY statements that 026_supplemental_rls.sql contains so the
// linter and the live DB stay in sync even if the .sql file
// never gets re-applied.
func (d *DB) ensureSupplementalRLS(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
			ALTER TABLE tenant_settings_kv ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv;
			CREATE POLICY tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv
			    USING ((tenant_id)::text = (public.get_current_tenant())::text);

			ALTER TABLE settings_audit ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_settings_audit ON public.settings_audit;
			CREATE POLICY tenant_isolation_settings_audit ON public.settings_audit
			    USING ((tenant_id)::text = (public.get_current_tenant())::text
			           OR (tenant_id) IS NULL);

			ALTER TABLE tenant_tool_policies ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies;
			CREATE POLICY tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies
			    USING ((tenant_id)::text = (public.get_current_tenant())::text);

			ALTER TABLE tool_call_events ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_tool_call_events ON public.tool_call_events;
			CREATE POLICY tenant_isolation_tool_call_events ON public.tool_call_events
			    USING ((tenant_id)::text = (public.get_current_tenant())::text);

			ALTER TABLE tool_usage_stats ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_tool_usage_stats ON public.tool_usage_stats;
			CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats
			    USING ((tenant_id)::text = (public.get_current_tenant())::text);

			-- 2026-06-21 audit: tool_registry also has tenant_id column
			-- (added in 028_tool_registry_extensions.sql) but no RLS policy.
			-- Without RLS, any tenant can SELECT/INSERT/UPDATE another tenant's
			-- tool_registry rows. Idempotent: drop-if-exists + recreate.
			ALTER TABLE tool_registry ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_tool_registry ON public.tool_registry;
			CREATE POLICY tenant_isolation_tool_registry ON public.tool_registry
			    USING ((tenant_id)::text = (public.get_current_tenant())::text
			           OR (tenant_id) IS NULL OR (tenant_id) = 'default');
		`)
	if err != nil {
		return err
	}
	slog.Info("supplemental RLS ensured (tenant_settings_kv, settings_audit, tenant_tool_policies, tool_call_events, tool_usage_stats, tool_registry)")
	return nil
}

// ensureAnalysisEventsRLS — 2026-07-01 (round 50 audit fix)
//
// Adds RLS to public.analysis_events and public.intent_aggregates. The two
// CREATE TABLE statements live in migrations 306/309 but the original authors
// deferred RLS ("加 RLS-friendly 列 (tenant_id) 便于后续多租户过滤" — 306.sql:14).
// This function reapplies RLS at startup so the linter and the live DB stay
// in sync even if the .sql files never get re-applied (consistent with the
// ensureSupplementalRLS pattern above).
//
// Two policies per table (matches 316_output_compliance_monitoring convention):
//   - tenant_isolation_<table>: USING (tenant_id = get_current_tenant())
//   - <table>_super_admin_bypass: USING (app.bypass_rls OR app.current_role = 'super_admin')
//
// Writers (publisher.go, intent_store.go) now wrap INSERT in a tx with
// `SET LOCAL app.bypass_rls = 'true'` so they can write across tenants.
func (d *DB) ensureAnalysisEventsRLS(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// These tables may not exist (e.g. on older deployments without the full
	// migration history); skip gracefully rather than blocking DB startup.
	for _, tbl := range []string{"public.analysis_events", "public.intent_aggregates"} {
		var exists bool
		if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`, tbl[7:]).Scan(&exists); err != nil {
			slog.Warn("analysis_events RLS: table existence check failed", "table", tbl, "error", err)
			continue
		}
		if !exists {
			slog.Warn("analysis_events RLS: table does not exist (skipping)", "table", tbl)
			continue
		}
		if _, err := d.pool.Exec(ctx, fmt.Sprintf(`
			ALTER TABLE %s ENABLE ROW LEVEL SECURITY;
			DROP POLICY IF EXISTS tenant_isolation_%s ON %s;
			CREATE POLICY tenant_isolation_%s ON %s
			    USING ((tenant_id)::text = (public.get_current_tenant())::text);
			DROP POLICY IF EXISTS %s_super_admin_bypass ON %s;
			CREATE POLICY %s_super_admin_bypass ON %s
			    USING (
			        current_setting('app.current_role', true) = 'super_admin'
			        OR current_setting('app.bypass_rls', true) = 'true'
			    );
		`, tbl, strings.Replace(tbl[7:], ".", "_", 1), tbl, strings.Replace(tbl[7:], ".", "_", 1), tbl, strings.Replace(tbl[7:], ".", "_", 1), tbl, strings.Replace(tbl[7:], ".", "_", 1), tbl)); err != nil {
			slog.Warn("analysis_events RLS: apply failed", "table", tbl, "error", err)
		} else {
			slog.Info("analysis_events RLS ensured", "table", tbl)
		}
	}
	return nil
}

// ensureApplicationsTable creates the applications table (used by api_keys
// for tenant-scoped application_code references) and seeds a default
// 'admin' application if missing. The applications table is referenced
// by admin/authentication.go's verifyAdminAuth, which requires app.code == "admin"
// to authorize legacy admin API keys (sk-...).
//
// Without this, monitor-summary and other super-admin endpoints return
// 401 because the api_keys.application_id points to a non-existent
// applications row.
//
// Mirrors the schema implied by admin/keys.go (getOrCreateApplication).
func (d *DB) ensureApplicationsTable(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS applications (
		    id                     BIGSERIAL PRIMARY KEY,
		    tenant_id              TEXT NOT NULL DEFAULT 'default',
		    code                   TEXT NOT NULL,
		    display_name           TEXT NOT NULL,
		    owner_user             TEXT,
		    data_sensitivity       TEXT NOT NULL DEFAULT 'internal',
		    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
		    notes                  TEXT,
		    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    default_client_profile TEXT,
		    allowed_models_json    JSONB,
		    CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code),
		    CONSTRAINT applications_data_sensitivity_check
		        CHECK (data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text]))
		);
		-- idx_applications_tenant_code (partial on tenant_id,code) was removed in
		-- R38/719: shadowed by applications_tenant_id_code_key UNIQUE above.

		-- Seed default 'admin' application for super-admin authentication.
		-- Explicit id=1 to match existing api_keys.application_id references
		-- (legacy data: 8 keys reference application_id=1, which was the
		-- admin app before the applications table was wiped). Using id=1
		-- with ON CONFLICT (id) DO NOTHING keeps this idempotent.
		INSERT INTO applications (id, tenant_id, code, display_name, owner_user, data_sensitivity, enabled)
		VALUES (1, 'default', 'admin', 'Admin Console', 'admin', 'confidential', TRUE)
		ON CONFLICT (id) DO NOTHING;

		-- Seed 'applicant' application for the public /v1/keys/apply flow.
		-- admin/keys.go handleV1KeysApply also references this code.
		INSERT INTO applications (tenant_id, code, display_name, owner_user, data_sensitivity, enabled)
		VALUES ('default', 'applicant', 'API Key Applicant', 'public', 'public', TRUE)
		ON CONFLICT (tenant_id, code) DO NOTHING;

		-- Reset sequence to MAX(id)+1 so future inserts don't collide.
		-- Safe even on fresh DBs (MAX returns NULL → setval to 1).
		SELECT setval(pg_get_serial_sequence('applications', 'id'),
		              GREATEST(COALESCE(MAX(id), 0), 1), true)
		FROM applications;
	`)
	if err != nil {
		return err
	}
	slog.Info("applications schema ensured (table + admin + applicant seed)")
	return nil
}

// ensureCredentialColumns adds columns from db/migrations/033-034 that
// have not been picked up by an ensure* function yet.
//
// 033_credential_model_call_history.sql — call_history table (consumed
//
//	by bg/call_history_aggregator.go's GetRecent).
//
// 034_concurrency_limit_auto.sql — credentials.concurrency_limit_auto
//
//	(consumed by admin/credential_monitor.go's monitor-summary).
//
// Without these, monitor-summary returns 500 ("column does not exist")
// and call-history aggregation silently no-ops.
func (d *DB) ensureCredentialColumns(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// D1 残余（2026-09-23 审计轮）：credentials 热表 ALTER/UPDATE 家族守卫——
	// 245 seq 2200 boot 实测本步 15s 锁等待（预算 75s 吃掉 1/5）。列、表、
	// 索引全在位时跳过（backfill UPDATE 的 IS NULL 谓词在成熟库为空集）。
	if d.columnsAllPresent(ctx, "credentials", []string{"concurrency_limit_auto"}) {
		var have int
		if err := d.pool.QueryRow(ctx, `
			SELECT count(*) FROM (
			  SELECT 1 WHERE to_regclass('public.credential_model_call_history') IS NULL
			  UNION ALL
			  SELECT 1 FROM unnest(ARRAY['idx_call_history_cred_time','idx_call_history_model_time']) AS want(name)
			  WHERE NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname=want.name)
			) t`).Scan(&have); err == nil && have == 0 {
			slog.Info("credential columns ensured (catalog short-circuit)")
			return nil
		}
	}
	_, err := d.pool.Exec(ctx, `
		-- 034: credentials.concurrency_limit_auto
		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS concurrency_limit_auto INT;
		CREATE INDEX IF NOT EXISTS idx_credentials_auto_limit
		    ON credentials (concurrency_limit_auto)
		    WHERE concurrency_limit_auto IS NOT NULL;
		UPDATE credentials
		    SET concurrency_limit_auto = COALESCE(concurrency_limit, 5)
		    WHERE concurrency_limit_auto IS NULL;

		-- 033: credential_model_call_history (sliding window for the
		-- credential monitor UI; consumed by CallHistoryAggregator)
		-- 🆕 2026-06-23 真实表 schema (从 71 llm-pg-71 docker 内 psql 远程验证):
		--   credential_id, raw_model, window_start, total_calls, success_calls,
		--   failed_calls, avg_latency_ms, p95_latency_ms, p99_latency_ms, ...
		-- 老 schema (raw_model_name + ts per-call) 是 design 错位, 已不创建
		CREATE TABLE IF NOT EXISTS credential_model_call_history (
		    credential_id          BIGINT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
		    raw_model              TEXT NOT NULL,
		    window_start           TIMESTAMPTZ NOT NULL,
		    total_calls            INT NOT NULL DEFAULT 0,
		    success_calls          INT NOT NULL DEFAULT 0,
		    failed_calls           INT NOT NULL DEFAULT 0,
		    avg_latency_ms         NUMERIC(8,2),
		    p95_latency_ms         INT,
		    p99_latency_ms         INT,
		    error_rate_limit_count INT NOT NULL DEFAULT 0,
		    error_quota_count      INT NOT NULL DEFAULT 0,
		    error_concurrent_count INT NOT NULL DEFAULT 0,
		    error_network_count    INT NOT NULL DEFAULT 0,
		    error_auth_count       INT NOT NULL DEFAULT 0,
		    error_other_count      INT NOT NULL DEFAULT 0,
		    avg_concurrent         NUMERIC(5,2),
		    peak_concurrent        INT,
		    created_at             TIMESTAMPTZ DEFAULT now(),
		    PRIMARY KEY (credential_id, raw_model, window_start)
		);
		CREATE INDEX IF NOT EXISTS idx_call_history_cred_time
		    ON credential_model_call_history (credential_id, window_start DESC);
		CREATE INDEX IF NOT EXISTS idx_call_history_model_time
		    ON credential_model_call_history (raw_model, window_start DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("credential columns ensured (concurrency_limit_auto, credential_model_call_history)")
	return nil
}

// ensureFpSlotLimit mirrors db/migrations/036_fp_slot_limit.sql.
//
// Adds credentials.fp_slot_limit (INT NOT NULL DEFAULT 20) and the
// credentials_fp_slot_limit_check CHECK constraint, plus the
// system_identity_pool singleton for the global end-user cap.
//
// Why this matters: admin/provider_credential.go (listCredentials,
// addCredential, updateCredential, resetCredentialFpSlots,
// getCredentialFpSlotStats) and provider/client.go (loadCandidatesDB)
// all reference c.fp_slot_limit. Without this column, every SELECT /
// INSERT / UPDATE on those paths returns SQLSTATE 42703
// ("column does not exist") and surfaces to the API as 500 — most
// visibly on GET /api/providers/{id}/credentials and on every
// /v1/chat/completions request that needs to load candidates.
//
// Mirrors the SQL in db/migrations/036_fp_slot_limit.sql so the
// in-process migration runner covers this even if the .sql file was
// never applied by an external tool. Idempotent (ADD COLUMN IF NOT
// EXISTS, UPDATE guarded by IS NULL, NOT NULL via information_schema
// check, CHECK via pg_constraint check).
//
// Runs at startup via ensureSchema so every gateway instance
// (184 k3s + 71 host docker) converges on the same schema without
// needing a separate migration runner.
func (d *DB) ensureFpSlotLimit(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// D1 残余：credentials.fp_slot_limit + system_identity_pool 全在位时跳过
	// （245 seq 2200 boot 实测本步 10s 锁等待；同 columnsAllPresent 守卫族）。
	if d.columnsAllPresent(ctx, "credentials", []string{"fp_slot_limit"}) {
		var missingTable int
		if err := d.pool.QueryRow(ctx,
			`SELECT count(*) FROM (SELECT 1) AS one
			 WHERE to_regclass('public.system_identity_pool') IS NULL`,
		).Scan(&missingTable); err == nil && missingTable == 0 {
			slog.Info("fp_slot_limit schema ensured (catalog short-circuit)")
			return nil
		}
	}
	_, err := d.pool.Exec(ctx, `
		-- 036: credentials.fp_slot_limit (fingerprint slot pool size,
		-- distinct from concurrency_limit which is in-flight requests).
		ALTER TABLE credentials
		    ADD COLUMN IF NOT EXISTS fp_slot_limit INT;

		-- Backfill existing rows that have NULL fp_slot_limit. The
		-- UPDATE is wrapped in a DO block guarded by IS NULL so it's
		-- a no-op once the column has been backfilled on a prior boot.
		DO $$
		BEGIN
		    IF EXISTS (
		        SELECT 1 FROM credentials WHERE fp_slot_limit IS NULL
		    ) THEN
		        UPDATE credentials SET fp_slot_limit = 20 WHERE fp_slot_limit IS NULL;  -- 2026-06-24: 5→20
		    END IF;
		END $$;

		-- Apply NOT NULL if not already enforced. Postgres has no
		-- ADD NOT NULL IF NOT EXISTS, so check information_schema.
		DO $$
		BEGIN
		    IF EXISTS (
		        SELECT 1
		        FROM information_schema.columns
		        WHERE table_name = 'credentials'
		          AND column_name = 'fp_slot_limit'
		          AND is_nullable = 'YES'
		    ) THEN
		        ALTER TABLE credentials ALTER COLUMN fp_slot_limit SET NOT NULL;
		    END IF;
		END $$;

		-- CHECK constraint: 0=unlimited, >0=explicit pool size, max 10000.
		DO $$
		BEGIN
		    IF NOT EXISTS (
		        SELECT 1 FROM pg_constraint
		        WHERE conname = 'credentials_fp_slot_limit_check'
		          AND conrelid = 'credentials'::regclass
		    ) THEN
		        ALTER TABLE credentials
		            ADD CONSTRAINT credentials_fp_slot_limit_check
		            CHECK (fp_slot_limit >= 0 AND fp_slot_limit <= 10000);
		    END IF;
		END $$;

		-- 036 also creates system_identity_pool (global end-user cap).
		CREATE TABLE IF NOT EXISTS system_identity_pool (
		    id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
		    max_identities INT NOT NULL DEFAULT 10000,
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_by TEXT
		);
		INSERT INTO system_identity_pool (id, max_identities)
		VALUES (1, 10000)
		ON CONFLICT (id) DO NOTHING;
	`)
	if err != nil {
		return err
	}
	slog.Info("fp_slot_limit schema ensured (credentials.fp_slot_limit + system_identity_pool)")
	return nil
}

// ensureConcurrencyMode adds the concurrency_mode / tpm_limit / max_queue_depth /
// max_queue_wait_ms columns to credentials and backfills concurrency_mode from
// the existing rpm_limit / concurrency_limit values.
//
// Mirrors sql/migrations/startup/479_concurrency_mode.sql so the in-process
// runner covers this even if the .sql file was never applied externally.
// Idempotent (ADD COLUMN IF NOT EXISTS, backfill guarded by IS NULL, CHECK via
// pg_constraint). Runs at startup via ensureSchema. See
// docs/会话优化v2/57-多层队列调度架构设计方案.md.
func (d *DB) ensureConcurrencyMode(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// D1 残余：4 列全在位时跳过（245 seq 2200 boot 实测本步 10s 锁等待；
	// backfill UPDATE 的 IS NULL 谓词在成熟库为空集，同守卫族）。
	if d.columnsAllPresent(ctx, "credentials", []string{
		"concurrency_mode", "tpm_limit", "max_queue_depth", "max_queue_wait_ms",
	}) {
		slog.Info("concurrency_mode schema ensured (catalog short-circuit)")
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE credentials ADD COLUMN IF NOT EXISTS concurrency_mode TEXT;
		ALTER TABLE credentials ADD COLUMN IF NOT EXISTS tpm_limit INT;
		ALTER TABLE credentials ADD COLUMN IF NOT EXISTS max_queue_depth INT;
		ALTER TABLE credentials ADD COLUMN IF NOT EXISTS max_queue_wait_ms INT;

		-- Backfill: 有 rpm_limit 且无并发数 → rpm；否则 concurrency。
		UPDATE credentials SET concurrency_mode = 'rpm'
		 WHERE concurrency_mode IS NULL
		   AND rpm_limit IS NOT NULL
		   AND concurrency_limit IS NULL;
		UPDATE credentials SET concurrency_mode = 'concurrency'
		 WHERE concurrency_mode IS NULL;

		-- DEFAULT + NOT NULL（仅当当前可空时）。
		DO $$
		BEGIN
		    IF EXISTS (
		        SELECT 1 FROM information_schema.columns
		        WHERE table_name = 'credentials' AND column_name = 'concurrency_mode' AND is_nullable = 'YES'
		    ) THEN
		        ALTER TABLE credentials ALTER COLUMN concurrency_mode SET DEFAULT 'concurrency';
		        ALTER TABLE credentials ALTER COLUMN concurrency_mode SET NOT NULL;
		    END IF;
		END $$;

		-- CHECK 约束（幂等）。
		DO $$
		BEGIN
		    IF NOT EXISTS (
		        SELECT 1 FROM pg_constraint
		        WHERE conname = 'credentials_concurrency_mode_check'
		          AND conrelid = 'credentials'::regclass
		    ) THEN
		        ALTER TABLE credentials
		            ADD CONSTRAINT credentials_concurrency_mode_check
		            CHECK (concurrency_mode IN ('concurrency','rpm','tpm','disabled'));
		    END IF;
		END $$;
	`)
	if err != nil {
		return err
	}
	slog.Info("concurrency_mode schema ensured (credentials.concurrency_mode/tpm_limit/max_queue_depth/max_queue_wait_ms)")
	return nil
}

// ensureCredentialGovernorRevision mirrors migration 566. The listener gets
// only a revision payload, so the sequence is global and publishers can safely
// catch up using revision > lastSeen.
func (d *DB) ensureCredentialGovernorRevision(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE public.credentials
			ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;
		CREATE SEQUENCE IF NOT EXISTS public.credentials_governor_revision_seq
			AS BIGINT START WITH 1 MINVALUE 1;
		SELECT setval(
			'public.credentials_governor_revision_seq',
			GREATEST(
				COALESCE((SELECT MAX(revision) FROM public.credentials), 0),
				COALESCE(pg_sequence_last_value('public.credentials_governor_revision_seq'), 0),
				1
			),
			COALESCE((SELECT MAX(revision) FROM public.credentials), 0) > 0
				OR pg_sequence_last_value('public.credentials_governor_revision_seq') IS NOT NULL
		);
		CREATE INDEX IF NOT EXISTS credentials_revision_idx ON public.credentials (revision);
		COMMENT ON COLUMN public.credentials.revision IS
			'Globally monotonic governor policy revision. Bumped by governor-relevant credential writes and consumed by domains/dispatch/policy_publisher.go via LISTEN credentials_revision.';

		CREATE OR REPLACE FUNCTION public.bump_credentials_governor_revision()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF TG_OP = 'INSERT' THEN
				NEW.revision := nextval('public.credentials_governor_revision_seq');
			ELSIF OLD.concurrency_limit IS DISTINCT FROM NEW.concurrency_limit
			   OR OLD.concurrency_mode IS DISTINCT FROM NEW.concurrency_mode
			   OR OLD.rpm_limit IS DISTINCT FROM NEW.rpm_limit
			   OR OLD.tpm_limit IS DISTINCT FROM NEW.tpm_limit
			   OR OLD.fp_slot_limit IS DISTINCT FROM NEW.fp_slot_limit
			   OR OLD.max_queue_depth IS DISTINCT FROM NEW.max_queue_depth
			   OR OLD.max_queue_wait_ms IS DISTINCT FROM NEW.max_queue_wait_ms THEN
				NEW.revision := nextval('public.credentials_governor_revision_seq');
			ELSE
				NEW.revision := OLD.revision;
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE OR REPLACE FUNCTION public.notify_credentials_governor_revision()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_notify('credentials_revision', NEW.revision::text);
			RETURN NEW;
		END;
		$$;

		DROP TRIGGER IF EXISTS trg_bump_credentials_governor_revision ON public.credentials;
		CREATE TRIGGER trg_bump_credentials_governor_revision
		BEFORE INSERT OR UPDATE OF concurrency_limit, concurrency_mode, rpm_limit,
			tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms
		ON public.credentials FOR EACH ROW
		EXECUTE FUNCTION public.bump_credentials_governor_revision();

		DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_insert ON public.credentials;
		CREATE TRIGGER trg_notify_credentials_governor_revision_insert
		AFTER INSERT ON public.credentials FOR EACH ROW
		EXECUTE FUNCTION public.notify_credentials_governor_revision();

		DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_update ON public.credentials;
		CREATE TRIGGER trg_notify_credentials_governor_revision_update
		AFTER UPDATE OF concurrency_limit, concurrency_mode, rpm_limit, tpm_limit,
			fp_slot_limit, max_queue_depth, max_queue_wait_ms
		ON public.credentials FOR EACH ROW
		WHEN (OLD.revision IS DISTINCT FROM NEW.revision)
		EXECUTE FUNCTION public.notify_credentials_governor_revision();

		DROP TRIGGER IF EXISTS trg_notify_auto_route_creds ON public.credentials;
		CREATE TRIGGER trg_notify_auto_route_creds
		AFTER UPDATE OF status, availability_state, quota_state, circuit_state,
			concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit,
			max_queue_depth, max_queue_wait_ms, lifecycle_status, manual_disabled
		ON public.credentials FOR EACH ROW
		WHEN (OLD.* IS DISTINCT FROM NEW.*)
		EXECUTE FUNCTION public.notify_auto_route_refresh();
	`)
	if err != nil {
		return err
	}
	slog.Info("credential governor revision schema ensured")
	return nil
}

// ensureRoutingRecentSuccessRate mirrors the live request_logs_hot definition
// from sql/migrations/startup/406_recent_success_rate_read_hot.sql.
//
// Two parts, both idempotent:
//  1. Backfill: any binding whose (credential, model) pair is currently
//     model_probe_state='broken_confirmed' gets cmb.available=FALSE. This
//     covers bindings that reached broken_confirmed before the P4 propagation
//     code (2026-06-19) landed — e.g. cred-11/minimax-m3 from 2026-06-17 —
//     which otherwise stay available=TRUE forever and keep re-entering the
//     candidate pool.
//  2. recent_success_rate(cred, model, sample_n) helper used by
//     loadCandidatesDB so the last-N gate is a single SQL expression. The
//     function is STABLE and uses the request_logs_hot composite index for a
//     50-row index descent.
//
// Runs at startup via ensureSchema so every gateway instance converges on the
// same function definition without a separate migration runner.
func (d *DB) ensureRoutingRecentSuccessRate(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE IF EXISTS request_logs_hot
		    ADD COLUMN IF NOT EXISTS task_type TEXT,
		    ADD COLUMN IF NOT EXISTS origin_stage VARCHAR(32);

		-- (1) Backfill broken_confirmed → binding available=FALSE.
		UPDATE credential_model_bindings cmb
		SET available          = FALSE,
		    unavailable_reason = 'model_probe_broken',
		    unavailable_at     = NOW()
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND EXISTS (
		      SELECT 1 FROM model_probe_state mps
		      WHERE mps.credential_id = cmb.credential_id
		        AND mps.raw_model_name = pm.raw_model_name
		        AND mps.state = 'broken_confirmed'
		  );

		-- (2) recent_success_rate helper. DROP+CREATE keeps the body in sync
		--     with the live hot-table source even if a prior deploy left an
		--     older body. request_logs only receives promoted rows, so using it
		--     for the default 3-hour window yields samples=0 by design.
		--     2026-06-23: Add p_window_hours parameter for time-based windowing.
		DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int);
		DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int, int);
		CREATE FUNCTION recent_success_rate(p_credential_id BIGINT,
		                                    p_raw_model     TEXT,
		                                    p_sample_n      INT DEFAULT 50,
		                                    p_window_hours  INT DEFAULT 3)
		RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
		LANGUAGE sql
		STABLE
		AS $$
			    WITH recent AS (
			        SELECT success
				    FROM request_logs_hot
				    WHERE credential_id = p_credential_id
				      AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
				      AND ts > NOW() - (p_window_hours || ' hours')::interval
				      -- Probe/self-check rows measure the health worker, not the
				      -- business route. Legacy probe IDs are retained for old rows
				      -- created before origin_stage/task_type was added.
				      AND COALESCE(task_type, '') <> 'probe_triggered'
				      AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
				      AND request_id NOT LIKE 'probe-%'
				    ORDER BY ts DESC
				    LIMIT p_sample_n
			    )

		    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
		           COUNT(*)::int
		    FROM recent;
		$$;
	`)
	if err != nil {
		return err
	}
	slog.Info("routing recent success-rate schema ensured (recent_success_rate fn + broken_confirmed backfill)")
	return nil
}

// ensureUnavailableRecoverAtSchema mirrors db/migrations/292_unavailable_recover_at.sql.
func (d *DB) ensureUnavailableRecoverAtSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE credential_model_bindings ADD COLUMN IF NOT EXISTS unavailable_recover_at TIMESTAMPTZ;
		UPDATE credential_model_bindings SET unavailable_recover_at = unavailable_at + (
		    CASE unavailable_reason
		        WHEN 'auto_concurrent' THEN INTERVAL '5 minutes'
		        WHEN 'auto_rate_limit' THEN INTERVAL '15 minutes'
		        WHEN 'auto_network' THEN INTERVAL '2 minutes'
		        WHEN 'auto_timeout' THEN INTERVAL '30 seconds'
		        WHEN 'auto_stream_timeout' THEN INTERVAL '30 seconds'
		        WHEN 'auto_upstream_down' THEN INTERVAL '1 minute'
		        WHEN 'continuous_failure' THEN INTERVAL '15 minutes'
		        ELSE INTERVAL '30 seconds'
		    END)
		WHERE available = FALSE AND unavailable_recover_at IS NULL AND unavailable_at IS NOT NULL
		  AND COALESCE(admin_protected, FALSE) = FALSE
		  AND (unavailable_reason LIKE 'auto\_%' OR unavailable_reason = 'continuous_failure');

		CREATE INDEX IF NOT EXISTS idx_cmb_unavailable_recover_at
		    ON credential_model_bindings (unavailable_recover_at) WHERE available = FALSE;
	`)
	if err != nil {
		return err
	}
	slog.Info("unavailable_recover_at schema ensured")
	return nil
}

// ensureProbeHealthDashboardViews mirrors db/migrations/314_probe_health_comprehensive_fix.sql.
//
// Creates (or replaces) the five PostgreSQL views + one helper function that
// the /probe-health admin page reads:
//
//	v_model_health_dashboard      → GET /api/admin/probe/dashboard
//	v_probe_queue_snapshot        → GET /api/admin/probe/queue-snapshot
//	v_model_priority_details      → GET /api/admin/probe/model/{model}/nodes
//	v_probe_system_health         → GET /api/admin/probe/system-health
//	v_model_availability_timeline → GET /api/admin/probe/timeline
//	get_model_state_summary(TEXT) → GET /api/admin/probe/model/{model}/summary
//
// Dashboard views are derived data — they are NOT on the request critical path.
// If creation fails (e.g. a column added by a migration that hasn't been applied
// yet), the gateway must still start. Therefore this function logs a warning on
// error and returns nil, never blocking db.Open.
//
// 2026-06-30 PR-8: wraps the DROP+CREATE in a transaction guarded by
// pg_try_advisory_xact_lock. Without this, two pods booting concurrently
// race on DROP VIEW CASCADE: the loser sees "view does not exist" / rows
// flipping schema mid-flight, leaving /probe-health returning 500 for
// ~30s (audit P0-11). The non-blocking variant is deliberate — pods
// that lose the race skip the rebuild; the winner's commit is visible
// immediately. Lock ID is a fixed int64 chosen to not collide with
// other advisory locks in this codebase.
const probeViewAdvisoryLockID int64 = 0x50524F42 // "PROB" in ASCII

func (d *DB) ensureProbeHealthDashboardViews(ctx context.Context) {
	if d == nil || d.pool == nil {
		return
	}

	// 2026-06-30 PR-8: wrap DROP+CREATE in a transaction so the advisory
	// lock is auto-released at commit/rollback (no unlock path to forget).
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		slog.Warn("probe health dashboard views: begin tx failed (non-fatal)",
			"error", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // tx Commit supersedes; Rollback on commit is a no-op

	// Try to acquire the advisory lock. If another pod already holds it,
	// skip the rebuild — that pod's commit will publish the views.
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, probeViewAdvisoryLockID).Scan(&locked); err != nil {
		slog.Warn("probe health dashboard views: pg_try_advisory_xact_lock failed (non-fatal)",
			"error", err)
		return
	}
	if !locked {
		slog.Info("probe health dashboard views: another pod holds the advisory lock, skipping rebuild")
		return
	}

	_, err = tx.Exec(ctx, probeHealthDashboardViewsSQL())
	if err != nil {
		// Non-fatal: the gateway must still serve traffic even if the
		// admin dashboard views are unavailable. The probe-health page
		// will show empty data, but routing is unaffected.
		slog.Warn("probe health dashboard views creation failed (non-fatal; /probe-health page may be empty)",
			"error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("probe health dashboard views: tx commit failed (non-fatal)",
			"error", err)
		return
	}
	slog.Info("probe health dashboard views ensured (v_node_probe_state_compat, v_model_health_dashboard, v_probe_queue_snapshot, v_model_priority_details, v_probe_system_health, v_model_availability_timeline, get_model_state_summary)")
}

// ensureProductModulesSchema mirrors sql/migrations/startup/371_product_modules.sql
// for startup apply. Idempotent. Creates product modules, subscription tiers,
// and tier-module mapping tables with seed data.
func (d *DB) ensureProductModulesSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS product_modules (
			id              SERIAL PRIMARY KEY,
			key             TEXT NOT NULL UNIQUE,
			name            TEXT NOT NULL,
			description     TEXT NOT NULL DEFAULT '',
			category        TEXT NOT NULL,
			icon            TEXT,
			setting_key     TEXT,
			is_base         BOOLEAN NOT NULL DEFAULT FALSE,
			sort_order      INT NOT NULL DEFAULT 0,
			enabled         BOOLEAN NOT NULL DEFAULT TRUE,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_pm_category ON product_modules (category);
		CREATE INDEX IF NOT EXISTS idx_pm_setting ON product_modules (setting_key);

		CREATE TABLE IF NOT EXISTS product_module_features (
			id              SERIAL PRIMARY KEY,
			module_key      TEXT NOT NULL REFERENCES product_modules(key) ON DELETE CASCADE,
			feature_key     TEXT NOT NULL,
			feature_name    TEXT NOT NULL,
			description     TEXT NOT NULL DEFAULT '',
			setting_key     TEXT,
			enabled         BOOLEAN NOT NULL DEFAULT TRUE,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (module_key, feature_key)
		);
		CREATE INDEX IF NOT EXISTS idx_pmf_module ON product_module_features (module_key);

		CREATE TABLE IF NOT EXISTS subscription_tiers (
			id              SERIAL PRIMARY KEY,
			code            TEXT NOT NULL UNIQUE,
			name            TEXT NOT NULL,
			description     TEXT NOT NULL DEFAULT '',
			price_cents     INT NOT NULL DEFAULT 0,
			sort_order      INT NOT NULL DEFAULT 0,
			enabled         BOOLEAN NOT NULL DEFAULT TRUE,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_st_code ON subscription_tiers (code);

		CREATE TABLE IF NOT EXISTS tier_module_map (
			tier_code       TEXT NOT NULL REFERENCES subscription_tiers(code) ON DELETE CASCADE,
			module_key      TEXT NOT NULL REFERENCES product_modules(key) ON DELETE CASCADE,
			max_features    TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tier_code, module_key)
		);
	`)
	if err != nil {
		return err
	}
	slog.Info("product_modules schema ensured (4 tables)")
	return nil
}

// ensureLicenseModulesSchema mirrors sql/migrations/startup/372_license_modules.sql
// for startup apply. Idempotent. Creates license_modules and license_module_audit tables.
func (d *DB) ensureLicenseModulesSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS licenses (
			id               BIGSERIAL PRIMARY KEY,
			license_key      TEXT NOT NULL UNIQUE,
			customer_name    TEXT NOT NULL DEFAULT '',
			customer_email   TEXT NOT NULL DEFAULT '',
			max_devices      INT NOT NULL DEFAULT 2,
			subscription_tier TEXT NOT NULL DEFAULT 'starter',
			features         JSONB NOT NULL DEFAULT '[]'::jsonb,
			expires_at       TIMESTAMPTZ,
			revoked_at       TIMESTAMPTZ,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		-- idx_licenses_key removed in R38/719: shadowed by license_key UNIQUE.
		CREATE INDEX IF NOT EXISTS idx_licenses_expires ON licenses (expires_at) WHERE expires_at IS NOT NULL;

		CREATE TABLE IF NOT EXISTS license_modules (
			id              BIGSERIAL PRIMARY KEY,
			license_id      BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
			module_key      TEXT NOT NULL REFERENCES product_modules(key),
			enabled         BOOLEAN NOT NULL DEFAULT TRUE,
			config          JSONB,
			expires_at      TIMESTAMPTZ,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (license_id, module_key)
		);
		CREATE INDEX IF NOT EXISTS idx_lm_license ON license_modules (license_id);
		CREATE INDEX IF NOT EXISTS idx_lm_module ON license_modules (module_key);

		CREATE TABLE IF NOT EXISTS license_module_audit (
			id              BIGSERIAL PRIMARY KEY,
			license_key     TEXT NOT NULL,
			module_key      TEXT NOT NULL,
			action          TEXT NOT NULL,
			old_value       JSONB,
			new_value       JSONB,
			actor           TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_lma_key ON license_module_audit (license_key, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_lma_module ON license_module_audit (module_key, created_at DESC);

		CREATE TABLE IF NOT EXISTS license_trial_consents (
			id                BIGSERIAL PRIMARY KEY,
			license_id        BIGINT NOT NULL UNIQUE REFERENCES licenses(id) ON DELETE CASCADE,
			agreement_version TEXT NOT NULL,
			accepted_at       TIMESTAMPTZ NOT NULL,
			source            TEXT NOT NULL,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_license_trial_consents_accepted_at
			ON license_trial_consents (accepted_at DESC);

		CREATE TABLE IF NOT EXISTS runtime_telemetry_preferences (
			hardware_hash     TEXT PRIMARY KEY,
			license_id        BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
			enabled           BOOLEAN NOT NULL DEFAULT FALSE,
			agreement_version TEXT NOT NULL,
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
			disabled_at       TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_runtime_telemetry_preferences_license
			ON runtime_telemetry_preferences (license_id);

		CREATE TABLE IF NOT EXISTS runtime_telemetry_consent_events (
			id                BIGSERIAL PRIMARY KEY,
			hardware_hash     TEXT NOT NULL,
			license_id        BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
			enabled           BOOLEAN NOT NULL,
			agreement_version TEXT NOT NULL,
			operator_user_id  BIGINT NOT NULL,
			source            TEXT NOT NULL,
			occurred_at       TIMESTAMPTZ NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_runtime_telemetry_consent_events_hardware_time
			ON runtime_telemetry_consent_events (hardware_hash, occurred_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("license_modules schema ensured (2 tables)")
	return nil
}

// ensureVibeCodingSchema mirrors sql/migrations/startup/373_vibecoding.sql
// for startup apply. Idempotent. Creates VibeCoding projects, sessions,
// and code reviews tables with RLS policies.
func (d *DB) ensureVibeCodingSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS vibe_coding_projects (
			id              BIGSERIAL PRIMARY KEY,
			tenant_id       TEXT NOT NULL DEFAULT 'default',
			name            TEXT NOT NULL,
			description     TEXT,
			language        TEXT,
			framework       TEXT,
			status          TEXT NOT NULL DEFAULT 'active'
				CHECK (status IN ('active', 'archived', 'deleted')),
			settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_by      TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS vcp_tenant ON vibe_coding_projects (tenant_id);
		CREATE INDEX IF NOT EXISTS vcp_status ON vibe_coding_projects (status);
		ALTER TABLE vibe_coding_projects ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS tenant_isolation_vcp ON public.vibe_coding_projects;
		CREATE POLICY tenant_isolation_vcp ON public.vibe_coding_projects
			USING ((tenant_id)::text = (public.get_current_tenant())::text);

		CREATE TABLE IF NOT EXISTS vibe_coding_sessions (
			id              BIGSERIAL PRIMARY KEY,
			project_id      BIGINT REFERENCES vibe_coding_projects(id) ON DELETE SET NULL,
			tenant_id       TEXT NOT NULL DEFAULT 'default',
			session_id      TEXT NOT NULL,
			task_type       TEXT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'active'
				CHECK (status IN ('active', 'completed', 'failed', 'cancelled')),
			messages        JSONB NOT NULL DEFAULT '[]'::jsonb,
			metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at    TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS vcs_project ON vibe_coding_sessions (project_id);
		CREATE INDEX IF NOT EXISTS vcs_session ON vibe_coding_sessions (session_id);
		CREATE INDEX IF NOT EXISTS vcs_tenant ON vibe_coding_sessions (tenant_id, created_at DESC);
		ALTER TABLE vibe_coding_sessions ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS tenant_isolation_vcs ON public.vibe_coding_sessions;
		CREATE POLICY tenant_isolation_vcs ON public.vibe_coding_sessions
			USING ((tenant_id)::text = (public.get_current_tenant())::text);

		CREATE TABLE IF NOT EXISTS vibe_code_reviews (
			id              BIGSERIAL PRIMARY KEY,
			session_id      BIGINT REFERENCES vibe_coding_sessions(id) ON DELETE SET NULL,
			tenant_id       TEXT NOT NULL DEFAULT 'default',
			file_path       TEXT,
			language        TEXT,
			original_code   TEXT,
			review_result   JSONB,
			score           NUMERIC(3,2),
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS vcr_session ON vibe_code_reviews (session_id);
		CREATE INDEX IF NOT EXISTS vcr_tenant ON vibe_code_reviews (tenant_id, created_at DESC);
		ALTER TABLE vibe_code_reviews ENABLE ROW LEVEL SECURITY;
		DROP POLICY IF EXISTS tenant_isolation_vcr ON public.vibe_code_reviews;
		CREATE POLICY tenant_isolation_vcr ON public.vibe_code_reviews
			USING ((tenant_id)::text = (public.get_current_tenant())::text);
	`)
	if err != nil {
		return err
	}
	slog.Info("vibe_coding schema ensured (3 tables + RLS)")
	return nil
}

// ensureLicenseDevicesSchema mirrors sql/migrations/startup/374_license_devices.sql
// for startup apply. Idempotent. Creates license_devices and offline_activation_requests tables.
func (d *DB) ensureLicenseDevicesSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS license_devices (
			id                  BIGSERIAL PRIMARY KEY,
			license_id          BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
			instance_id         TEXT NOT NULL,
			hardware_hash       TEXT NOT NULL,
			device_name         TEXT NOT NULL,
			activated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_heartbeat      TIMESTAMPTZ,
			status              TEXT NOT NULL DEFAULT 'active'
				CHECK (status IN ('active', 'deactivated')),
			deactivated_at      TIMESTAMPTZ,
			deactivate_reason   TEXT,
			UNIQUE (license_id, hardware_hash)
		);
		CREATE INDEX IF NOT EXISTS idx_ld_license ON license_devices (license_id);
		CREATE INDEX IF NOT EXISTS idx_ld_status ON license_devices (status);
		CREATE INDEX IF NOT EXISTS idx_ld_hardware ON license_devices (hardware_hash);

		CREATE TABLE IF NOT EXISTS offline_activation_requests (
			id                  BIGSERIAL PRIMARY KEY,
			license_key         TEXT NOT NULL,
			hardware_hash       TEXT NOT NULL,
			instance_id         TEXT NOT NULL,
			device_name         TEXT NOT NULL,
			request_id          TEXT NOT NULL UNIQUE,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			approved_at         TIMESTAMPTZ,
			signed_license      JSONB
		);
		-- idx_oar_request removed in R38/719: shadowed by request_id UNIQUE.
		CREATE INDEX IF NOT EXISTS idx_oar_license ON offline_activation_requests (license_key);
		CREATE INDEX IF NOT EXISTS idx_oar_created ON offline_activation_requests (created_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("license_devices schema ensured (2 tables)")
	return nil
}

// ensureFaultManagementSchema mirrors sql/migrations/startup/375_fault_management.sql
// for startup apply. Idempotent. Creates fault management tables.
func (d *DB) ensureFaultManagementSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS fault_events (
			id              BIGSERIAL PRIMARY KEY,
			rule_id         BIGINT NOT NULL,
			rule_name       TEXT NOT NULL,
			severity        TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
			title           TEXT NOT NULL,
			description     TEXT NOT NULL,
			source          TEXT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'new'
				CHECK (status IN ('new', 'acknowledged', 'resolving', 'resolved', 'ignored')),
			metadata        JSONB,
			detected_at     TIMESTAMPTZ NOT NULL,
			acked_at        TIMESTAMPTZ,
			acked_by        TEXT,
			resolved_at     TIMESTAMPTZ,
			resolved_by     TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_fe_rule ON fault_events (rule_id);
		CREATE INDEX IF NOT EXISTS idx_fe_status ON fault_events (status);
		CREATE INDEX IF NOT EXISTS idx_fe_severity ON fault_events (severity);
		CREATE INDEX IF NOT EXISTS idx_fe_detected ON fault_events (detected_at DESC);

		CREATE TABLE IF NOT EXISTS fault_rules (
			id              SERIAL PRIMARY KEY,
			name            TEXT NOT NULL UNIQUE,
			description     TEXT NOT NULL,
			metric          TEXT NOT NULL,
			operator        TEXT NOT NULL CHECK (operator IN ('gte', 'lte', 'eq', 'ne')),
			threshold       DOUBLE PRECISION NOT NULL,
			duration        TEXT NOT NULL,
			severity        TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
			action          TEXT NOT NULL,
			action_config   JSONB,
			enabled         BOOLEAN NOT NULL DEFAULT TRUE,
			cooldown        TEXT NOT NULL DEFAULT '5m',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_fr_enabled ON fault_rules (enabled);
		CREATE INDEX IF NOT EXISTS idx_fr_metric ON fault_rules (metric);

		CREATE TABLE IF NOT EXISTS fault_action_logs (
			id              BIGSERIAL PRIMARY KEY,
			event_id        BIGINT NOT NULL REFERENCES fault_events(id) ON DELETE CASCADE,
			action          TEXT NOT NULL,
			status          TEXT NOT NULL,
			result          TEXT,
			duration_ms     BIGINT NOT NULL DEFAULT 0,
			triggered_at    TIMESTAMPTZ NOT NULL,
			completed_at    TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_fal_event ON fault_action_logs (event_id);
		CREATE INDEX IF NOT EXISTS idx_fal_triggered ON fault_action_logs (triggered_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("fault_management schema ensured (3 tables)")
	return nil
}

// ensureAutoUpdateSchema mirrors sql/migrations/startup/376_autoupdate.sql
// for startup apply. Idempotent. Creates autoupdate tables.
func (d *DB) ensureAutoUpdateSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS releases (
			id              BIGSERIAL PRIMARY KEY,
			version         TEXT NOT NULL UNIQUE,
			build_seq       INT NOT NULL,
			channel         TEXT NOT NULL DEFAULT 'stable'
				CHECK (channel IN ('stable', 'beta', 'canary')),
			title           TEXT NOT NULL,
			description     TEXT,
			changelog       TEXT,
			image_tag       TEXT NOT NULL,
			image_digest    TEXT,
			min_version     TEXT,
			mandatory       BOOLEAN NOT NULL DEFAULT FALSE,
			created_by      TEXT NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			published_at    TIMESTAMPTZ
		);
		-- idx_releases_version removed in R38/719: shadowed by version UNIQUE.
		CREATE INDEX IF NOT EXISTS idx_releases_channel ON releases (channel, build_seq DESC);
		CREATE INDEX IF NOT EXISTS idx_releases_published ON releases (published_at DESC)
			WHERE published_at IS NOT NULL;

		CREATE TABLE IF NOT EXISTS gray_release_rules (
			id              BIGSERIAL PRIMARY KEY,
			release_id      BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
			phase           TEXT NOT NULL
				CHECK (phase IN ('canary', 'batch_1', 'batch_2', 'batch_3', 'full')),
			percent         INT NOT NULL CHECK (percent >= 0 AND percent <= 100),
			selectors       JSONB,
			status          TEXT NOT NULL DEFAULT 'active'
				CHECK (status IN ('active', 'paused', 'completed')),
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_gray_rules_release ON gray_release_rules (release_id);
		CREATE INDEX IF NOT EXISTS idx_gray_rules_status ON gray_release_rules (status, created_at DESC);

		CREATE TABLE IF NOT EXISTS upgrade_logs (
			id              BIGSERIAL PRIMARY KEY,
			instance_id     TEXT NOT NULL,
			old_version     TEXT NOT NULL,
			new_version     TEXT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'downloading', 'ready_to_restart', 'upgrading', 'success', 'failed', 'rolled_back')),
			started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at    TIMESTAMPTZ,
			error_message   TEXT,
			retry_count     INT NOT NULL DEFAULT 0,
			duration_ms     INT
		);
		CREATE INDEX IF NOT EXISTS idx_upgrade_logs_instance ON upgrade_logs (instance_id, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_upgrade_logs_status ON upgrade_logs (status, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_upgrade_logs_failed ON upgrade_logs (started_at DESC)
			WHERE status = 'failed';

		CREATE TABLE IF NOT EXISTS instance_release_status (
			release_id      BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
			instance_id     TEXT NOT NULL PRIMARY KEY,
			status          TEXT NOT NULL,
			version         TEXT NOT NULL,
			started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at    TIMESTAMPTZ,
			error           TEXT,
			retry_count     INT NOT NULL DEFAULT 0,
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_instance_status_release ON instance_release_status (release_id);
		CREATE INDEX IF NOT EXISTS idx_instance_status_status ON instance_release_status (status);
	`)
	if err != nil {
		return err
	}
	slog.Info("autoupdate schema ensured (4 tables)")
	return nil
}

// ensureCenterOpsSchema mirrors sql/migrations/startup/377_center_ops.sql
// for startup apply. Idempotent. Creates center ops tables.
func (d *DB) ensureCenterOpsSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS gateway_instances (
			instance_id     TEXT PRIMARY KEY,
			hostname        TEXT NOT NULL,
			ip_address      TEXT NOT NULL,
			region          TEXT,
			version         TEXT NOT NULL,
			build_seq       INT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'online'
				CHECK (status IN ('online', 'offline', 'degraded')),
			started_at      TIMESTAMPTZ NOT NULL,
			last_heartbeat  TIMESTAMPTZ NOT NULL DEFAULT now(),
			registered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
		);
		CREATE INDEX IF NOT EXISTS idx_gi_status ON gateway_instances (status);
		CREATE INDEX IF NOT EXISTS idx_gi_region ON gateway_instances (region);
		CREATE INDEX IF NOT EXISTS idx_gi_heartbeat ON gateway_instances (last_heartbeat DESC);
		CREATE INDEX IF NOT EXISTS idx_gi_version ON gateway_instances (version);

		CREATE TABLE IF NOT EXISTS instance_heartbeats (
			instance_id     TEXT NOT NULL,
			timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
			uptime_secs     BIGINT NOT NULL,
			num_goroutine   INT NOT NULL,
			alloc_mb        DOUBLE PRECISION NOT NULL,
			status          TEXT NOT NULL,
			PRIMARY KEY (instance_id, timestamp)
		);
		CREATE INDEX IF NOT EXISTS idx_ih_instance ON instance_heartbeats (instance_id, timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_ih_timestamp ON instance_heartbeats (timestamp DESC);

		CREATE TABLE IF NOT EXISTS center_commands (
			id              BIGSERIAL PRIMARY KEY,
			command_id      TEXT NOT NULL UNIQUE,
			instance_id     TEXT NOT NULL,
			command         TEXT NOT NULL,
			args            JSONB,
			status          TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'executed', 'failed', 'expired')),
			issued_at       TIMESTAMPTZ NOT NULL,
			issued_by       TEXT NOT NULL,
			expires_at      TIMESTAMPTZ,
			executed_at     TIMESTAMPTZ,
			result          JSONB
		);
		CREATE INDEX IF NOT EXISTS idx_cc_instance ON center_commands (instance_id, issued_at DESC);
		CREATE INDEX IF NOT EXISTS idx_cc_status ON center_commands (status, issued_at DESC);
		-- idx_cc_command_id removed in R38/719: shadowed by command_id UNIQUE.

		CREATE TABLE IF NOT EXISTS instance_status_reports (
			instance_id     TEXT NOT NULL,
			timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
			state           TEXT NOT NULL,
			active_licenses INT NOT NULL DEFAULT 0,
			active_devices  INT NOT NULL DEFAULT 0,
			requests_total  BIGINT NOT NULL DEFAULT 0,
			requests_ok     BIGINT NOT NULL DEFAULT 0,
			requests_err    BIGINT NOT NULL DEFAULT 0,
			avg_latency_ms  DOUBLE PRECISION NOT NULL DEFAULT 0,
			p99_latency_ms  DOUBLE PRECISION NOT NULL DEFAULT 0,
			PRIMARY KEY (instance_id, timestamp)
		);
		-- idx_isr_instance removed in R38/719: shadowed by PRIMARY KEY
		-- (instance_id, timestamp) — same columns, DESC served by backward scan.
		CREATE INDEX IF NOT EXISTS idx_isr_timestamp ON instance_status_reports (timestamp DESC);

		ALTER TABLE gateway_instances
			ADD COLUMN IF NOT EXISTS instance_token    TEXT,
			ADD COLUMN IF NOT EXISTS refresh_token     TEXT,
			ADD COLUMN IF NOT EXISTS refresh_token_issued_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS refresh_token_expires_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS public_key        TEXT,
			ADD COLUMN IF NOT EXISTS current_version   TEXT,
			ADD COLUMN IF NOT EXISTS license_key_hash  TEXT,
			ADD COLUMN IF NOT EXISTS hardware_hash     TEXT,
			ADD COLUMN IF NOT EXISTS instance_type     TEXT DEFAULT 'standalone',
			ADD COLUMN IF NOT EXISTS deployment_id     TEXT,
			ADD COLUMN IF NOT EXISTS replica_count     INT DEFAULT 1;
		CREATE INDEX IF NOT EXISTS idx_gi_license ON gateway_instances (license_key_hash);
		CREATE INDEX IF NOT EXISTS idx_gi_deployment ON gateway_instances (deployment_id);
		CREATE INDEX IF NOT EXISTS idx_gi_refresh_token ON gateway_instances (refresh_token);

		ALTER TABLE instance_heartbeats
			ADD COLUMN IF NOT EXISTS metrics JSONB;
	`)
	if err != nil {
		return err
	}
	slog.Info("center_ops schema ensured (4 tables)")
	return nil
}

// ensureRuntimeMetricsSchema mirrors sql/migrations/startup/402_runtime_metrics.sql
// and 403_runtime_alert_events.sql for startup apply.
func (d *DB) ensureRuntimeMetricsSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS runtime_metrics (
			id                  BIGSERIAL PRIMARY KEY,
			instance_id         TEXT NOT NULL,
			license_id          BIGINT REFERENCES licenses(id) ON DELETE SET NULL,
			timestamp           TIMESTAMPTZ NOT NULL DEFAULT now(),
			cpu_usage_pct       REAL,
			mem_used_mb         BIGINT,
			mem_total_mb        BIGINT,
			disk_used_gb        BIGINT,
			disk_total_gb       BIGINT,
			db_size_mb          BIGINT,
			uptime_secs         BIGINT,
			current_concurrency INT,
			last_5min_tps       REAL,
			last_5min_p50_ms    REAL,
			last_5min_p99_ms    REAL,
			last_5min_success_pct REAL,
			model_usage         JSONB,
			tenant_count        INT
		);
		CREATE INDEX IF NOT EXISTS idx_rt_instance_time
			ON runtime_metrics (instance_id, timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_rt_time
			ON runtime_metrics (timestamp DESC);

		CREATE TABLE IF NOT EXISTS runtime_alert_events (
			id                BIGSERIAL PRIMARY KEY,
			rule_key          TEXT NOT NULL,
			instance_id       TEXT NOT NULL,
			severity          TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
			title             TEXT NOT NULL,
			message           TEXT NOT NULL,
			status            TEXT NOT NULL DEFAULT 'triggered'
				CHECK (status IN ('triggered', 'acknowledged', 'resolved', 'suppressed')),
			metric_value      DOUBLE PRECISION,
			detected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			acked_at          TIMESTAMPTZ,
			acked_by          TEXT,
			resolved_at       TIMESTAMPTZ,
			resolved_by       TEXT,
			suppressed_until  TIMESTAMPTZ,
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_rae_instance_status
			ON runtime_alert_events (instance_id, status, detected_at DESC);
		CREATE INDEX IF NOT EXISTS idx_rae_rule_open
			ON runtime_alert_events (rule_key, instance_id)
			WHERE status IN ('triggered', 'acknowledged', 'suppressed');
	`)
	if err != nil {
		return err
	}
	slog.Info("runtime_metrics schema ensured (runtime_metrics, runtime_alert_events)")
	return nil
}

// ensureRouteIncidentSchema mirrors sql/migrations/startup/389_route_incidents.sql
// for startup apply. Idempotent. Creates the route_incidents aggregate
// and route_incident_events evidence trail (Phase 1 read-only diagnosis).
//
// See docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md.
func (d *DB) ensureRouteIncidentSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS route_incidents (
			id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id           TEXT NOT NULL,
			endpoint_protocol   TEXT NOT NULL,
			model               TEXT NOT NULL,
			provider_id         BIGINT,
			credential_id       BIGINT,
			state               TEXT NOT NULL
				CHECK (state IN ('active', 'recovering', 'recovered')),
			failure_streak      INT  NOT NULL DEFAULT 0,
			recovery_streak     INT  NOT NULL DEFAULT 0,
			first_failure_at    TIMESTAMPTZ NOT NULL,
			last_failure_at     TIMESTAMPTZ,
			last_success_at     TIMESTAMPTZ,
			recovered_at        TIMESTAMPTZ,
			total_failures      BIGINT NOT NULL DEFAULT 0,
			total_successes     BIGINT NOT NULL DEFAULT 0,
			last_error_kind     TEXT,
			last_failure_stage  TEXT,
			resolution_source   TEXT,
			resolved_by_user    TEXT,
			resolved_reason     TEXT,
			version             BIGINT NOT NULL DEFAULT 1,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incidents_active_route
			ON route_incidents (
				tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
			)
			WHERE state IN ('active', 'recovering');
		CREATE INDEX IF NOT EXISTS idx_route_incidents_state_updated
			ON route_incidents (state, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_route_incidents_tenant_state
			ON route_incidents (tenant_id, state, updated_at DESC);

		CREATE OR REPLACE FUNCTION touch_route_incidents_updated_at()
		RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			NEW.updated_at := now();
			RETURN NEW;
		END;
		$$;
		DROP TRIGGER IF EXISTS route_incidents_touch ON route_incidents;
		CREATE TRIGGER route_incidents_touch
			BEFORE UPDATE ON route_incidents
			FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at();

		CREATE TABLE IF NOT EXISTS route_incident_events (
			id                  BIGSERIAL PRIMARY KEY,
			incident_id         UUID NOT NULL REFERENCES route_incidents(id) ON DELETE CASCADE,
			event_type          TEXT NOT NULL
				CHECK (event_type IN (
					'opened', 'failure_observed', 'recovery_progress',
					'recovered', 'diagnostic_run', 'operator_action'
				)),
			request_id          TEXT,
			terminal_status     TEXT,
			failure_kind        TEXT,
			failure_stage       TEXT,
			failure_streak      INT,
			recovery_streak     INT,
			evidence            JSONB NOT NULL DEFAULT '{}'::jsonb,
			actor               TEXT,
			created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incident_events_idem
			ON route_incident_events (incident_id, request_id, terminal_status)
			WHERE request_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_route_incident_events_incident_created
			ON route_incident_events (incident_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_route_incident_events_type_created
			ON route_incident_events (event_type, created_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("route_incident schema ensured (route_incidents + route_incident_events)")
	return nil
}

// ensureRouteIncidentPendingState mirrors
// sql/migrations/startup/715_route_incidents_pending_state.sql. 2026-09-16
// (bba08b922 事件修复的另一半)：DecideState 新增 StatePending 后，
// route_incidents 的 CHECK 约束与部分唯一索引必须先放行 'pending'，
// 否则 streak<threshold 的首条写入即 23514（observer 重试耗尽后打点放弃，
// 事件追踪整体静默失效）；全新安装路径由 ensureRouteIncidentSchema 建出
// 旧约束，也依赖本函数就地升级。
//
// 幂等：DROP CONSTRAINT/INDEX IF EXISTS + 无条件重建（表为聚合小表，
// AccessExclusive 锁窗口毫秒级，与 389 每次启动重建 trigger 同级），
// 末尾按 701/704 定式补记 schema_migrations 账本 stamp。
func (d *DB) ensureRouteIncidentPendingState(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		ALTER TABLE route_incidents
		    DROP CONSTRAINT IF EXISTS route_incidents_state_check;

		ALTER TABLE route_incidents
		    ADD CONSTRAINT route_incidents_state_check
		    CHECK (state IN ('pending', 'active', 'recovering', 'recovered'));

		DROP INDEX IF EXISTS uq_route_incidents_active_route;

		CREATE UNIQUE INDEX uq_route_incidents_active_route
		    ON route_incidents (
		        tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
		    )
		    WHERE state IN ('pending', 'active', 'recovering');

		INSERT INTO public.schema_migrations (version, description)
		VALUES ('715', 'route_incidents pending state (threshold-gated incident visibility)')
		ON CONFLICT (version) DO NOTHING;
	`)
	if err != nil {
		return err
	}
	slog.Info("route_incident pending state ensured (migration 715)")
	return nil
}

// ensureRouteIncidentPhase2Schema mirrors
// sql/migrations/startup/390_routing_audit_log.sql for startup
// apply. Idempotent. Creates the append-only audit trail and the
// diagnostic_runs table used by Phase-2 mutating actions and
// evidence export.
func (d *DB) ensureRouteIncidentPhase2Schema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	// 2026-07-14 fix: routing_audit_log may already exist from an older
	// schema (8 columns, no idempotency_key). CREATE TABLE IF NOT EXISTS
	// silently skips, but then the index creation on created_at fails.
	// Use ALTER TABLE ADD COLUMN IF NOT EXISTS to backfill missing columns
	// idempotently, then create indexes that reference those columns.
	_, err := d.pool.Exec(ctx, `
		-- Backfill routing_audit_log columns if the table pre-dates Phase 2.
		ALTER TABLE routing_audit_log
		    ADD COLUMN IF NOT EXISTS incident_id UUID,
		    ADD COLUMN IF NOT EXISTS tenant_id TEXT,
		    ADD COLUMN IF NOT EXISTS confirmation_token_hash TEXT,
		    ADD COLUMN IF NOT EXISTS idempotency_key TEXT,
		    ADD COLUMN IF NOT EXISTS request_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
		    ADD COLUMN IF NOT EXISTS pre_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
		    ADD COLUMN IF NOT EXISTS post_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
		    ADD COLUMN IF NOT EXISTS response_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
		    ADD COLUMN IF NOT EXISTS outcome TEXT,
		    ADD COLUMN IF NOT EXISTS failure_reason TEXT,
		    ADD COLUMN IF NOT EXISTS diagnostic_run_id UUID,
		    ADD COLUMN IF NOT EXISTS actor_ip_hash TEXT,
		    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

		-- Backfill tenant_id for legacy rows
		UPDATE routing_audit_log SET tenant_id = 'default' WHERE tenant_id IS NULL;

		-- Idempotency unique index (only if column has values)
		CREATE UNIQUE INDEX IF NOT EXISTS uq_routing_audit_log_idem
			ON routing_audit_log (idempotency_key);

		CREATE INDEX IF NOT EXISTS idx_routing_audit_log_incident_created
			ON routing_audit_log (incident_id, created_at DESC)
			WHERE incident_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_routing_audit_log_tenant_created
			ON routing_audit_log (tenant_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_routing_audit_log_actor_created
			ON routing_audit_log (actor, created_at DESC);

		-- diagnostic_runs: ensure table + columns
		CREATE TABLE IF NOT EXISTS diagnostic_runs (
			id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			incident_id     UUID NOT NULL REFERENCES route_incidents(id) ON DELETE CASCADE,
			tenant_id       TEXT NOT NULL,
			kind            TEXT NOT NULL,
			state           TEXT NOT NULL DEFAULT 'pending',
			route_key       JSONB NOT NULL,
			parameters      JSONB NOT NULL DEFAULT '{}'::jsonb,
			started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at     TIMESTAMPTZ,
			result          JSONB NOT NULL DEFAULT '{}'::jsonb,
			audit_log_id    BIGINT REFERENCES routing_audit_log(id) ON DELETE SET NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		-- Backfill columns for pre-existing diagnostic_runs
		ALTER TABLE diagnostic_runs
		    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

		CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_incident
			ON diagnostic_runs (incident_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_tenant
			ON diagnostic_runs (tenant_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_state
			ON diagnostic_runs (state, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_kind_state
			ON diagnostic_runs (kind, state, started_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("route_incident phase-2 schema ensured (routing_audit_log + diagnostic_runs)")
	return nil
}

// ensureDistributionSchema mirrors sql/migrations/startup/400_distribution.sql
func (d *DB) ensureDistributionSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS license_holders (
			id              BIGSERIAL PRIMARY KEY,
			email           TEXT NOT NULL UNIQUE,
			display_name    TEXT NOT NULL DEFAULT '',
			holder_type     TEXT NOT NULL DEFAULT 'individual'
				CHECK (holder_type IN ('individual', 'organization')),
			consent_version TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_seen_at    TIMESTAMPTZ
		);

		ALTER TABLE licenses
			ADD COLUMN IF NOT EXISTS holder_id BIGINT REFERENCES license_holders(id) ON DELETE SET NULL;
		CREATE INDEX IF NOT EXISTS idx_licenses_holder ON licenses (holder_id)
			WHERE holder_id IS NOT NULL;

		CREATE TABLE IF NOT EXISTS download_events (
			id              BIGSERIAL PRIMARY KEY,
			request_id      TEXT NOT NULL UNIQUE,
			release_version TEXT NOT NULL,
			platform        TEXT NOT NULL,
			arch            TEXT NOT NULL DEFAULT '',
			edition         TEXT NOT NULL DEFAULT 'customer',
			channel         TEXT NOT NULL DEFAULT 'stable',
			holder_id       BIGINT REFERENCES license_holders(id) ON DELETE SET NULL,
			donation_id     BIGINT,
			result          TEXT NOT NULL DEFAULT 'started'
				CHECK (result IN ('started', 'completed', 'failed')),
			duration_ms     INT,
			source          TEXT NOT NULL DEFAULT 'web',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_download_events_created ON download_events (created_at DESC);

		CREATE TABLE IF NOT EXISTS donations (
			id              BIGSERIAL PRIMARY KEY,
			order_no        TEXT NOT NULL UNIQUE,
			holder_id       BIGINT REFERENCES license_holders(id) ON DELETE SET NULL,
			email           TEXT NOT NULL DEFAULT '',
			amount_cents    INT NOT NULL CHECK (amount_cents > 0),
			currency        TEXT NOT NULL DEFAULT 'CNY',
			channel         TEXT NOT NULL DEFAULT 'alipay'
				CHECK (channel IN ('alipay', 'wechat', 'manual')),
			status          TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'paid', 'cancelled', 'expired')),
			tier_label      TEXT NOT NULL DEFAULT 'supporter',
			paid_at         TIMESTAMPTZ,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_donations_status ON donations (status, created_at DESC);

		CREATE TABLE IF NOT EXISTS release_artifacts (
			id              BIGSERIAL PRIMARY KEY,
			release_version TEXT NOT NULL,
			platform        TEXT NOT NULL,
			arch            TEXT NOT NULL DEFAULT '',
			edition         TEXT NOT NULL DEFAULT 'customer',
			artifact_name   TEXT NOT NULL,
			sha256          TEXT NOT NULL DEFAULT '',
			size_bytes      BIGINT NOT NULL DEFAULT 0,
			download_path   TEXT NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (release_version, platform, arch, edition, artifact_name)
		);

		CREATE TABLE IF NOT EXISTS download_publish_runs (
			id              BIGSERIAL PRIMARY KEY,
			release_version TEXT NOT NULL,
			build_seq       INT NOT NULL DEFAULT 0,
			status          TEXT NOT NULL DEFAULT 'pending',
			artifact_count  INT NOT NULL DEFAULT 0,
			test_passed     BOOLEAN NOT NULL DEFAULT FALSE,
			log_summary     TEXT,
			created_by      TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			finished_at     TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_download_publish_runs_created
			ON download_publish_runs (created_at DESC);
	`)
	if err != nil {
		return err
	}
	slog.Info("distribution schema ensured (license_holders, download_events, donations, release_artifacts)")
	return nil
}

// ensureCredentialKeysSchema mirrors sql/migrations/076-credential-keys.sql.
// It is idempotent and startup-safe so multi-key admin/runtime paths do not
// depend on an external file runner applying root sql/migrations/*.sql.
func (d *DB) ensureCredentialKeysSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.credential_keys (
		    id                BIGSERIAL PRIMARY KEY,
		    credential_id     BIGINT NOT NULL REFERENCES public.credentials(id) ON DELETE CASCADE,
		    kid_index         INT NOT NULL,
		    label             TEXT,
		    secret_ciphertext BYTEA NOT NULL,
		    status            TEXT NOT NULL DEFAULT 'active',
		    last_used_at      TIMESTAMPTZ,
		    last_failed_at    TIMESTAMPTZ,
		    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
		    tenant_id         TEXT NOT NULL DEFAULT public.get_current_tenant(),
		    CONSTRAINT credential_keys_cred_kid_key UNIQUE (credential_id, kid_index),
		    CONSTRAINT credential_keys_status_chk CHECK (status IN ('active','invalid')),
		    CONSTRAINT credential_keys_kid_pos_chk CHECK (kid_index >= 1)
		);

		CREATE INDEX IF NOT EXISTS idx_credential_keys_credential
		    ON public.credential_keys(credential_id)
		    WHERE status = 'active';
		CREATE INDEX IF NOT EXISTS idx_credential_keys_tenant
		    ON public.credential_keys(tenant_id);

		CREATE OR REPLACE FUNCTION public.credential_keys_enforce_parent_tenant()
		RETURNS TRIGGER AS $fn$
		DECLARE
		    parent_tenant text;
		BEGIN
		    SELECT tenant_id INTO parent_tenant
		    FROM public.credentials
		    WHERE id = NEW.credential_id;

		    IF parent_tenant IS NULL OR NEW.tenant_id <> parent_tenant THEN
		        RAISE EXCEPTION 'credential_keys tenant_id must match parent credential'
		            USING ERRCODE = '23514';
		    END IF;
		    RETURN NEW;
		END;
		$fn$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS trg_credential_keys_enforce_parent_tenant ON public.credential_keys;
		CREATE TRIGGER trg_credential_keys_enforce_parent_tenant
		    BEFORE INSERT OR UPDATE OF credential_id, tenant_id ON public.credential_keys
		    FOR EACH ROW EXECUTE FUNCTION public.credential_keys_enforce_parent_tenant();

		CREATE OR REPLACE FUNCTION public.credential_keys_touch_updated_at()
		RETURNS TRIGGER AS $fn$
		BEGIN
		    NEW.updated_at = now();
		    RETURN NEW;
		END;
		$fn$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS trg_credential_keys_touch_updated_at ON public.credential_keys;
		CREATE TRIGGER trg_credential_keys_touch_updated_at
		    BEFORE UPDATE ON public.credential_keys
		    FOR EACH ROW EXECUTE FUNCTION public.credential_keys_touch_updated_at();

		ALTER TABLE public.credential_keys ENABLE ROW LEVEL SECURITY;

		DROP POLICY IF EXISTS tenant_isolation_credential_keys ON public.credential_keys;
		CREATE POLICY tenant_isolation_credential_keys ON public.credential_keys
		    USING (
		        tenant_id = public.get_current_tenant()
		        OR current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true'
		    )
		    WITH CHECK (
		        tenant_id = public.get_current_tenant()
		        OR current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true'
		    );
	`)
	if err != nil {
		return err
	}
	slog.Info("credential_keys schema ensured")
	return nil
}

// ensureWebCookieSessionsSchema mirrors sql/migrations/077-webcookie-sessions.sql.
func (d *DB) ensureWebCookieSessionsSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.webcookie_sessions (
		    id              BIGSERIAL    PRIMARY KEY,
		    provider_code   TEXT         NOT NULL,
		    account_label   TEXT         NOT NULL DEFAULT 'default',
		    cookies_json    JSONB        NOT NULL DEFAULT '{}'::jsonb,
		    session_meta    JSONB        NOT NULL DEFAULT '{}'::jsonb,
		    status          TEXT         NOT NULL DEFAULT 'active',
		    last_used_at    TIMESTAMPTZ,
		    last_refresh_at TIMESTAMPTZ,
		    expires_at      TIMESTAMPTZ,
		    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
		    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
		    tenant_id       TEXT         NOT NULL DEFAULT public.get_current_tenant(),
		    CONSTRAINT webcookie_sessions_provider_account_key UNIQUE (provider_code, account_label, tenant_id),
		    CONSTRAINT webcookie_sessions_status_chk CHECK (status IN ('active','expired','banned','refreshing'))
		);

		CREATE INDEX IF NOT EXISTS idx_webcookie_sessions_provider
		    ON public.webcookie_sessions(provider_code)
		    WHERE status = 'active';
		CREATE INDEX IF NOT EXISTS idx_webcookie_sessions_tenant
		    ON public.webcookie_sessions(tenant_id);

		CREATE OR REPLACE FUNCTION public.webcookie_sessions_touch_updated_at()
		RETURNS TRIGGER AS $fn$
		BEGIN
		    NEW.updated_at = now();
		    RETURN NEW;
		END;
		$fn$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS trg_webcookie_sessions_touch_updated_at ON public.webcookie_sessions;
		CREATE TRIGGER trg_webcookie_sessions_touch_updated_at
		    BEFORE UPDATE ON public.webcookie_sessions
		    FOR EACH ROW EXECUTE FUNCTION public.webcookie_sessions_touch_updated_at();

		ALTER TABLE public.webcookie_sessions ENABLE ROW LEVEL SECURITY;

		DROP POLICY IF EXISTS tenant_isolation_webcookie_sessions ON public.webcookie_sessions;
		CREATE POLICY tenant_isolation_webcookie_sessions ON public.webcookie_sessions
		    USING (
		        tenant_id = public.get_current_tenant()
		        OR current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true'
		    )
		    WITH CHECK (
		        tenant_id = public.get_current_tenant()
		        OR current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true'
		    );
	`)
	if err != nil {
		return err
	}
	slog.Info("webcookie_sessions schema ensured")
	return nil
}

// ensurePartitionAutovacuumSchema mirrors sql/migrations/startup/404_partition_autovacuum_analyze.sql.
// Applies aggressive autovacuum reloptions on hot/partition tables; ANALYZE runs via partition_manager.
func (d *DB) ensurePartitionAutovacuumSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION apply_llm_gateway_autovacuum_settings()
		RETURNS integer LANGUAGE plpgsql AS $fn$
		DECLARE
		    opts_sql constant text := '
		        autovacuum_enabled=true,
		        autovacuum_vacuum_scale_factor=0.05,
		        autovacuum_vacuum_threshold=10,
		        autovacuum_analyze_scale_factor=0.02,
		        autovacuum_analyze_threshold=50';
		    r record;
		    applied integer := 0;
		BEGIN
		    FOR r IN
		        SELECT c.relname FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        WHERE n.nspname = 'public' AND c.relkind = 'r'
		          AND (c.relname LIKE '%\_hot' ESCAPE '\'
		            OR c.relname = 'credential_probe_model_log')
		    LOOP
		        BEGIN
		            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
		            applied := applied + 1;
		        EXCEPTION WHEN others THEN
		            RAISE NOTICE 'skip autovacuum %: %', r.relname, SQLERRM;
		        END;
		    END LOOP;
		    FOR r IN
		        SELECT c.relname FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        JOIN pg_inherits i ON i.inhrelid = c.oid
		        JOIN pg_class p ON p.oid = i.inhparent
		        WHERE n.nspname = 'public'
		          AND p.relname = ANY (ARRAY[
		              'credential_model_index','model_probe_runs','request_logs',
		              'routing_decision_log','request_wal','usage_ledger',
		              'credit_ledger','tool_usage_stats','candidate_failure_logs',
		              'handoff_logs','request_logs_bodies'])
		    LOOP
		        BEGIN
		            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
		            applied := applied + 1;
		        EXCEPTION WHEN others THEN
		            RAISE NOTICE 'skip autovacuum partition %: %', r.relname, SQLERRM;
		        END;
		    END LOOP;
		    RETURN applied;
		END;
		$fn$;

		CREATE OR REPLACE FUNCTION analyze_llm_gateway_table_stats(p_recent_months integer DEFAULT 2)
		RETURNS integer LANGUAGE plpgsql AS $fn$
		DECLARE r record; suffix text; m integer; analyzed integer := 0;
		BEGIN
		    IF p_recent_months < 1 THEN p_recent_months := 1;
		    ELSIF p_recent_months > 12 THEN p_recent_months := 12; END IF;
		    FOR r IN
		        SELECT c.relname FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        JOIN pg_am am ON am.oid = c.relam
		        WHERE n.nspname = 'public' AND c.relkind = 'r'
		          AND c.relname LIKE '%\_hot' ESCAPE '\' AND am.amname = 'heap'
		    LOOP
		        EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		    END LOOP;
		    FOR m IN 0..(p_recent_months - 1) LOOP
		        suffix := to_char(date_trunc('month', now()) - (m || ' months')::interval, 'YYYY_MM');
		        FOR r IN
		            SELECT c.relname FROM pg_class c
		            JOIN pg_namespace n ON n.oid = c.relnamespace
		            WHERE n.nspname = 'public' AND c.relkind = 'r'
		              AND c.relname ~ ('^(credential_model_index|model_probe_runs|request_logs|routing_decision_log|request_wal|usage_ledger|credit_ledger|tool_usage_stats|candidate_failure_logs|handoff_logs|request_logs_bodies)_' || suffix || '$')
		        LOOP
		            EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		        END LOOP;
		    END LOOP;
		    RETURN analyzed;
		END;
		$fn$;

		SELECT apply_llm_gateway_autovacuum_settings();
	`)
	if err != nil {
		return err
	}
	slog.Info("partition autovacuum settings ensured (hot + partition tables)")
	return nil
}

// ensureCredentialClientQuotaSchema mirrors the per-credential, per-client
// quota contract introduced by P0-C. The shadow rollout is driven by
// settings (credential_client_quota.mode); the table only records policy
// rows and never blocks credential health writes.
//
// Parked by AUDIT_24H_20260817.md B1 (option C): the bootstrap call at
// applyMigrationsOnce is commented out until the dispatch path actually
// consumes credential_client_quota.mode, so this method is currently
// uncalled. The body is retained to make restoring one line of caller +
// re-enabling the spec registration enough to un-park; suppress the
// resulting U1000 so a future staticcheck gate stays clean in the meantime.
//
//lint:ignore U1000 retained for credentialquota un-park; see AUDIT_24H_20260817.md B1
func (d *DB) ensureCredentialClientQuotaSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.credential_client_quota (
		    credential_id    BIGINT NOT NULL REFERENCES public.credentials(id) ON DELETE CASCADE,
		    client_type      VARCHAR(64) NOT NULL,
		    owner_tenant_id  TEXT NOT NULL,
		    max_concurrent   INTEGER,
		    max_fp_slots     INTEGER,
		    fp_enforce_after TIMESTAMPTZ,
		    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_by       TEXT,
		    CONSTRAINT credential_client_quota_pkey
		        PRIMARY KEY (credential_id, client_type),
		    CONSTRAINT credential_client_quota_client_type_check
		        CHECK (client_type IN (
		            'cursor', 'claude-code', 'opencode', 'zcode', 'codex', 'roocode',
		            'vscode', 'copilot', 'windsurf', 'zed', 'jetbrains', 'unknown'
		        )),
		    CONSTRAINT credential_client_quota_max_concurrent_check
		        CHECK (max_concurrent IS NULL OR max_concurrent > 0),
		    CONSTRAINT credential_client_quota_max_fp_slots_check
		        CHECK (max_fp_slots IS NULL OR max_fp_slots > 0),
		    CONSTRAINT credential_client_quota_has_limit_check
		        CHECK (max_concurrent IS NOT NULL OR max_fp_slots IS NOT NULL)
		);
		CREATE INDEX IF NOT EXISTS idx_credential_client_quota_owner_client
		    ON public.credential_client_quota (owner_tenant_id, client_type);
		ALTER TABLE public.credential_client_quota
		    ADD COLUMN IF NOT EXISTS updated_by TEXT;
	`)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION public.credential_client_quota_set_owner_tenant()
		RETURNS TRIGGER AS $fn$
		DECLARE
		    parent_tenant TEXT;
		BEGIN
		    SELECT tenant_id INTO parent_tenant
		    FROM public.credentials
		    WHERE id = NEW.credential_id;
		    IF NOT FOUND THEN
		        RAISE EXCEPTION 'credential_client_quota credential_id % does not exist', NEW.credential_id
		            USING ERRCODE = '23503';
		    END IF;
		    NEW.owner_tenant_id := parent_tenant;
		    RETURN NEW;
		END;
		$fn$ LANGUAGE plpgsql;
		DROP TRIGGER IF EXISTS trg_credential_client_quota_set_owner_tenant
		    ON public.credential_client_quota;
		CREATE TRIGGER trg_credential_client_quota_set_owner_tenant
		    BEFORE INSERT OR UPDATE OF credential_id, owner_tenant_id
		    ON public.credential_client_quota
		    FOR EACH ROW
		    EXECUTE FUNCTION public.credential_client_quota_set_owner_tenant();

		CREATE OR REPLACE FUNCTION public.credential_client_quota_touch_updated_at()
		RETURNS TRIGGER AS $fn$
		BEGIN
		    NEW.updated_at := now();
		    RETURN NEW;
		END;
		$fn$ LANGUAGE plpgsql;
		DROP TRIGGER IF EXISTS trg_credential_client_quota_touch_updated_at
		    ON public.credential_client_quota;
		CREATE TRIGGER trg_credential_client_quota_touch_updated_at
		    BEFORE UPDATE ON public.credential_client_quota
		    FOR EACH ROW
		    EXECUTE FUNCTION public.credential_client_quota_touch_updated_at();

		ALTER TABLE public.credential_client_quota ENABLE ROW LEVEL SECURITY;
		ALTER TABLE public.credential_client_quota FORCE ROW LEVEL SECURITY;

		DROP POLICY IF EXISTS tenant_isolation_credential_client_quota
		    ON public.credential_client_quota;
		CREATE POLICY tenant_isolation_credential_client_quota
		    ON public.credential_client_quota
		    USING (
		        owner_tenant_id = public.get_current_tenant()
		        OR current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true'
		    )
		    WITH CHECK (
		        owner_tenant_id = public.get_current_tenant()
		        OR current_setting('app.current_role', true) = 'super_admin'
		        OR current_setting('app.bypass_rls', true) = 'true'
		    );
	`)
	if err != nil {
		return err
	}
	slog.Info("credential_client_quota schema ensured")
	return nil
}

// ensureUrsmKeyMigrationLedgerSchema maintains the final schema formed by
// sql/migrations/080-ursm-key-migration-ledger.sql and the additive 081
// rollback_deadline repair. Idempotent and startup-safe so multi-key
// admin/runtime paths do not depend on an external file runner applying root
// sql/migrations/*.sql. The schema records the durable migration identity
// (owner / ledger_id / mode / preflight checksum / checkpoint) and one row per
// exact source Redis key so copy and cleanup remain exact-key and
// interruptible.
func (d *DB) ensureUrsmKeyMigrationLedgerSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS ursm_key_migration_runs (
		    ledger_id          TEXT PRIMARY KEY,
		    owner              TEXT NOT NULL,
		    key_schema_mode    TEXT NOT NULL,
		    preflight_checksum TEXT NOT NULL,
		    preflight_total    INT  NOT NULL DEFAULT 0,
		    preflight_migratable INT NOT NULL DEFAULT 0,
		    preflight_canonical_present INT NOT NULL DEFAULT 0,
		    preflight_ambiguous INT NOT NULL DEFAULT 0,
		    preflight_excluded INT NOT NULL DEFAULT 0,
		    checkpoint         TEXT NOT NULL,
			cutover_epoch      BIGINT NOT NULL DEFAULT 0,
			rollback_deadline  TIMESTAMPTZ,
			started_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    finished_at        TIMESTAMPTZ,
		    CONSTRAINT ursm_key_migration_runs_checkpoint_chk CHECK (
		        checkpoint IN ('preflight','copy','coverage','dual','observe','cleanup','rollback','done')
		    ),
		    CONSTRAINT ursm_key_migration_runs_mode_chk CHECK (
		        key_schema_mode IN ('legacy','dual','canonical')
		    )
			);

			ALTER TABLE ursm_key_migration_runs
			    ADD COLUMN IF NOT EXISTS rollback_deadline TIMESTAMPTZ;

			CREATE TABLE IF NOT EXISTS ursm_key_migration_entries (
		    ledger_id        TEXT NOT NULL REFERENCES ursm_key_migration_runs(ledger_id) ON DELETE CASCADE,
		    source_key       TEXT NOT NULL,
		    target_key       TEXT NOT NULL DEFAULT '',
		    classification   TEXT NOT NULL,
		    schema_origin    TEXT NOT NULL DEFAULT '',
		    key_type         TEXT NOT NULL DEFAULT '',
		    pttl_ms          BIGINT NOT NULL DEFAULT -2,
		    generation       BIGINT NOT NULL DEFAULT 0,
		    field_checksum   TEXT NOT NULL DEFAULT '',
		    tuple_tenant     TEXT NOT NULL DEFAULT '',
		    tuple_credential BIGINT NOT NULL DEFAULT 0,
		    tuple_raw_model  TEXT NOT NULL DEFAULT '',
		    state            TEXT NOT NULL DEFAULT 'classified',
		    copied_at        TIMESTAMPTZ,
		    copied_pttl_ms   BIGINT,
		    cleaned_at       TIMESTAMPTZ,
		    last_error       TEXT NOT NULL DEFAULT '',
		    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    PRIMARY KEY (ledger_id, source_key),
		    CONSTRAINT ursm_key_migration_entries_class_chk CHECK (
		        classification IN ('migratable','canonical_present','ambiguous','excluded_non_authoritative')
		    ),
		    CONSTRAINT ursm_key_migration_entries_schema_chk CHECK (
		        schema_origin IN ('','legacy','k2')
		    ),
		    CONSTRAINT ursm_key_migration_entries_state_chk CHECK (
		        state IN ('classified','copied','cleaned','rolled_back','conflict','expired','fenced')
		    )
		);

		CREATE INDEX IF NOT EXISTS ursm_key_migration_entries_state_idx
		    ON ursm_key_migration_entries (ledger_id, state);
			CREATE INDEX IF NOT EXISTS ursm_key_migration_entries_class_idx
			    ON ursm_key_migration_entries (ledger_id, classification);
			ALTER TABLE ursm_key_migration_entries
			    ADD COLUMN IF NOT EXISTS cleanup_claim_owner TEXT NOT NULL DEFAULT '',
			    ADD COLUMN IF NOT EXISTS cleanup_claim_epoch BIGINT,
			    ADD COLUMN IF NOT EXISTS cleanup_claimed_at TIMESTAMPTZ;
			CREATE TABLE IF NOT EXISTS ursm_key_migration_transitions (
			    ledger_id       TEXT NOT NULL REFERENCES ursm_key_migration_runs(ledger_id) ON DELETE CASCADE,
			    cutover_epoch   BIGINT NOT NULL,
			    from_checkpoint TEXT NOT NULL,
			    to_checkpoint   TEXT NOT NULL,
			    key_schema_mode TEXT NOT NULL,
			    actor           TEXT NOT NULL,
			    approved_by     TEXT NOT NULL,
			    evidence_sha256 TEXT NOT NULL,
			    evidence_ref    TEXT NOT NULL,
			    reason          TEXT NOT NULL DEFAULT '',
			    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			    PRIMARY KEY (ledger_id, cutover_epoch),
			    CONSTRAINT ursm_key_migration_transitions_checkpoint_chk CHECK (
from_checkpoint IN ('preflight','copy','coverage','dual','observe','cleanup')
				        AND to_checkpoint IN ('copy','coverage','dual','observe','cleanup','done','rollback')
			    ),
			    CONSTRAINT ursm_key_migration_transitions_mode_chk CHECK (
			        key_schema_mode IN ('legacy','dual','canonical')
			    ),
			    CONSTRAINT ursm_key_migration_transitions_evidence_sha256_chk CHECK (
			        evidence_sha256 ~ '^[0-9a-f]{64}$'
			    )
			);
			CREATE INDEX IF NOT EXISTS ursm_key_migration_entries_fenced_claim_idx
			    ON ursm_key_migration_entries (ledger_id, state, cleanup_claimed_at);
			ALTER TABLE ursm_key_migration_runs
			    DROP CONSTRAINT IF EXISTS ursm_key_migration_runs_checkpoint_chk;
			ALTER TABLE ursm_key_migration_runs
			    ADD CONSTRAINT ursm_key_migration_runs_checkpoint_chk CHECK (
			        checkpoint IN ('preflight','copy','coverage','dual','observe','cleanup','rollback','done')
			    );
			`)
	if err != nil {
		return err
	}
	slog.Info("ursm_key_migration ledger schema ensured")
	return nil
}

// ensureApprovalResumeClaimSchema mirrors startup migration 553 for gateway
// databases that are upgraded through db.Open rather than the installer.
func (d *DB) ensureApprovalResumeClaimSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('public.approval_queue') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("check approval resume table: %w", err)
	}
	if !exists {
		return nil
	}

	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin approval resume schema: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE public.approval_queue
		    ADD COLUMN IF NOT EXISTS resume_state TEXT NOT NULL DEFAULT 'idle',
		    ADD COLUMN IF NOT EXISTS resume_owner TEXT,
		    ADD COLUMN IF NOT EXISTS resume_lease_until TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS resume_fencing_token BIGINT NOT NULL DEFAULT 0,
		    ADD COLUMN IF NOT EXISTS resume_started_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS resume_completed_at TIMESTAMPTZ,
		    ADD COLUMN IF NOT EXISTS resume_error TEXT;
		ALTER TABLE public.approval_queue
		    DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk;
		ALTER TABLE public.approval_queue
		    ADD CONSTRAINT approval_queue_resume_state_chk CHECK (
		        resume_state IN ('idle', 'running', 'completed', 'failed')
		    );
		ALTER TABLE public.approval_queue
		    DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk;
		ALTER TABLE public.approval_queue
		    ADD CONSTRAINT approval_queue_resume_fencing_token_chk CHECK (
		        resume_fencing_token >= 0
		    );
		CREATE INDEX IF NOT EXISTS idx_approval_queue_resume_claimable
		    ON public.approval_queue (resume_lease_until, created_at)
		    WHERE status = 'approved' AND resume_state IN ('idle', 'running', 'failed');
	`); err != nil {
		return fmt.Errorf("ensure approval resume schema: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit approval resume schema: %w", err)
	}
	return nil
}

// ensureProxyManagementCanonicalSchema mirrors startup migration 646. It is
// deliberately explicit in the production startup chain so gateway upgrades do
// not depend on an external SQL file runner. Every statement is replay-safe and
// repairs nullable columns left by interrupted legacy migration 364 installs.
func (d *DB) ensureProxyManagementCanonicalSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.proxy_subscriptions (
			id SERIAL PRIMARY KEY, name VARCHAR(100) NOT NULL, subscribe_url TEXT NOT NULL,
			status VARCHAR(20) NOT NULL DEFAULT 'active', last_fetch_at TIMESTAMP,
			last_fetch_status VARCHAR(20), last_error TEXT, node_count INTEGER NOT NULL DEFAULT 0,
			priority INTEGER NOT NULL DEFAULT 0, notes TEXT, created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS name VARCHAR(100);
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS subscribe_url TEXT;
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'active';
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS last_fetch_at TIMESTAMP;
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS last_fetch_status VARCHAR(20);
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS last_error TEXT;
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS node_count INTEGER DEFAULT 0;
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS priority INTEGER DEFAULT 0;
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS notes TEXT;
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT NOW();
		ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT NOW();
			UPDATE public.proxy_subscriptions SET name = COALESCE(NULLIF(name, ''), 'legacy-subscription-' || id::text), status = CASE WHEN subscribe_url IS NULL OR btrim(subscribe_url) = '' THEN 'disabled' ELSE COALESCE(NULLIF(status, ''), 'disabled') END, subscribe_url = COALESCE(subscribe_url, ''), node_count = COALESCE(node_count, 0), priority = COALESCE(priority, 0), created_at = COALESCE(created_at, NOW()), updated_at = COALESCE(updated_at, NOW());
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN name SET NOT NULL;
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN subscribe_url SET NOT NULL;
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN status SET NOT NULL;
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN node_count SET NOT NULL;
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN priority SET NOT NULL;
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN created_at SET NOT NULL;
		ALTER TABLE public.proxy_subscriptions ALTER COLUMN updated_at SET NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_proxy_subs_status ON public.proxy_subscriptions(status);
		CREATE INDEX IF NOT EXISTS idx_proxy_subs_priority ON public.proxy_subscriptions(priority DESC) WHERE status = 'active';

		CREATE TABLE IF NOT EXISTS public.proxy_nodes (
			id SERIAL PRIMARY KEY, subscription_id INTEGER REFERENCES public.proxy_subscriptions(id) ON DELETE CASCADE,
			name VARCHAR(200) NOT NULL, protocol VARCHAR(20) NOT NULL, server VARCHAR(255) NOT NULL,
			port INTEGER NOT NULL, username VARCHAR(100), password TEXT, config JSONB, location VARCHAR(50),
			status VARCHAR(20) NOT NULL DEFAULT 'active', health_check_url TEXT DEFAULT 'https://www.google.com/generate_204',
			last_health_check_at TIMESTAMP, last_health_check_status VARCHAR(20), response_time_ms INTEGER,
			success_rate FLOAT NOT NULL DEFAULT 1.0, consecutive_failures INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(), updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS subscription_id INTEGER;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS name VARCHAR(200);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS protocol VARCHAR(20);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS server VARCHAR(255);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS port INTEGER;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS username VARCHAR(100);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS password TEXT;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS config JSONB;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS location VARCHAR(50);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'active';
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS health_check_url TEXT DEFAULT 'https://www.google.com/generate_204';
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS last_health_check_at TIMESTAMP;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS last_health_check_status VARCHAR(20);
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS response_time_ms INTEGER;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS success_rate FLOAT DEFAULT 1.0;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS consecutive_failures INTEGER DEFAULT 0;
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT NOW();
		ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT NOW();
			DELETE FROM public.proxy_nodes n WHERE n.subscription_id IS NULL OR NOT EXISTS (SELECT 1 FROM public.proxy_subscriptions s WHERE s.id = n.subscription_id);
			UPDATE public.proxy_nodes SET name = COALESCE(NULLIF(name, ''), 'legacy-node-' || id::text), status = CASE WHEN protocol IS NULL OR btrim(protocol) = '' OR server IS NULL OR btrim(server) = '' OR port IS NULL OR port < 1 OR port > 65535 THEN 'disabled' WHEN status IN ('active', 'disabled', 'unhealthy') THEN status ELSE 'disabled' END, protocol = COALESCE(NULLIF(protocol, ''), 'http'), server = COALESCE(NULLIF(server, ''), 'invalid.local'), port = CASE WHEN port BETWEEN 1 AND 65535 THEN port ELSE 0 END, health_check_url = COALESCE(NULLIF(health_check_url, ''), 'https://www.google.com/generate_204'), success_rate = COALESCE(success_rate, 0.0), consecutive_failures = COALESCE(consecutive_failures, 0), created_at = COALESCE(created_at, NOW()), updated_at = COALESCE(updated_at, NOW()) WHERE name IS NULL OR name = '' OR protocol IS NULL OR protocol = '' OR server IS NULL OR server = '' OR port IS NULL OR port < 1 OR port > 65535 OR status IS NULL OR status NOT IN ('active', 'disabled', 'unhealthy') OR health_check_url IS NULL OR health_check_url = '' OR success_rate IS NULL OR consecutive_failures IS NULL OR created_at IS NULL OR updated_at IS NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN subscription_id SET NOT NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN name SET NOT NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN protocol SET NOT NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN server SET NOT NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN port SET NOT NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN status SET NOT NULL;
			ALTER TABLE public.proxy_nodes ALTER COLUMN health_check_url SET DEFAULT 'https://www.google.com/generate_204';
			ALTER TABLE public.proxy_nodes ALTER COLUMN health_check_url SET NOT NULL;
		ALTER TABLE public.proxy_nodes ALTER COLUMN success_rate SET NOT NULL;
		ALTER TABLE public.proxy_nodes ALTER COLUMN consecutive_failures SET NOT NULL;
		ALTER TABLE public.proxy_nodes ALTER COLUMN created_at SET NOT NULL;
		ALTER TABLE public.proxy_nodes ALTER COLUMN updated_at SET NOT NULL;
			DO $$ BEGIN
				IF EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid = 'public.proxy_nodes'::regclass AND c.conname = 'proxy_nodes_subscription_id_fkey' AND NOT (c.contype = 'f' AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'public.proxy_nodes'::regclass AND attname = 'subscription_id' AND NOT attisdropped)]::smallint[] AND c.confrelid = 'public.proxy_subscriptions'::regclass AND c.confdeltype = 'c')) THEN
					ALTER TABLE public.proxy_nodes DROP CONSTRAINT proxy_nodes_subscription_id_fkey;
				END IF;
				IF NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid = 'public.proxy_nodes'::regclass AND c.contype = 'f' AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'public.proxy_nodes'::regclass AND attname = 'subscription_id' AND NOT attisdropped)]::smallint[] AND c.confrelid = 'public.proxy_subscriptions'::regclass AND c.confdeltype = 'c') THEN
					ALTER TABLE public.proxy_nodes ADD CONSTRAINT proxy_nodes_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES public.proxy_subscriptions(id) ON DELETE CASCADE NOT VALID;
				END IF;
			END $$;
		CREATE INDEX IF NOT EXISTS idx_proxy_nodes_sub_id ON public.proxy_nodes(subscription_id);
		CREATE INDEX IF NOT EXISTS idx_proxy_nodes_status ON public.proxy_nodes(status);
		CREATE INDEX IF NOT EXISTS idx_proxy_nodes_health ON public.proxy_nodes(status, response_time_ms) WHERE status = 'active';

		CREATE TABLE IF NOT EXISTS public.provider_domains (
			id SERIAL PRIMARY KEY, domain VARCHAR(255) NOT NULL UNIQUE, catalog_code VARCHAR(100),
			requires_proxy BOOLEAN NOT NULL DEFAULT FALSE, location VARCHAR(50), probe_status VARCHAR(20),
			last_probe_at TIMESTAMP, last_probe_direct_ms INTEGER, last_probe_proxy_ms INTEGER, notes TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(), updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS domain VARCHAR(255);
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS catalog_code VARCHAR(100);
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS requires_proxy BOOLEAN DEFAULT FALSE;
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS location VARCHAR(50);
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS probe_status VARCHAR(20);
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS last_probe_at TIMESTAMP;
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS last_probe_direct_ms INTEGER;
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS last_probe_proxy_ms INTEGER;
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS notes TEXT;
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS created_at TIMESTAMP DEFAULT NOW();
		ALTER TABLE public.provider_domains ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP DEFAULT NOW();
		UPDATE public.provider_domains SET domain = COALESCE(NULLIF(domain, ''), 'legacy-domain-' || id::text), requires_proxy = COALESCE(requires_proxy, FALSE), created_at = COALESCE(created_at, NOW()), updated_at = COALESCE(updated_at, NOW());
		UPDATE public.provider_domains d SET domain = left(d.domain, 255 - length('#legacy-' || d.id::text)) || '#legacy-' || d.id::text WHERE EXISTS (SELECT 1 FROM public.provider_domains older WHERE older.domain = d.domain AND older.id < d.id);
		ALTER TABLE public.provider_domains ALTER COLUMN domain SET NOT NULL;
		ALTER TABLE public.provider_domains ALTER COLUMN requires_proxy SET NOT NULL;
		ALTER TABLE public.provider_domains ALTER COLUMN created_at SET NOT NULL;
		ALTER TABLE public.provider_domains ALTER COLUMN updated_at SET NOT NULL;
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = 'public' AND t.relname = 'provider_domains' AND c.contype IN ('p', 'u') AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'public.provider_domains'::regclass AND attname = 'domain' AND NOT attisdropped)]::smallint[]) THEN
				ALTER TABLE public.provider_domains ADD CONSTRAINT provider_domains_domain_key UNIQUE (domain);
			END IF;
		END $$;
		CREATE INDEX IF NOT EXISTS idx_provider_domains_requires_proxy ON public.provider_domains(requires_proxy);
		CREATE INDEX IF NOT EXISTS idx_provider_domains_catalog ON public.provider_domains(catalog_code);
		CREATE INDEX IF NOT EXISTS idx_provider_domains_probe ON public.provider_domains(probe_status);

		ALTER TABLE public.providers ADD COLUMN IF NOT EXISTS egress_profile TEXT DEFAULT 'direct';
		ALTER TABLE public.providers ADD COLUMN IF NOT EXISTS proxy_subscription_id INTEGER;
		UPDATE public.providers SET egress_profile = 'direct' WHERE egress_profile IS NULL OR egress_profile = '';
		ALTER TABLE public.providers ALTER COLUMN egress_profile SET DEFAULT 'direct';
		ALTER TABLE public.providers ALTER COLUMN egress_profile SET NOT NULL;
			DO $$ BEGIN
				IF EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid = 'public.providers'::regclass AND c.conname = 'providers_proxy_subscription_id_fkey' AND NOT (c.contype = 'f' AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'public.providers'::regclass AND attname = 'proxy_subscription_id' AND NOT attisdropped)]::smallint[] AND c.confrelid = 'public.proxy_subscriptions'::regclass AND c.confdeltype = 'n')) THEN
					ALTER TABLE public.providers DROP CONSTRAINT providers_proxy_subscription_id_fkey;
				END IF;
				IF NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid = 'public.providers'::regclass AND c.contype = 'f' AND c.conkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'public.providers'::regclass AND attname = 'proxy_subscription_id' AND NOT attisdropped)]::smallint[] AND c.confrelid = 'public.proxy_subscriptions'::regclass AND c.confdeltype = 'n') THEN
					ALTER TABLE public.providers ADD CONSTRAINT providers_proxy_subscription_id_fkey FOREIGN KEY (proxy_subscription_id) REFERENCES public.proxy_subscriptions(id) ON DELETE SET NULL NOT VALID;
				END IF;
			END $$;
			CREATE INDEX IF NOT EXISTS idx_providers_egress ON public.providers(egress_profile) WHERE egress_profile IS NOT NULL;
			CREATE INDEX IF NOT EXISTS idx_providers_proxy_sub ON public.providers(proxy_subscription_id) WHERE proxy_subscription_id IS NOT NULL;

			ALTER TABLE public.proxy_subscriptions ADD COLUMN IF NOT EXISTS banned_regions TEXT[] NOT NULL DEFAULT '{}';
			ALTER TABLE public.proxy_nodes ADD COLUMN IF NOT EXISTS banned_regions TEXT[] NOT NULL DEFAULT '{}';
			CREATE TABLE IF NOT EXISTS public.proxy_selection_policy (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				load_balance_strategy VARCHAR(32) NOT NULL DEFAULT 'best_only' CHECK (load_balance_strategy IN ('best_only', 'round_robin', 'weighted_rr', 'least_conn', 'consistent_hash')),
				location_affinity VARCHAR(32) NOT NULL DEFAULT 'any' CHECK (location_affinity IN ('any', 'prefer_same', 'require_same')),
				auto_disable_threshold INTEGER NOT NULL DEFAULT 3 CHECK (auto_disable_threshold > 0),
				auto_disable_enabled BOOLEAN NOT NULL DEFAULT TRUE,
				auto_recover_enabled BOOLEAN NOT NULL DEFAULT TRUE,
				swap_check_interval_ms INTEGER NOT NULL DEFAULT 30000 CHECK (swap_check_interval_ms >= 1000),
				swap_failure_threshold INTEGER NOT NULL DEFAULT 2 CHECK (swap_failure_threshold > 0),
				updated_at TIMESTAMP NOT NULL DEFAULT NOW()
			);
			INSERT INTO public.proxy_selection_policy (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
			-- Keep the canonical-schema ledger entry for databases upgraded directly
			-- by the binary, not only by the external startup migration runner.
			INSERT INTO public.schema_migrations (version, description)
			VALUES ('646', 'canonical proxy management schema') ON CONFLICT (version) DO NOTHING;
			INSERT INTO public.schema_migrations (version, description) VALUES ('691', 'proxy region avoidance and auto-switch selection policy') ON CONFLICT (version) DO NOTHING;
		`)

	if err != nil {
		return fmt.Errorf("ensure proxy management canonical schema: %w", err)
	}
	return nil
}

// autoRouteSelectionsHotEnsureSQL mirrors the hot-table half of
// sql/migrations/startup/656_auto_route_selections_hot.sql (table shape,
// defaults, CHECK constraints, indexes, and the auto_route_selections_all
// UNION ALL view). It intentionally does NOT install the
// ensure_/promote_ functions — those ship with migration 656 and with the
// installer; this ensure only guarantees the write path
// (auto_route_selections_hot + view) exists so selection batches are not
// dropped on binaries upgraded without re-running migrations.
const autoRouteSelectionsHotEnsureSQL = `
CREATE TABLE IF NOT EXISTS public.auto_route_selections_hot (
  id BIGINT NOT NULL DEFAULT nextval('public.auto_route_selections_id_seq'::regclass),
  request_id TEXT NOT NULL, session_id TEXT, task_id TEXT, tenant_id VARCHAR(64),
  ts TIMESTAMPTZ NOT NULL DEFAULT NOW(), task_type TEXT NOT NULL, profile TEXT NOT NULL DEFAULT 'smart',
  classifier TEXT NOT NULL DEFAULT 'heuristic', confidence NUMERIC(4,3), canonical_id BIGINT,
  chosen_model TEXT NOT NULL, candidate_rank SMALLINT NOT NULL DEFAULT 1,
  composite_score NUMERIC(6,2), affinity_score NUMERIC(6,2),
  affinity_applied BOOLEAN NOT NULL, explore BOOLEAN NOT NULL, fallback_used BOOLEAN NOT NULL,
  success BOOLEAN, latency_ms INTEGER, cost_usd NUMERIC(14,8), reward NUMERIC(4,3),
  reward_source TEXT, settled_at TIMESTAMPTZ, partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
  experiment_id TEXT, treatment TEXT, assignment_version TEXT, assignment_key_hash TEXT,
  PRIMARY KEY (id, partition_date)
);
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN ts SET DEFAULT NOW();
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN profile SET DEFAULT 'smart';
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN classifier SET DEFAULT 'heuristic';
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN candidate_rank SET DEFAULT 1;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN affinity_applied SET DEFAULT FALSE;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN explore SET DEFAULT FALSE;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN fallback_used SET DEFAULT FALSE;
ALTER TABLE public.auto_route_selections_hot ALTER COLUMN partition_date SET DEFAULT CURRENT_DATE;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.auto_route_selections_hot'::regclass AND conname = 'ars_hot_profile_check') THEN
    ALTER TABLE public.auto_route_selections_hot ADD CONSTRAINT ars_hot_profile_check CHECK (profile IN ('', 'smart', 'speed_first', 'cost_first'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.auto_route_selections_hot'::regclass AND conname = 'ars_hot_reward_range') THEN
    ALTER TABLE public.auto_route_selections_hot ADD CONSTRAINT ars_hot_reward_range CHECK (reward IS NULL OR (reward >= 0 AND reward <= 1));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.auto_route_selections_hot'::regclass AND conname = 'ars_hot_reward_source_check') THEN
    ALTER TABLE public.auto_route_selections_hot ADD CONSTRAINT ars_hot_reward_source_check CHECK (reward_source IS NULL OR reward_source IN ('request', 'session'));
  END IF;
END $$;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'detected_language') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN detected_language TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'prompt_length_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN prompt_length_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'context_length_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN context_length_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'turn_count_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN turn_count_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_code_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_code_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_math_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_math_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_table_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_table_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'has_multimedia_indicator') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN has_multimedia_indicator BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'intent_category') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN intent_category TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'domain_hint') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN domain_hint TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'complexity_bucket') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN complexity_bucket TEXT;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'latency_sensitive') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN latency_sensitive BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'cost_sensitive') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN cost_sensitive BOOLEAN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'feature_version') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN feature_version TEXT DEFAULT 'v1';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'auto_route_selections_hot' AND column_name = 'content_hash') THEN
    ALTER TABLE public.auto_route_selections_hot ADD COLUMN content_hash TEXT;
  END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS uq_ars_hot_request ON public.auto_route_selections_hot (request_id, partition_date);
CREATE INDEX IF NOT EXISTS idx_ars_hot_task_profile_ts ON public.auto_route_selections_hot (task_type, profile, ts DESC);
CREATE INDEX IF NOT EXISTS idx_ars_hot_session ON public.auto_route_selections_hot (session_id, ts DESC) WHERE session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ars_hot_unsettled ON public.auto_route_selections_hot (ts) WHERE settled_at IS NULL;
DROP VIEW IF EXISTS public.auto_route_selections_all;
CREATE OR REPLACE VIEW public.auto_route_selections_all AS
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash,
  detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
  has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
  intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
  feature_version, content_hash, 'hot'::text AS storage_tier
FROM public.auto_route_selections_hot
UNION ALL
SELECT id, request_id, session_id, task_id, tenant_id, ts, task_type, profile, classifier, confidence,
  canonical_id, chosen_model, candidate_rank, composite_score, affinity_score, affinity_applied,
  explore, fallback_used, success, latency_ms, cost_usd, reward, reward_source, settled_at,
  partition_date, experiment_id, treatment, assignment_version, assignment_key_hash,
  detected_language, prompt_length_bucket, context_length_bucket, turn_count_bucket,
  has_code_indicator, has_math_indicator, has_table_indicator, has_multimedia_indicator,
  intent_category, domain_hint, complexity_bucket, latency_sensitive, cost_sensitive,
  feature_version, content_hash, 'parent'::text AS storage_tier
FROM public.auto_route_selections;
`

// ensureAutoRouteSelectionsHotSchema mirrors the hot-table DDL of
// sql/migrations/startup/656_auto_route_selections_hot.sql + 658_auto_route_structured_features.sql
// (audit 2026-09-05 D-2#4 / H-2 + 2026-09-06 view schema mismatch fix). On a deployment that upgraded
// the binary without re-running the 656/658 migrations, every telemetry selection-writer batch INSERT
// failed and was dropped (dropped counter + one WARN), the settle worker failed each sweep, and the
// AUTO routing learning loop silently did nothing.
//
// 2026-09-06 fix: Migration 658 added 15 structured feature columns and updated the
// auto_route_selections_all view with DROP VIEW + CREATE VIEW (because CREATE OR REPLACE VIEW cannot
// change column positions). This ensure function must match that schema to avoid
// "ERROR: cannot drop columns from view (SQLSTATE 42P16)" when the view already has the 658 columns
// but the ensure tries to replace it with the old 656 definition.
//
// Error handling matches ensureSessionSummariesCanonical: a missing parent table (fresh empty database
// where 478/650 have not run yet) is a degraded skip with a pointer to the migration SQL, not a fatal
// — the hot DDL references auto_route_selections_id_seq (owned by the parent's BIGSERIAL) and the view
// unions the parent, so both require the parent to exist. Everything else is CREATE IF NOT EXISTS /
// DROP+CREATE idempotent and any execution error is returned to ApplyMigrations.
func (d *DB) ensureAutoRouteSelectionsHotSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	var parentExists bool
	if err := d.pool.QueryRow(ctx,
		`SELECT to_regclass('public.auto_route_selections') IS NOT NULL`,
	).Scan(&parentExists); err != nil {
		return fmt.Errorf("probe auto_route_selections existence: %w", err)
	}
	if !parentExists {
		slog.Warn("auto_route_selections parent table missing; skipping auto_route_selections_hot ensure " +
			"(run sql/migrations/startup/656_auto_route_selections_hot.sql to install)")
		return nil
	}
	// D1 残余（2026-09-23 审计轮）：ensure 体含 CREATE TABLE IF NOT EXISTS /
	// 8 条 ALTER SET DEFAULT / 约束 DO 块 / DROP VIEW+CREATE VIEW——表与视图
	// 已就位时这些仍要取锁（DROP VIEW 要视图的 ACCESS EXCLUSIVE，CREATE 表
	// 要既有表的检查锁），auto_route_selections_hot 是每请求 INSERT 的热表，
	// 晨间负载下锁等待超 30s 被 rolconfig 击杀（154 seq 2197 boot 两次尝试
	// 在此 57014，PG 日志逐字实锤）。全量清单守卫：44 列 + 4 索引 + 3 约束
	// + 视图全部在位 = 零 DDL。清单与 autoRouteSelectionsHotEnsureSQL 同步。
	const arsGuardSQL = `
		SELECT
		  (SELECT count(*) FROM unnest(ARRAY[
		     'id','request_id','session_id','task_id','tenant_id','ts','task_type',
		     'profile','classifier','confidence','canonical_id','chosen_model',
		     'candidate_rank','composite_score','affinity_score','affinity_applied',
		     'explore','fallback_used','success','latency_ms','cost_usd','reward',
		     'reward_source','settled_at','partition_date','experiment_id','treatment',
		     'assignment_version','assignment_key_hash','detected_language',
		     'prompt_length_bucket','context_length_bucket','turn_count_bucket',
		     'has_code_indicator','has_math_indicator','has_table_indicator',
		     'has_multimedia_indicator','intent_category','domain_hint',
		     'complexity_bucket','latency_sensitive','cost_sensitive',
		     'feature_version','content_hash'
		   ]) AS want(name)
		   WHERE to_regclass('public.auto_route_selections_hot') IS NULL
		      OR NOT EXISTS (SELECT 1 FROM information_schema.columns
		        WHERE table_schema='public' AND table_name='auto_route_selections_hot'
		          AND column_name = want.name))
		+
		  (SELECT count(*) FROM unnest(ARRAY[
		     'uq_ars_hot_request','idx_ars_hot_task_profile_ts',
		     'idx_ars_hot_session','idx_ars_hot_unsettled'
		   ]) AS want(name)
		   WHERE NOT EXISTS (SELECT 1 FROM pg_indexes
		     WHERE schemaname='public' AND indexname = want.name))
		+
		  (SELECT count(*) FROM unnest(ARRAY[
		     'ars_hot_profile_check','ars_hot_reward_range','ars_hot_reward_source_check'
		   ]) AS want(name)
		   WHERE NOT EXISTS (SELECT 1 FROM pg_constraint
		     WHERE conrelid = 'public.auto_route_selections_hot'::regclass
		       AND conname = want.name))
		+
		  (SELECT count(*) FROM (SELECT 1) AS one
		   WHERE NOT EXISTS (SELECT 1 FROM pg_views
		     WHERE schemaname='public' AND viewname='auto_route_selections_all'))
		`
	var arsMissing int
	if err := d.pool.QueryRow(ctx, arsGuardSQL).Scan(&arsMissing); err == nil && arsMissing == 0 {
		slog.Info("auto_route_selections_hot schema ensured (catalog short-circuit: table, indexes, constraints and view present, zero DDL)")
		return nil
	} else if err != nil {
		slog.Warn("auto_route_selections_hot ensure catalog probe failed; falling back to full ensure", "error", err)
	}
	if _, err := d.pool.Exec(ctx, autoRouteSelectionsHotEnsureSQL); err != nil {
		return fmt.Errorf("ensure auto_route_selections_hot schema: %w", err)
	}
	return nil
}
