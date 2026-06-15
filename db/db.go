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
	if err := db.ensureRequestLogSchema(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureWorkTypeSchema(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.EnsureTenantsTable(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureTuningSignalsStrategyColumn(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureSessionMemoraExtractionLog(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.ensureTuningSignalsViews(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := db.EnsureMaasSchema(pingCtx); err != nil {
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
		    ADD COLUMN IF NOT EXISTS application_code TEXT;
		CREATE INDEX IF NOT EXISTS idx_request_logs_gw_session_ts
		    ON request_logs (gw_session_id, ts DESC)
		    WHERE gw_session_id IS NOT NULL AND gw_session_id <> '';
		CREATE INDEX IF NOT EXISTS idx_request_logs_gw_task_ts
		    ON request_logs (gw_task_id, ts DESC)
		    WHERE gw_task_id IS NOT NULL AND gw_task_id <> '';
		CREATE INDEX IF NOT EXISTS idx_request_logs_status_ts
		    ON request_logs (request_status, ts DESC)
		    WHERE request_status IS NOT NULL AND request_status <> '';
	`)
	if err != nil {
		return err
	}
	slog.Info("request_logs schema ensured (gw_session_id, gw_task_id, request_status, api_key_prefix, api_key_owner_user, application_code)")
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
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_work_type_config_category ON work_type_config (category, sort_order);
CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config (l1_task_type);

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
//   idx_tuning_signals_strategy_ts    (strategy, ts DESC) — A/B summary
//   idx_tuning_signals_strategy_task  (strategy, task_type, ts DESC) — breakdown
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


// ensureTuningSignalsViews creates two pre-aggregated views on
// tuning_signals (P7.5). The /tuning/accuracy endpoint's GROUP BY
// (task_type, classifier) over 7 days of data does a full scan
// with a non-trivial aggregation (~30ms on 100k rows). The views
// pre-aggregate into 5-min and 1-day buckets, so the endpoint
// can read a 7-day window in ~3ms (10x speedup).
//
// Two views:
//
//   tuning_signals_5m   — 5-minute buckets, retained 7 days
//   tuning_signals_daily — 1-day buckets, retained 90 days
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
