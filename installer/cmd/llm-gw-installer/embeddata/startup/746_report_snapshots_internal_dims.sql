-- 746: report_snapshots 内部对帐维度补齐（2026-09-25 对账报表落地轮）。
-- 设计文档：docs/reconciliation/design-report-rollup.md（§2/§8 内部价与
-- 租户/人员视角由 follow-up 提升为本轮落地范围）。
--
-- 三件事：
--   ① tenant_id BIGINT → TEXT：745 建表时按 bigint 设计，但数据源
--      usage_facts/stats_usage_daily 的 tenant_id 全线为 text
--      （DEFAULT 'default'），bigint 列装不下真实租户键。本表尚无任何
--      写入方（零数据），ALTER TYPE 安全；USING tenant_id::text 使语句
--      在已转换库上重复执行同样 no-op（text::text 合法），可重入。
--   ② 新增内部口径列：credits_charged（内部计费积分，来源
--      usage_facts.credits_charged，内部价 + 折扣 + 峰谷倍率的最终计费
--      结果）与 latency_p50_ms / latency_p95_ms（模型质量 sheet 的延迟
--      分位，来源 usage_facts.latency_ms）。
--   ③ scope 枚举扩员（TEXT 列无 CHECK，靠 SSOT 注记约束）：
--      internal_person（scope_key = end_user_id，缺失回落 person_hash，
--      前缀 person: 区分）与 internal_model（scope_key = tenant_id、
--      raw_model_name = 出站模型名，业务流量按租户×模型的质量行）。
--      一并注明 provider 面三 scope 含全部流量类（探针也烧供应商钱），
--      internal 面三 scope 仅 business 流量（内部计费口径）。

ALTER TABLE report_snapshots
    ALTER COLUMN tenant_id TYPE text USING tenant_id::text;

ALTER TABLE report_snapshots
    ADD COLUMN IF NOT EXISTS credits_charged BIGINT NOT NULL DEFAULT 0;

ALTER TABLE report_snapshots
    ADD COLUMN IF NOT EXISTS latency_p50_ms BIGINT NOT NULL DEFAULT 0;

ALTER TABLE report_snapshots
    ADD COLUMN IF NOT EXISTS latency_p95_ms BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN report_snapshots.scope IS
    'daily_total | daily_by_provider | daily_by_model | internal_tenant | internal_person | internal_model; provider-facing scopes include all traffic classes, internal scopes are business traffic only';
COMMENT ON COLUMN report_snapshots.tenant_id IS
    'text tenant id (usage_facts.tenant_id); set for internal_tenant / internal_person / internal_model';
COMMENT ON COLUMN report_snapshots.credits_charged IS
    'internal billing credits summed from usage_facts.credits_charged (internal pricing caliber); money = credits * price_snapshot.cents_per_credit';
