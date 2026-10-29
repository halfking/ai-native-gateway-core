-- ===========================================================================
-- File:          sql/migrations/startup/922_model_credibility.sql
-- Migration:     922
-- Database:      llm_gateway
-- Purpose:       模型可信度（防掺水）体系的落点：
--                  1) provider_model_credibility        —— 每个 (凭据×模型) 节点的当前判定
--                  2) provider_model_credibility_runs   —— 判定历史（append-only）
--                  3) provider_quality_profiles 补齐 reliability_score / calculated_at
--                     （供应商×模型粒度的「可信度参数」落点；顺带修一个全新安装会
--                      缺列的真缺陷，见下）
--
-- Status:        active
-- Idempotent:    YES（CREATE TABLE IF NOT EXISTS / ADD COLUMN IF NOT EXISTS）
-- Dependencies:  model_iq_runs（350）、node_iq_latest（350）、models_canonical、
--                provider_models、credentials、providers
--
-- ★★ 为什么要这两张表（缺口本体）
--
-- 仓内已有的能力面覆盖「质量」，没有覆盖「身份」：
--   · domains/modelquality 用 MMLU 精简题库给每个 (凭据,模型) 节点打智商分，
--     写 model_iq_runs / node_iq_latest；但它只与**上一次**自己的成绩比较
--     （checkQualityDrop），不与同款模型的**其它节点**比较，也不校验上游
--     到底在跑哪个模型。
--   · domains/streaming/integrity 是**被动**检测：等真实流量里出现
--     model_mismatch / fingerprint_drift 才落一条事件。
--   · models_canonical.standard_iq 是外部基线（Artificial Analysis 指数），
--     但它与本地 MMLU 准确率**不是同一把尺**，不能直接相减。
-- ⇒ 「你买的是 claude-sonnet-4-5，上游实际给你 Haiku」这类渗水，本仓此前看不见。
--
-- ★★ 为什么 provider_quality_profiles 要补两列（真缺陷，不是本轮引入的）
--
-- migration 435 建这张表时**没有**建 reliability_score / calculated_at，
-- 但 pg_dump 基线（deploy/sql/schemas/baseline/01-schema.sql）里有 ⇒
--   · 从基线建的库：有这两列。
--   · 只跑迁移建的库（全新安装路径）：**没有**这两列。
-- 而 internal/quality/profile_updater.go:133 的 INSERT 句写死了
-- reliability_score 与 calculated_at ⇒ 全新安装上那条 INSERT 会 42703。
-- ⇒ 本迁移补齐这两列（幂等），顺带把「供应商×模型粒度的可信度参数」
--    落到 L3 可信度列上。
--
-- 判定口径（domains/modelcredibility）：
--   · 四把互补的尺：身份自洽 / 能力水位（同款模型同伴对比）/ 指纹一致性
--     （同节点前后漂移）/ 参考基线（standard_iq 平台级校准）。
--   · **缺失维度不计入**（不按 0 分算）：四维都没测到 ⇒ 结论 unknown，
--     而不是「综合分 0」。
--   · 能力对比**只用 status='success' 的 model_iq_runs 行**：partial 行
--     的准确率分母含出错题（domains/modelquality/executor.go 的
--     Accuracy = CorrectCount / TotalQuestions），被系统性压低 ⇒
--     拿它对比会把「当时网络抖了一下」误判成渗水。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '30min';

-- ── 1) 当前判定（每个 凭据×模型 一行）────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.provider_model_credibility (
    id                   bigserial PRIMARY KEY,
    tenant_id            text NOT NULL DEFAULT 'default',
    credential_id        bigint NOT NULL,
    provider_id          bigint NOT NULL,
    raw_model_name       text NOT NULL,
    canonical_id         bigint,
    canonical_name       text,

    -- 四个维度的分数（0-100）。NULL = 该维度未测量（不是 0 分）。
    credibility_score    numeric(5,2),
    identity_score       numeric(5,2),
    capability_score     numeric(5,2),
    consistency_score    numeric(5,2),
    reference_score      numeric(5,2),

    -- verdict: verified / suspected / diluted / unknown
    --   verified  = 身份与能力均与声称一致，且证据维度数达标
    --   suspected = 存在异常信号，证据不足以定性
    --   diluted   = 能力显著低于同款模型最好水平，或身份自证换族
    --   unknown   = 证据不足（未探测 / 探测失败 / 维度缺失）
    verdict              text NOT NULL DEFAULT 'unknown',
    -- status: success / partial / failed / inconclusive（本次探测状态）
    status               text NOT NULL DEFAULT 'unknown',

    -- 指纹原始信号（漂移检测的基线）
    reported_model       text,
    prompt_token_signature integer,
    system_fingerprint   text,

    evidence             jsonb NOT NULL DEFAULT '{}'::jsonb,
    anomalies            jsonb NOT NULL DEFAULT '[]'::jsonb,
    measured_dimensions  integer NOT NULL DEFAULT 0,
    measured_at          timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT provider_model_credibility_key
        UNIQUE (tenant_id, credential_id, raw_model_name),
    CONSTRAINT provider_model_credibility_verdict_check
        CHECK (verdict IN ('verified','suspected','diluted','unknown')),
    CONSTRAINT provider_model_credibility_status_check
        CHECK (status IN ('success','partial','failed','inconclusive','unknown'))
);

COMMENT ON TABLE public.provider_model_credibility IS
    '模型可信度判定当前态：每个 (租户,凭据,模型) 节点一行；四维度分数 + 结论 + 指纹基线';
COMMENT ON COLUMN public.provider_model_credibility.credibility_score IS
    '综合可信度（0-100）。按【有数据的维度】重新归一化的加权平均；全缺失时为 NULL（不是 0）';
COMMENT ON COLUMN public.provider_model_credibility.identity_score IS
    '身份自洽：上游自报的模型与请求模型是否一致（跨族换模型 ⇒ 低分）';
COMMENT ON COLUMN public.provider_model_credibility.capability_score IS
    '能力水位：本节点实测准确率相对同款模型最好节点的水平（同族降级的唯一抓手）';
COMMENT ON COLUMN public.provider_model_credibility.consistency_score IS
    '指纹一致性：同一节点的请求签名（prompt token 计数 / system_fingerprint）是否稳定';
COMMENT ON COLUMN public.provider_model_credibility.reference_score IS
    '参考基线：与 standard_iq 平台级校准出的期望准确率的差距';
COMMENT ON COLUMN public.provider_model_credibility.measured_dimensions IS
    '本次判定有几个维度有数据（0~4）。用于区分「综合分 0（没测过）」与「综合分 0（测了但极差）」';
COMMENT ON COLUMN public.provider_model_credibility.prompt_token_signature IS
    '标定提示词的 prompt token 计数 —— 同节点前后两次不一致 ⇒ 后端被换（跨族换模型会换分词器）';

CREATE INDEX IF NOT EXISTS idx_provider_model_credibility_provider
    ON public.provider_model_credibility (tenant_id, provider_id, raw_model_name);
CREATE INDEX IF NOT EXISTS idx_provider_model_credibility_canonical
    ON public.provider_model_credibility (tenant_id, canonical_id)
    WHERE canonical_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_provider_model_credibility_verdict
    ON public.provider_model_credibility (tenant_id, verdict, measured_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_model_credibility_measured_at
    ON public.provider_model_credibility (tenant_id, measured_at DESC);

-- ── 2) 判定历史（append-only）─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.provider_model_credibility_runs (
    id                   bigserial PRIMARY KEY,
    tenant_id            text NOT NULL DEFAULT 'default',
    credential_id        bigint NOT NULL,
    provider_id          bigint NOT NULL,
    raw_model_name       text NOT NULL,
    canonical_id         bigint,
    canonical_name       text,

    credibility_score    numeric(5,2),
    identity_score       numeric(5,2),
    capability_score     numeric(5,2),
    consistency_score    numeric(5,2),
    reference_score      numeric(5,2),

    verdict              text NOT NULL DEFAULT 'unknown',
    status               text NOT NULL DEFAULT 'unknown',
    measured_dimensions  integer NOT NULL DEFAULT 0,

    reported_model       text,
    prompt_token_signature integer,
    system_fingerprint   text,
    http_status          integer,
    latency_ms           integer,

    evidence             jsonb NOT NULL DEFAULT '{}'::jsonb,
    anomalies            jsonb NOT NULL DEFAULT '[]'::jsonb,
    error                text,
    measured_at          timestamptz NOT NULL DEFAULT now(),
    created_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT provider_model_credibility_runs_verdict_check
        CHECK (verdict IN ('verified','suspected','diluted','unknown')),
    CONSTRAINT provider_model_credibility_runs_status_check
        CHECK (status IN ('success','partial','failed','inconclusive','unknown'))
);

COMMENT ON TABLE public.provider_model_credibility_runs IS
    '模型可信度判定历史（append-only）：每次扫描一行，供趋势图与复核使用';

CREATE INDEX IF NOT EXISTS idx_provider_model_credibility_runs_node
    ON public.provider_model_credibility_runs (tenant_id, credential_id, raw_model_name, measured_at DESC);
CREATE INDEX IF NOT EXISTS idx_provider_model_credibility_runs_measured_at
    ON public.provider_model_credibility_runs (tenant_id, measured_at DESC);

-- ── 3) 供应商×模型粒度的「模型可信度参数」落点（顺带修全新安装缺列）──────
--
-- provider_quality_profiles 的 L3 可信度列（reliability_score）在 pg_dump
-- 基线里有、在 migration 435 里没有 ⇒ 只跑迁移的全新安装会缺列 ⇒
-- internal/quality/profile_updater.go 的 INSERT 会 42703。
-- 这里幂等补齐。
--
-- ★★ **为什么模型可信度另起一列，而不是写进 reliability_score**：
-- reliability_score 在仓内已被占用，含义是「**服务可靠性**」
-- （internal/quality/scorer_reliability.go：在线率 50% + 成功率一致性 30%
-- + 错误率趋势 20%），与「这个供应商给的是不是它声称的那个模型」**不是
-- 同一件事** ⇒ 写进同一列就是**一列两义**，读方无法分辨 ⇒ 另起
-- model_credibility_score / model_credibility_verdict 两列。
ALTER TABLE public.provider_quality_profiles
    ADD COLUMN IF NOT EXISTS reliability_score numeric(5,2) DEFAULT 0;
ALTER TABLE public.provider_quality_profiles
    ADD COLUMN IF NOT EXISTS calculated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE public.provider_quality_profiles
    ADD COLUMN IF NOT EXISTS model_credibility_score numeric(5,2);
ALTER TABLE public.provider_quality_profiles
    ADD COLUMN IF NOT EXISTS model_credibility_verdict text;
ALTER TABLE public.provider_quality_profiles
    ADD COLUMN IF NOT EXISTS model_credibility_sample_count integer;

COMMENT ON COLUMN public.provider_quality_profiles.reliability_score IS
    'L3 服务可靠性（0-100）：在线率 + 成功率一致性 + 错误率趋势（internal/quality/scorer_reliability.go）。★ 与下面的 model_credibility_score 不是一回事。';
COMMENT ON COLUMN public.provider_quality_profiles.model_credibility_score IS
    '模型可信度（0-100，供应商×模型粒度）：取该供应商该模型下**最差**一条凭据的可信度判定分（掺水是"至少有一条线是假的"⇒ 平均会把假线稀释掉）。来源 provider_model_credibility。';
COMMENT ON COLUMN public.provider_quality_profiles.model_credibility_verdict IS
    '模型可信度结论（verified/suspected/diluted/unknown），取最差那条凭据的结论';

-- ★ 不新建 (provider_id, model_name) 唯一索引：
--   provider_quality_profiles_provider_id_model_name_key 已经是
--   UNIQUE(provider_id, model_name) ⇒ 本轮写入方直接复用它做 ON CONFLICT 目标
--   （Postgres 的唯一约束里 NULL 互不相等 ⇒ model_name IS NULL 的供应商级行
--     不受影响）。再建一个部分唯一索引是冗余，且在已有重复数据的库上会失败。
