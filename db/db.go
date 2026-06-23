package db

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	cfg.MaxConns = 16
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	slog.Info("postgres connected")
	db := &DB{pool: pool}
	// Use the parent ctx (no 3s timeout) for schema migrations. The
	// pingCtx above is only for the initial Ping() check; reusing it
	// for the migrations makes a real DB with many tables (15+ ALTER/
	// CREATE INDEX / MATERIALIZED VIEW statements) time out at boot.
	migCtx, migCancel := context.WithTimeout(ctx, 60*time.Second)
	defer migCancel()
	if err := db.ensureRequestLogSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureQualityFixModeSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureApplicationsTable(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureCredentialColumns(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureFpSlotLimit(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureRoutingRecentSuccessRate(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureUnavailableRecoverAtSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureWorkTypeSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.EnsureTenantsTable(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureTuningSignalsStrategyColumn(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureSessionMemoraExtractionLog(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureSessionTitles(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureTuningSignalsViews(migCtx); err != nil {
		pool.Close()
		return nil, err
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
		pool.Close()
		return nil, err
	}
	if err := db.EnsureMaasSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureRoutingOverridesTable(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureRoutingOverridesAudit(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensurePassiveProbeStateSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureTenantModelPoliciesSchema(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureSupplementalRLS(migCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
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
		    ADD COLUMN IF NOT EXISTS outbound_msg_hashes JSONB;
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
	slog.Info("request_logs schema ensured (gw_session_id, gw_task_id, request_status, api_key_prefix, api_key_owner_user, application_code, parent_request_id, compression_reason, compression_strategy, compression_meta, outbound_body, outbound_msg_count, outbound_token_est, outbound_msg_hashes, quality_flags, quality_fix_actions, quality_score)")
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

// EnsureUsersTable creates the users table for multi-tenant admin auth.
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
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
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
    UNIQUE (work_type_key, canonical_name)
);
CREATE INDEX IF NOT EXISTS idx_wtmr_work_type ON work_type_model_route (work_type_key);

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
{"summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"]}
要求：
- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何
- key_points 提取 3-5 个关键事实或决策点，每条 15-40 字
- 不要输出 JSON 以外的任何文本
- 如果语料中包含错误信息，务必在总结中提及'
  )
ON CONFLICT (key) DO NOTHING;

INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled)
VALUES
  ('session_title',   'minimax-m2.7',  1.00, 0, TRUE),
  ('session_title',   'glm-5.1',       0.95, 0, TRUE),
  ('session_title',   'minimax-m3',    0.90, 0, TRUE),
  ('session_title',   'deepseek-chat', 0.85, 0, TRUE),
  ('session_summary', 'minimax-m2.7',  1.00, 0, TRUE),
  ('session_summary', 'glm-5.1',       0.95, 0, TRUE),
  ('session_summary', 'minimax-m3',    0.90, 0, TRUE),
  ('session_summary', 'deepseek-chat', 0.85, 0, TRUE)
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

// ensureApplicationsTable creates the applications table (used by api_keys
// for tenant-scoped application_code references) and seeds a default
// 'admin' application if missing. The applications table is referenced
// by admin/auth.go's verifyAdminAuth, which requires app.code == "admin"
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

		-- Seed default 'admin' application for super-admin auth.
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

// ensureRoutingRecentSuccessRate mirrors db/migrations/035_routing_recent_success_rate.sql.
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
//     function is STABLE and uses idx_request_logs_credential_ts so the
//     LIMIT scan is a 50-row index descent.
//
// Runs at startup via ensureSchema so every gateway instance converges on the
// same function definition without a separate migration runner.
func (d *DB) ensureRoutingRecentSuccessRate(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	_, err := d.pool.Exec(ctx, `
		-- (1) Backfill broken_confirmed → binding available=FALSE.
		UPDATE credential_model_bindings cmb
		SET available          = FALSE,
		    unavailable_reason = 'model_probe_broken',
		    unavailable_at     = NOW()
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND EXISTS (
		      SELECT 1 FROM model_probe_state mps
		      WHERE mps.credential_id = cmb.credential_id
		        AND mps.raw_model_name = pm.raw_model_name
		        AND mps.state = 'broken_confirmed'
		  );

		-- (2) recent_success_rate helper. DROP+CREATE keeps the body in sync
		--     with the source file even if a prior deploy left an older body.
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
		        FROM request_logs
		        WHERE credential_id = p_credential_id
		          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
		          AND ts > NOW() - (p_window_hours || ' hours')::interval
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
		        WHEN 'continuous_failure' THEN INTERVAL '2 hours'
		        ELSE INTERVAL '30 seconds'
		    END)
		WHERE available = FALSE AND unavailable_recover_at IS NULL AND unavailable_at IS NOT NULL
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
