--
-- Name: report_snapshots; Type: TABLE; Schema: public; Owner: -
--
-- 日报表快照表（对账报表）。SSOT 规范体；startup 迁移
-- sql/migrations/startup/745_report_snapshots.sql（建表）+
-- 746_report_snapshots_internal_dims.sql（内部对帐维度补齐）与 db.go 的
-- ensureReportSnapshots 均以本文件为准（迁移体带幂等守卫）。
--
-- 状态：消费方已落地 —— bg/report_rollup_worker.go（每日 RunHour 聚合
-- usage_facts 写入，默认 02:00 UTC，settings 键 reports.daily_rollup.hour）
-- 与 admin/report_rollup.go（区间汇总读面 + Excel 导出）。
--
-- 唯一键 (scope, scope_key, report_date, raw_model_name)：
--   - scope ∈ daily_total | daily_by_provider | daily_by_model |
--     internal_tenant | internal_person | internal_model（TEXT 无 CHECK，
--     本注记为唯一约束源）
--   - provider 面三 scope（daily_*）含全部流量类（探针/自检同样烧供应
--     商钱，对帐须全量）；internal 面三 scope 仅 business 流量（内部计
--     费口径）。
--   - daily_by_model 行：scope_key = provider_id（文本化）、raw_model_name
--     = 出站模型名（usage_facts.raw_model_name = outbound_model 回落
--     client_model）；internal_model 行：scope_key = tenant_id、
--     raw_model_name = 出站模型名；internal_person 行：scope_key =
--     len(tenant_id) + ':' + tenant_id + ':' + person（person = end_user_id，
--     缺失回落 'person:'+person_hash，双缺 'unknown'；R65 起租户编码进键
--     ——四键 UNIQUE 不含 tenant_id 列，跨租户同名人员裸键会在 ON
--     CONFLICT 中互相覆盖。02da86168 根修弃 R65 原稿的 '\x00' 分隔——
--     PG TEXT 拒绝 NUL 字节，真库 INSERT 全量 22021）；其余行
--     raw_model_name = ''（非模型维度哨兵）。
--   - 唯一键支撑 worker 的 INSERT ... ON CONFLICT DO UPDATE 幂等回填。

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
    tenant_id           TEXT,
    credits_charged     BIGINT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
);

CREATE INDEX idx_report_snapshots_scope_date
    ON report_snapshots (scope, report_date DESC);
