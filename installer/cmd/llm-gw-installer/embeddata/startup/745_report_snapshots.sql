-- 745: report_snapshots 日报表快照表（对账报表设计切片，2026-09-24 投递）。
-- 设计文档：docs/reconciliation/design-report-rollup.md。
--
-- 状态注记①：本表消费方（报表 worker bg/report_rollup_worker.go 与
-- admin/report_rollup.go 读面）尚未实现，本迁移属设计预埋——先钉住表结构
-- 契约，避免 worker 落地时再补迁移号。无读写方时建表零运行时代价。
--
-- rollup 粒度契约②（与设计 §2/§3 对齐，2026-09-24 修正：原死文件
-- migrations/745_report_snapshots.sql 的 UNIQUE(scope, scope_key,
-- report_date) 无模型维度，装不下 provider×model×day 粒度）：
--   - scope 四值枚举：daily_total | daily_by_provider | daily_by_model |
--     internal_tenant；
--   - daily_by_model 行：scope_key = canonical model（models_canonical 口
--     径）、raw_model_name = 原始模型名（usage_facts.raw_model_name 原值），
--     粒度 = canonical × raw × day；
--   - 其余行（daily_total / daily_by_provider / internal_tenant）不含模型
--     维度，raw_model_name = ''（NOT NULL DEFAULT '' 哨兵，进唯一键保证
--     ON CONFLICT 幂等回填可在两粒度上并存）。
--   - 唯一键 (scope, scope_key, report_date, raw_model_name)：worker 写入
--     走 INSERT ... ON CONFLICT 同键 DO UPDATE，可重入。
--
-- 演进注记③：热区 + 分区（report_snapshots_hot + report_date 月分区，
-- 对照 request_logs 家族与 bg/partition_manager.go 的 archiveSpec 保留清
-- 理模式）与索引加密（date 前导索引等）待消费方落地、数据量实测后一并
-- 演进；MVP 阶段单表 + (scope, report_date DESC) 一条索引即可（设计 §2.2
-- 原文口径）。原死文件第二条索引 idx_report_snapshots_date(report_date)
-- 已删：计划内读者只按 (scope, report_date)（API §4）或完整唯一键（§3
-- worker）过滤，date 前导索引无消费方，随消费方落地再评估。
--
-- 幂等声明④（2026-09-25 补，对齐 800_provider_endpoint_protocols.sql 的
-- IDEMPOTENCY 注记风格）：本文件可安全重复执行（双跑零副作用）——DDL 仅含
-- CREATE TABLE IF NOT EXISTS 与 CREATE INDEX IF NOT EXISTS 两种语句，二跑
-- 零结构变更、零数据改写；worker 写入面另有 INSERT ... ON CONFLICT 幂等
-- 回填（见粒度契约②）。无破坏性语句，down（745_report_snapshots.down.sql）
-- 仅 DROP TABLE IF EXISTS。

CREATE TABLE IF NOT EXISTS report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,        -- daily_total | daily_by_provider | daily_by_model | internal_tenant
    scope_key           TEXT NOT NULL,        -- *_by_* : provider_id / canonical model / tenant_id; daily_total : 'all'
    report_date         DATE NOT NULL,        -- UTC day the snapshot covers
    raw_model_name      TEXT NOT NULL DEFAULT '',  -- daily_by_model 行 = 原始模型名；其余行 ''（非模型维度）
    granularity         TEXT NOT NULL DEFAULT 'day',  -- reserved for future week/month
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    cache_hit_ratio     NUMERIC(6,4)          -- cache_read / (input_tokens+cache_read); nullable when denom=0
                            CHECK (cache_hit_ratio IS NULL OR (cache_hit_ratio >= 0 AND cache_hit_ratio <= 1)),
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,   -- 成本口径统一为分（BIGINT），避免浮点漂移
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,               -- nullable; set for daily_by_provider
    canonical_id        BIGINT,               -- nullable; set for daily_by_model / internal_tenant
    tenant_id           BIGINT,               -- nullable; set for internal_tenant
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 命名约束与 SSOT（sql/objects/tables/report_snapshots.sql）保持一致。
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
);

CREATE INDEX IF NOT EXISTS idx_report_snapshots_scope_date
    ON report_snapshots (scope, report_date DESC);
