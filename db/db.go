package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

type DB struct {
	pool *pgxpool.Pool
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
	cfg.MaxConns = 32
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
	slog.Info("postgres connected")
	db := &DB{pool: pool}
	if err := db.ApplyMigrations(ctx); err != nil {
		return nil, err
	}
	success = true // Mark success to prevent defer from closing pool
	return db, nil
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
	if err := db.ensureQualityFixModeSchema(migCtx); err != nil {
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
	if err := db.ensureRoutingRecentSuccessRate(migCtx); err != nil {
		return err
	}
	if err := db.ensureUnavailableRecoverAtSchema(migCtx); err != nil {
		return err
	}
	if err := db.ensureWorkTypeSchema(migCtx); err != nil {
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
	if err := db.ensureRouteIncidentPhase2Schema(migCtx); err != nil {
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
	db.ensureProbeHealthDashboardViews(migCtx)
	return nil
}

func (d *DB) ensureRequestLogSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
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
	    WHERE tool_calls IS NOT NULL AND jsonb_array_length(tool_calls) > 0;
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
	return d != nil && d.pool != nil
}

func (d *DB) Pool() *pgxpool.Pool {
	if d == nil {
		return nil
	}
	return d.pool
}

// Stdlib 返回一个 database/sql.DB，用于需要 *sql.DB 接口的场景。
// 注意：返回的 *sql.DB 与 Pool() 共享底层连接池，调用方不应关闭它。
func (d *DB) Stdlib() *sql.DB {
	if d == nil || d.pool == nil {
		return nil
	}
	return stdlib.OpenDB(*d.pool.Config().ConnConfig)
}

func (d *DB) Close() {
	if d != nil && d.pool != nil {
		d.pool.Close()
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
		CREATE INDEX IF NOT EXISTS idx_applications_tenant_code
		    ON applications (tenant_id, code)
		    WHERE enabled = TRUE;

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

	_, err = tx.Exec(ctx, `
		DROP VIEW IF EXISTS v_model_health_dashboard CASCADE;
		DROP VIEW IF EXISTS v_probe_queue_snapshot CASCADE;
		DROP VIEW IF EXISTS v_model_priority_details CASCADE;
		DROP VIEW IF EXISTS v_probe_system_health CASCADE;
		DROP VIEW IF EXISTS v_model_availability_timeline CASCADE;
		DROP FUNCTION IF EXISTS get_model_state_summary(TEXT) CASCADE;

		CREATE OR REPLACE VIEW v_model_health_dashboard AS
		WITH model_stats AS (
		    SELECT
		        mps.raw_model_name,
		        mps.raw_model_name as outbound_model_name,
		        'openai-completions' as protocol,
		        p.display_name as provider_name,

		        COUNT(*) as total_credentials,
		        COUNT(*) FILTER (WHERE mps.state IN ('healthy_confirmed', 'healthy')) as healthy_count,
		        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_count,
		        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering')) as failing_count,
		        COUNT(*) FILTER (WHERE mps.state = 'probing') as probing_count,

		        SUM(CASE WHEN mps.consecutive_failures >= 3 THEN 1 ELSE 0 END) as urgent_count,
		        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_priority_count,
		        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering')) as failing_priority_count,
		        COUNT(*) FILTER (WHERE mps.state = 'healthy_confirmed') as watchdog_count,

		        AVG(CASE WHEN mps.total_attempts > 0
		            THEN mps.consecutive_successes::float / mps.total_attempts * 100
		            ELSE NULL END) as avg_success_rate_7d,
		        AVG(EXTRACT(EPOCH FROM (mps.next_retry_at - NOW())) / 3600) as avg_verification_hours,
		        AVG(mps.consecutive_successes) as avg_consecutive_successes,

		        0 as total_real_success_24h,
		        0 as total_real_failure_24h,

		        MAX(mps.last_attempt_at) as last_verified_at,
		        MAX(mps.last_attempt_at) as last_real_request_at,
		        MIN(mps.next_retry_at) as next_probe_at,

		        SUM(CASE WHEN mps.state IN ('failing', 'broken_confirmed')
		                  AND mps.consecutive_failures >= 3
		             THEN 1 ELSE 0 END) as critical_nodes,

		        COUNT(*) FILTER (
		            WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes'
		              AND mps.state != 'probing'
		        ) as pending_probes_5min

		    FROM model_probe_state mps
		    JOIN credentials c ON c.id = mps.credential_id
		    JOIN providers p ON p.id = c.provider_id
		    WHERE COALESCE(c.status, 'active') = 'active'
		      AND COALESCE(c.lifecycle_status, 'active') = 'active'
		      AND COALESCE(c.manual_disabled, FALSE) = FALSE
		    GROUP BY mps.raw_model_name, p.display_name
		)
		SELECT
		    0 as provider_model_id,
		    raw_model_name,
		    outbound_model_name,
		    protocol,
		    provider_name,
		    total_credentials,
		    healthy_count,
		    suspicious_count,
		    failing_count,
		    probing_count,
		    ROUND(healthy_count * 100.0 / NULLIF(total_credentials, 0), 1) as healthy_percentage,
		    ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) as failing_percentage,
		    urgent_count,
		    suspicious_priority_count,
		    failing_priority_count,
		    watchdog_count,
		    ROUND(avg_success_rate_7d::numeric, 2) as avg_success_rate_7d,
		    ROUND(avg_verification_hours::numeric, 1) as avg_verification_hours,
		    ROUND(avg_consecutive_successes::numeric, 1) as avg_consecutive_successes,
		    total_real_success_24h,
		    total_real_failure_24h,
		    CASE
		        WHEN (total_real_success_24h + total_real_failure_24h) > 0
		        THEN ROUND((total_real_success_24h * 100.0 / (total_real_success_24h + total_real_failure_24h))::numeric, 2)
		        ELSE NULL
		    END as real_success_rate_24h,
		    last_verified_at,
		    last_real_request_at,
		    next_probe_at,
		    critical_nodes,
		    pending_probes_5min,
		    CASE
		        WHEN critical_nodes > 0 THEN 'critical'
		        WHEN ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) > 20 THEN 'warning'
		        WHEN ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) > 10 THEN 'degraded'
		        WHEN ROUND(healthy_count * 100.0 / NULLIF(total_credentials, 0), 1) >= 90 THEN 'healthy'
		        ELSE 'normal'
		    END as overall_health
		FROM model_stats
		ORDER BY
		    CASE
		        WHEN critical_nodes > 0 THEN 1
		        WHEN urgent_count > 0 THEN 2
		        WHEN ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) > 20 THEN 3
		        ELSE 4
		    END,
		    total_credentials DESC,
		    raw_model_name;

		CREATE OR REPLACE VIEW v_probe_queue_snapshot AS
		SELECT
		    sub.probe_priority,
		    sub.state,
		    COUNT(*) as queue_size,
		    COUNT(*) FILTER (WHERE sub.next_retry_at <= NOW()) as ready_now,
		    COUNT(*) FILTER (WHERE sub.next_retry_at <= NOW() + INTERVAL '1 minute') as ready_1min,
		    COUNT(*) FILTER (WHERE sub.next_retry_at <= NOW() + INTERVAL '5 minutes') as ready_5min,
		    MIN(sub.next_retry_at) as earliest_retry_at,
		    MAX(sub.next_retry_at) as latest_retry_at,
		    AVG(EXTRACT(EPOCH FROM (NOW() - sub.last_attempt_at))) as avg_wait_seconds,
		    MAX(EXTRACT(EPOCH FROM (NOW() - sub.last_attempt_at))) as max_wait_seconds
		FROM (
		    SELECT
		    CASE
		        WHEN mps.consecutive_failures >= 3 THEN 'urgent'
		        WHEN mps.state = 'suspicious' THEN 'suspicious'
		        WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
		        WHEN mps.state = 'healthy_confirmed' THEN 'watchdog'
		        ELSE NULL
		    END as probe_priority,
		        mps.state,
		        mps.next_retry_at,
		        mps.last_attempt_at
		    FROM model_probe_state mps
		    JOIN credentials c ON c.id = mps.credential_id
		    WHERE mps.state IN ('suspicious', 'failing', 'recovering')
		      AND COALESCE(c.status, 'active') = 'active'
		      AND COALESCE(c.lifecycle_status, 'active') = 'active'
		      AND COALESCE(c.manual_disabled, FALSE) = FALSE
		) sub
		GROUP BY sub.probe_priority, sub.state
		ORDER BY
		    CASE
		        WHEN sub.probe_priority = 'urgent' THEN 1
		        WHEN sub.probe_priority = 'suspicious' THEN 2
		        WHEN sub.probe_priority = 'failing' THEN 3
		        WHEN sub.probe_priority = 'watchdog' THEN 4
		        ELSE 5
		    END,
		    sub.state;

		CREATE OR REPLACE VIEW v_model_priority_details AS
		SELECT
		    mps.raw_model_name,
		    mps.raw_model_name as outbound_model_name,
		    CASE
		        WHEN mps.consecutive_failures >= 3 THEN 'urgent'
		        WHEN mps.state = 'suspicious' THEN 'suspicious'
		        WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
		        ELSE 'watchdog'
		    END as probe_priority,
		    mps.state,
		    c.id as credential_id,
		    c.label as credential_label,
		    p.display_name as provider_name,
		    mps.last_attempt_at as last_verified_at,
		    mps.next_retry_at,
		    mps.last_attempt_at as marked_suspicious_at,
		    NULL::timestamp as probing_started_at,
		    mps.consecutive_successes,
		    mps.consecutive_failures,
		    0 as consecutive_watchdog_successes,
		    CASE WHEN mps.total_attempts > 0
		         THEN mps.consecutive_successes::float / mps.total_attempts * 100
		         ELSE NULL END as success_rate_7d,
		    (mps.next_retry_at - NOW()) as verification_interval,
		    0 as real_success_24h,
		    0 as real_failure_24h,
		    mps.last_attempt_at as last_real_request_at,
		    NULL::text as last_unavailable_reason,
		    mps.last_status as last_err_code,
		    CASE
		        WHEN mps.next_retry_at <= NOW() THEN 'ready'
		        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 minute' THEN '<1min'
		        WHEN mps.next_retry_at <= NOW() + INTERVAL '5 minutes' THEN '<5min'
		        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 hour' THEN '<1h'
		        ELSE '>1h'
		    END as retry_in,
		    EXTRACT(EPOCH FROM (NOW() - mps.last_attempt_at)) / 60 as state_duration_minutes
		FROM model_probe_state mps
		JOIN credentials c ON c.id = mps.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		ORDER BY
		    mps.raw_model_name,
		    CASE
		        WHEN mps.consecutive_failures >= 3 THEN 1
		        WHEN mps.state = 'suspicious' THEN 2
		        WHEN mps.state IN ('failing', 'recovering') THEN 3
		        ELSE 4
		    END,
		    c.id;

		CREATE OR REPLACE VIEW v_probe_system_health AS
		SELECT
		    (SELECT COUNT(*) FROM model_probe_state) as total_nodes,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('healthy_confirmed', 'healthy')) as healthy_nodes,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'broken_confirmed')) as failing_nodes,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_nodes,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as probing_nodes,
		    (SELECT COUNT(*) FROM model_probe_state WHERE consecutive_failures >= 3) as urgent_queue_size,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_queue_size,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'recovering')) as failing_queue_size,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'healthy_confirmed') as watchdog_queue_size,
		    (SELECT COUNT(*) FROM model_probe_state
		     WHERE next_retry_at <= NOW() AND state != 'probing') as ready_probes,
		    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as current_probing,
		    (SELECT COUNT(DISTINCT credential_id) FROM model_probe_state
		     WHERE state = 'probing') as credentials_being_probed,
		    (SELECT ROUND(AVG(CASE WHEN total_attempts > 0
		                           THEN consecutive_successes::float / total_attempts * 100
		                           ELSE NULL END)::numeric, 2)
		     FROM model_probe_state) as avg_success_rate_7d,
		    (SELECT MAX(last_attempt_at) FROM model_probe_state) as last_probe_at,
		    (SELECT MAX(last_attempt_at) FROM model_probe_state) as last_real_request_at,
		    0 as total_real_success_24h,
		    0 as total_real_failure_24h,
		    (SELECT COUNT(*) FROM model_probe_state
		     WHERE state IN ('failing', 'broken_confirmed')
		       AND consecutive_failures >= 5) as critical_nodes,
		    (SELECT COUNT(*) FROM model_probe_state
		     WHERE next_retry_at <= NOW() + INTERVAL '5 minutes'
		       AND state != 'probing') as pending_probes_5min,
		    NOW() as snapshot_at;

		CREATE OR REPLACE VIEW v_model_availability_timeline AS
		SELECT
		    mpr.raw_model_name,
		    mpr.raw_model_name as outbound_model_name,
		    DATE_TRUNC('hour', mpr.created_at) as hour_bucket,
		    COUNT(*) as total_probes,
		    COUNT(*) FILTER (WHERE mpr.status = 'ok') as successful_probes,
		    COUNT(*) FILTER (WHERE mpr.status != 'ok') as failed_probes,
		    ROUND((COUNT(*) FILTER (WHERE mpr.status = 'ok') * 100.0 / COUNT(*))::numeric, 2) as success_rate,
		    AVG(mpr.latency_ms) FILTER (WHERE mpr.status = 'ok') as avg_latency_ms,
		    COUNT(DISTINCT mpr.credential_id) as probed_credentials,
		    COUNT(DISTINCT mpr.credential_id) FILTER (WHERE mpr.status = 'ok') as successful_credentials,
		    COUNT(DISTINCT mpr.credential_id) FILTER (WHERE mpr.status != 'ok') as failed_credentials
		FROM model_probe_runs_with_current_month mpr
		WHERE mpr.created_at >= NOW() - INTERVAL '24 hours'
		GROUP BY mpr.raw_model_name, DATE_TRUNC('hour', mpr.created_at)
		ORDER BY mpr.raw_model_name, hour_bucket DESC;

		CREATE OR REPLACE FUNCTION get_model_state_summary(p_raw_model_name TEXT)
		RETURNS TABLE (
		    state TEXT,
		    priority TEXT,
		    count BIGINT,
		    avg_success_rate NUMERIC,
		    next_probe_in_seconds INTEGER
		)
		LANGUAGE SQL
		STABLE
		AS $$
		    SELECT
		        sub.state::TEXT,
		        sub.priority::TEXT,
		        COUNT(*) as count,
		        ROUND(AVG(CASE WHEN sub.total_attempts > 0
		                       THEN sub.consecutive_successes::float / sub.total_attempts * 100
		                       ELSE NULL END)::numeric, 2) as avg_success_rate,
		        EXTRACT(EPOCH FROM MIN(sub.next_retry_at - NOW()))::INTEGER as next_probe_in_seconds
		    FROM (
		        SELECT
		            mps.state,
		            mps.consecutive_successes,
		            mps.total_attempts,
		            mps.next_retry_at,
		            CASE
		                WHEN mps.consecutive_failures >= 3 THEN 'urgent'
		                WHEN mps.state = 'suspicious' THEN 'suspicious'
		                WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
		                ELSE 'watchdog'
		            END as priority
		        FROM model_probe_state mps
		        JOIN credentials c ON c.id = mps.credential_id
		        WHERE mps.raw_model_name = p_raw_model_name
		          AND COALESCE(c.status, 'active') = 'active'
		          AND COALESCE(c.lifecycle_status, 'active') = 'active'
		          AND COALESCE(c.manual_disabled, FALSE) = FALSE
		    ) sub
		    GROUP BY sub.state, sub.priority
		    ORDER BY
		        CASE sub.priority
		            WHEN 'urgent' THEN 1
		            WHEN 'suspicious' THEN 2
		            WHEN 'failing' THEN 3
		            WHEN 'watchdog' THEN 4
		            ELSE 5
		        END,
		        sub.state;
		$$;
	`)
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
	slog.Info("probe health dashboard views ensured (v_model_health_dashboard, v_probe_queue_snapshot, v_model_priority_details, v_probe_system_health, v_model_availability_timeline, get_model_state_summary)")
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
		CREATE INDEX IF NOT EXISTS idx_licenses_key ON licenses (license_key);
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
		CREATE INDEX IF NOT EXISTS idx_oar_request ON offline_activation_requests (request_id);
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
		CREATE INDEX IF NOT EXISTS idx_releases_version ON releases (version);
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
		CREATE INDEX IF NOT EXISTS idx_cc_command_id ON center_commands (command_id);

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
		CREATE INDEX IF NOT EXISTS idx_isr_instance ON instance_status_reports (instance_id, timestamp DESC);
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
		        checkpoint IN ('preflight','copy','coverage','observe','cleanup','rollback','done')
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
	`)
	if err != nil {
		return err
	}
	slog.Info("ursm_key_migration ledger schema ensured")
	return nil
}
