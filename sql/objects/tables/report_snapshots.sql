--
-- Name: report_snapshots; Type: TABLE; Schema: public; Owner: -
--
-- 日报表快照表（对账报表设计切片）。SSOT 规范体；startup 迁移
-- sql/migrations/startup/745_report_snapshots.sql 与 db.go 的
-- ensureReportSnapshots 均以本文件为准（迁移体带 IF NOT EXISTS 幂等守卫）。
--
-- 状态：消费方（bg/report_rollup_worker.go / admin/report_rollup.go）尚未
-- 实现，设计预埋表。粒度契约与演进计划（hot + 月分区）见迁移文件头注记。
--
-- 唯一键 (scope, scope_key, report_date, raw_model_name)：
--   - scope ∈ daily_total | daily_by_provider | daily_by_model | internal_tenant
--   - daily_by_model 行：scope_key = canonical model、raw_model_name = 原始
--     模型名；其余行 raw_model_name = ''（非模型维度哨兵）。

CREATE TABLE public.report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,
    scope_key           TEXT NOT NULL,
    report_date         DATE NOT NULL,
    raw_model_name      TEXT NOT NULL DEFAULT '',
    granularity         TEXT NOT NULL DEFAULT 'day',
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    cache_hit_ratio     NUMERIC(6,4)
                            CHECK (cache_hit_ratio IS NULL OR (cache_hit_ratio >= 0 AND cache_hit_ratio <= 1)),
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,
    canonical_id        BIGINT,
    tenant_id           BIGINT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
);

CREATE INDEX idx_report_snapshots_scope_date
    ON report_snapshots (scope, report_date DESC);
