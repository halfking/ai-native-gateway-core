-- Migration 350: 模型智商（Model IQ）体系
-- Date: 2026-08-11
-- Purpose:
--   1) 为 models_canonical 增加标准智商列（来自评测站点 Artificial Analysis
--      Intelligence Index，0-100 分）；
--   2) 新增 model_iq_runs：每次对 (credential_id, raw_model_name) 节点做智商
--      测试的时点明细（append-only，含 trigger_kind / grade / probe_kind /
--      tested_at），支撑「供应商模型列表点击查看不同时点智商值」；
--   3) 新增 node_iq_latest：节点最新值 + 历史平均的缓存表，供模型列表批量
--      渲染及供应商品质计算（ModelIQ 维度）读取。
--
-- 节点定义沿用 credential_model_bindings 的 (credential_id, raw_model_name)，
-- 不新增外键约束以免影响现有 binding 的 rename/重写路径（参照 node_probe_state
-- 的做法）。设计文档：docs/model-iq/01-design.md
BEGIN;

-- 1. models_canonical 标准智商列
ALTER TABLE models_canonical
    ADD COLUMN IF NOT EXISTS standard_iq numeric(5,2),
    ADD COLUMN IF NOT EXISTS standard_iq_source text DEFAULT 'artificialanalysis',
    ADD COLUMN IF NOT EXISTS standard_iq_updated_at timestamptz;

COMMENT ON COLUMN models_canonical.standard_iq IS '标准智商值（0-100，来自评测站点，默认 Artificial Analysis Intelligence Index）';
COMMENT ON COLUMN models_canonical.standard_iq_source IS '标准智商数据来源标签，如 artificialanalysis / artificialanalysis-v4.1.1 / manual';
COMMENT ON COLUMN models_canonical.standard_iq_updated_at IS '标准智商最近一次更新时间';

-- 2. 节点智商测试明细（每次测试一行，append-only）
CREATE TABLE IF NOT EXISTS model_iq_runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    credential_id   bigint NOT NULL,
    provider_id     bigint NOT NULL,
    raw_model_name  text NOT NULL,
    canonical_id    bigint,
    benchmark_type  text NOT NULL DEFAULT 'mmlu_lite',   -- mmlu_lite / mmlu / custom
    total_questions integer NOT NULL DEFAULT 0,
    correct_count   integer NOT NULL DEFAULT 0,
    accuracy        numeric(5,2) NOT NULL DEFAULT 0,      -- 0-100 准确率
    stability       numeric(5,2),                          -- 0-100 稳定性(成功率)
    latency_p95     integer,                               -- 毫秒
    overall_score   numeric(5,2) NOT NULL DEFAULT 0,       -- 0-100 综合智商(准确率0.6+稳定性0.3+延迟0.1)
    grade           text,                                  -- A+/A/B+/B/C/D/F
    probe_kind      text NOT NULL DEFAULT 'direct',        -- gateway / direct / mock
    trigger_kind    text NOT NULL DEFAULT 'scheduled',     -- scheduled / on_demand / anomaly
    status          text NOT NULL DEFAULT 'success',       -- success / partial / failed
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

COMMENT ON TABLE model_iq_runs IS '每次节点(凭据+模型)智商测试的时点明细记录';
COMMENT ON COLUMN model_iq_runs.trigger_kind IS 'scheduled=定时自检 / on_demand=前端或API手动触发 / anomaly=可疑动作触发';
COMMENT ON COLUMN model_iq_runs.overall_score IS '综合智商 = 准确率*0.6 + 稳定性*0.3 + 延迟评分*0.1 (0-100)';

-- 3. 节点智商最新值缓存（1:1 到可路由节点，UPSERT 写入）
CREATE TABLE IF NOT EXISTS node_iq_latest (
    credential_id   bigint NOT NULL,
    raw_model_name  text NOT NULL,
    overall_score   numeric(5,2),                          -- 最新一次智商
    grade           text,
    sample_count    integer NOT NULL DEFAULT 0,             -- 历史样本数
    avg_score       numeric(5,2),                          -- 历史平均智商
    min_score       numeric(5,2),
    max_score       numeric(5,2),
    tested_at       timestamptz,                            -- 最新测试时间
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, raw_model_name)
);

COMMENT ON TABLE node_iq_latest IS '节点(凭据+模型)智商最新值与历史聚合缓存，供模型列表与品质计算读取';

COMMIT;
