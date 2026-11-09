-- ===========================================================================
-- File:          sql/migrations/startup/955_passive_model_fingerprints.sql
-- Migration:     955
-- Database:      llm_gateway
-- Purpose:       被动式模型指纹的落点：passive_model_fingerprints。
--
--                从**真实用户流量**聚合出的每个 (凭据×模型) 节点行为基线
--                （时序特征 TTFT/ITT/吞吐 + Token 签名 + 内容特征），供
--                domains/modelcredibility.EvaluatePassive 做零成本、难规避
--                的持续渗水检测（与主动探针互补：被动为主、主动为辅）。
--
-- Status:        active
-- Idempotent:    YES（CREATE TABLE IF NOT EXISTS）
-- Dependencies:  provider_model_credibility（922，同一判定族）、credentials
--
-- ★★ 为什么需要这张表（缺口本体）
--
-- 922 的可信度判定是**主动**的：直连上游发标定请求。它有三个结构性弱点：
--   · 成本：每个节点一次探测请求（行为指纹全量采集要 240 次/节点）；
--   · 可规避：上游可识别探测特征（固定提示词、temperature=1.0）针对性放行；
--   · 覆盖稀疏：周期扫描之间是盲区。
-- 被动指纹从真实流量提取（SSE 流的 EmitAt 时间戳差 ⇒ ITT 分布、首 token
-- 延迟 ⇒ TTFT、usage ⇒ token 签名），**零额外请求、不可区分于正常流量、
-- 每个请求都是数据点**。学术支撑：加密流量元数据分析对服务模型的识别
-- 准确率 97.7%（arXiv:2609.31877）；Inter-Token Timing 在加密流量下仍可
-- 实时识别（arXiv:2502.20589）。
--
-- ★★ 口径纪律（与 922 一致）
--
--   · 一行 = 一个 (租户, 凭据, 模型) 节点的**聚合基线**，不是逐请求明细；
--     credential_id = 0 保留给「按模型跨凭据聚合」的模型级基线。
--   · 样本不足时 sample_count 小，消费方（EvaluatePassive）按
--     confidence = min(1, sample_count/100) 降权 —— 表不丢样本量信息。
--   · ITT 分布以 10ms 桶宽落 jsonb（"0"/"10"/.../"90"/"100+"），与
--     domains/modelcredibility.bucketizeITTs 的桶划分逐字一致，JSD 对比
--     直接可用；改桶宽必须两侧同步。
--   · 时序列一律毫秒 real：Go 侧 time.Duration 在边界换算，表里不存
--     Duration 字符串（不可比、不可聚合）。
-- ===========================================================================

BEGIN;
SET LOCAL lock_timeout = '5s';
-- ★ statement_timeout 抬 10min（heavy-DDL 预算门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget
--   的要求，761 先例）：本迁移含两条非 CONCURRENTLY 的 CREATE INDEX。全新安装
--   建在空表上实测极快，但门要的是「按目标环境最大表实测后抬够」的预算声明
--   （914 反例：拍 60s ⇒ 部署中止），不是运行时快慢本身。
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.passive_model_fingerprints (
    id                bigserial PRIMARY KEY,
    tenant_id         text NOT NULL DEFAULT 'default',

    -- 节点身份键。credential_id = 0 ⇒ 模型级聚合基线（跨凭据）。
    credential_id     bigint NOT NULL DEFAULT 0,
    model_name        text NOT NULL,

    -- ── 时序特征（权重 0.6，最强的被动信号）───────────────────────────
    -- TTFT = 首 content chunk 时刻 − 请求发出时刻。
    ttft_mean_ms      real,
    ttft_p95_ms       real,
    -- ITT = 相邻 content chunk 的时刻差（>0 且 <5s，过滤异常值）。
    itt_mean_ms       real,
    itt_std_ms        real,
    itt_p50_ms        real,
    itt_p95_ms        real,
    -- 桶化分布（10ms 桶宽，与 bucketizeITTs 逐字一致）：{"40":0.30,...}
    itt_distribution  jsonb,
    -- tokens / (最后 content 时刻 − 首 content 时刻)。
    throughput_mean   real,
    throughput_std    real,

    -- ── Token 签名（权重 0.25）────────────────────────────────────────
    -- 标准化 prompt hash → token 计数（分词器指纹；同族应相同）。
    prompt_token_signature jsonb,
    -- completion_tokens / prompt_tokens 的聚合（详细度指纹）。
    output_density_mean    real,
    output_density_std     real,

    -- ── 内容特征（权重 0.15，弱信号）──────────────────────────────────
    -- 高频词频率（可选采集，未启用时为 NULL）。
    vocab_freq        jsonb,
    -- 格式位标聚合：bit0=### bit1=**bold** bit2=list bit3=code block。
    format_flags      integer NOT NULL DEFAULT 0,

    -- ── 统计元数据 ────────────────────────────────────────────────────
    -- 样本不足 ⇒ 消费方按 min(1, sample_count/100) 降权，不丢信息。
    sample_count      integer NOT NULL DEFAULT 0,
    collected_from    timestamptz,
    collected_to      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT passive_model_fingerprints_key
        UNIQUE (tenant_id, credential_id, model_name),
    CONSTRAINT passive_model_fingerprints_credential_check
        CHECK (credential_id >= 0),
    CONSTRAINT passive_model_fingerprints_sample_count_check
        CHECK (sample_count >= 0)
);

COMMENT ON TABLE public.passive_model_fingerprints IS
    '被动式模型指纹基线：从真实流量聚合的时序/Token/内容特征，供零成本渗水检测对比';
COMMENT ON COLUMN public.passive_model_fingerprints.credential_id IS
    '凭据 ID；0 = 按模型跨凭据聚合的模型级基线';
COMMENT ON COLUMN public.passive_model_fingerprints.itt_distribution IS
    'Inter-Token Time 桶化分布（10ms 桶宽），桶标签 "0"/"10"/.../"90"/"100+"，值为概率；与 domains/modelcredibility.bucketizeITTs 逐字一致';
COMMENT ON COLUMN public.passive_model_fingerprints.ttft_mean_ms IS
    'Time-to-First-Token 均值（毫秒）；Opus ~1000 / Sonnet ~500 / Haiku ~300 量级的档位指纹';
COMMENT ON COLUMN public.passive_model_fingerprints.sample_count IS
    '聚合样本数；判定置信度 = min(1, sample_count/100)';
COMMENT ON COLUMN public.passive_model_fingerprints.format_flags IS
    '格式特征位标聚合：bit0=### bit1=**bold** bit2=list bit3=code block';

CREATE INDEX IF NOT EXISTS idx_passive_model_fingerprints_model
    ON public.passive_model_fingerprints (tenant_id, model_name);
CREATE INDEX IF NOT EXISTS idx_passive_model_fingerprints_updated
    ON public.passive_model_fingerprints (tenant_id, updated_at DESC);

INSERT INTO public.schema_migrations (version, description)
VALUES ('955', 'passive model fingerprints: real-traffic behavioral baseline per (credential, model)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
