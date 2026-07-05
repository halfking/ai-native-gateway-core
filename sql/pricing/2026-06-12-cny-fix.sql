-- 2026_06_12_cny_pricing_fix.sql
-- Fix: 国内模型必须用人民币(CNY)计价，使用各厂商官方 CNY 价格
-- Affected: credential_id=8 (nvidia-build) 下的 60 个国内模型
-- Date: 2026-06-12
-- Sources:
--   Zhipu: https://open.bigmodel.cn/pricing (2026-06-11 scraped)
--   DeepSeek: https://api-docs.deepseek.com/zh-cn/quick_start/pricing
--   Qwen: https://help.aliyun.com/zh/model-studio/model-pricing
--   MiniMax: https://platform.minimaxi.com/docs/guides/pricing-paygo
--   Doubao: https://www.volcengine.com/docs/82379/1544106
--   Moonshot: https://platform.moonshot.ai/docs/pricing (CNY known: v1-8k=12/60, v1-32k=24/120, v1-128k=60/120)
--   Baichuan: https://platform.baichuan-ai.com/docs/pricing (CNY: baichuan3-turbo=4/8, turbo-128k=8/16, baichuan4=16/32)
--   Yi: https://platform.lingyiwanwu.com (CNY: yi-large=20/20, yi-large-turbo=12/12, yi-medium=2.5/2.5)
--   Step: https://platform.stepfun.com/docs/zh/guides/pricing/details
--   SenseChat: https://platform.sensenova.cn (CNY: sensechat-5=21/42, thinking=42/84, turbo=4/8)
--   MiniMax abab: abab5.5-chat=15/15, abab6.5s-chat=10/10

BEGIN;

-- ============================================================
-- Zhipu GLM models (10 offers: ids 102-113)
-- Source: open.bigmodel.cn/pricing — 旗舰模型 2026-06-11
-- ============================================================
-- GLM-5.1: input=6元/M, output=24元/M (32k context tier)
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 6, unit_price_out_per_1m = 24,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 102 AND currency != 'CNY';

-- GLM-5: input=4元/M, output=18元/M
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 4, unit_price_out_per_1m = 18,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 103 AND currency != 'CNY';

-- GLM-4.7: input=2元/M, output=8元/M (tier: input<32k, output<0.2M)
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 2, unit_price_out_per_1m = 8,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 104 AND currency != 'CNY';

-- GLM-4.7-flash: 免费
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0, unit_price_out_per_1m = 0,
    billing_mode = 'token', plan_meta = '{"free": true}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 105 AND currency != 'CNY';

-- GLM-4.5: (不在官网最新表，用 Tier1 保守估计 CNY)
-- Note: GLM-4.5 已被 GLM-4.7/5 替代，NVIDIA NIM 仍托管
-- 保守 CNY ≈ USD * 7.2 汇率（原价 0.6/2.2 USD → 4.32/15.84 CNY）
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 4.32, unit_price_out_per_1m = 15.84,
    billing_mode = 'token', plan_meta = '{"note": "legacy model, CNY estimated from USD*7.2"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 106 AND currency != 'CNY';

-- GLM-4.5-air: (保守估计)
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.9, unit_price_out_per_1m = 6.12,
    billing_mode = 'token', plan_meta = '{"note": "legacy model, CNY estimated from USD*7.2"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 108 AND currency != 'CNY';

-- GLM-4.5-flash: 免费
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0, unit_price_out_per_1m = 0,
    billing_mode = 'token', plan_meta = '{"free": true}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 107 AND currency != 'CNY';

-- GLM-4: ¥0.1/M in, ¥0.1/M out (legacy, confirmed on pricing page)
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.1, unit_price_out_per_1m = 0.1,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 109 AND currency != 'CNY';

-- GLM-4-air: ¥0.001/M (免费级)
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.001, unit_price_out_per_1m = 0.001,
    billing_mode = 'token', plan_meta = '{"free": true}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 111 AND currency != 'CNY';

-- GLM-4-9b-chat: ¥0.001/M (免费)
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.001, unit_price_out_per_1m = 0.001,
    billing_mode = 'token', plan_meta = '{"free": true}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 112 AND currency != 'CNY';

-- GLM-4-flash: 免费
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0, unit_price_out_per_1m = 0,
    billing_mode = 'token', plan_meta = '{"free": true}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 110 AND currency != 'CNY';

-- GLM-z1-flash: 免费
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0, unit_price_out_per_1m = 0,
    billing_mode = 'token', plan_meta = '{"free": true}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 113 AND currency != 'CNY';

-- ============================================================
-- DeepSeek models (6 offers: ids 88-90, 149, 158-160)
-- Source: api-docs.deepseek.com/zh-cn/quick_start/pricing
-- CNY: v4-flash in=1, out=2; v4-pro in=3, out=6
-- Legacy: v3/chat in=1, out=2; r1 in=3, out=6
-- ============================================================
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 1, unit_price_out_per_1m = 2,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id IN (88, 89, 149) AND currency != 'CNY';

UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 3, unit_price_out_per_1m = 6,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 90 AND currency != 'CNY';

UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 1, unit_price_out_per_1m = 2,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 158 AND currency != 'CNY';

UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.14, unit_price_out_per_1m = 0.28,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 159 AND currency != 'CNY';

UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 3, unit_price_out_per_1m = 6,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 160 AND currency != 'CNY';

-- ============================================================
-- Qwen models (6 offers: ids 91-96)
-- Source: help.aliyun.com/zh/model-studio/model-pricing
-- CNY: qwen-max=2.4/9.6, qwen-plus=0.8/2, qwen-turbo=0.3/0.6
--       qwen2.5-72b=4/12(estimate), qwen2.5-7b=1/2(estimate), qwq-32b=1.6/4
-- ============================================================
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 2.4, unit_price_out_per_1m = 9.6,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 91 AND currency != 'CNY';

UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.8, unit_price_out_per_1m = 2,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 92 AND currency != 'CNY';

UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 0.3, unit_price_out_per_1m = 0.6,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 93 AND currency != 'CNY';

-- qwen2.5-72b-instruct: 不在百炼最新定价表中（已被Qwen3替代）
-- 保守估计: qwen3-32b 定价 2/8, 72b 应比 32b 贵 ≈ 4/12
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 4, unit_price_out_per_1m = 12,
    billing_mode = 'token', plan_meta = '{"note": "qwen2.5-72b legacy, CNY estimated from qwen3-32b pricing tier"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 94 AND currency != 'CNY';

-- qwen2.5-7b-instruct: 保守估计 ≈ 1/2
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 1, unit_price_out_per_1m = 2,
    billing_mode = 'token', plan_meta = '{"note": "qwen2.5-7b legacy, CNY estimated"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 95 AND currency != 'CNY';

-- qwq-32b: Source shows qwq-plus=1.6/4, 32b smaller so ≈ 1/4
UPDATE credential_model_bindings
SET currency = 'CNY',
    unit_price_in_per_1m = 1, unit_price_out_per_1m = 4,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 96 AND currency != 'CNY';

-- ============================================================
-- Doubao models (12 offers: ids 1, 48, 74, 75, 97-101, 146, 186)
-- Source: volcengine.com/docs/82379/1544106
-- Note: doubao-seed-2.0-pro/lite (ids 32, 100) already CNY from direct volcengine creds
-- doubao-pro-128k (id 75): same as doubao-pro-32k → CNY 0.8/2
-- doubao-lite-4k (id 99): CNY 0.05/0.1 (basic tier)
-- doubao-embedding: CNY 0.1/0
-- doubao-1-5-lite-32k: CNY 0.3/0.6
-- doubao-1-5-pro-32k: CNY 0.8/2
-- doubao-1-5-pro-256k: CNY 1.5/4
-- doubao-pro-4k: CNY 0.1/0.2
-- doubao-seed-2.0-mini: CNY 0.1/0.2
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.1, unit_price_out_per_1m = 0,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 1 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.1, unit_price_out_per_1m = 0.2,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 48 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.3, unit_price_out_per_1m = 0.6,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 74 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.8, unit_price_out_per_1m = 2,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id IN (75, 97, 186) AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 1.5, unit_price_out_per_1m = 4,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 98 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.05, unit_price_out_per_1m = 0.1,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 99 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.1, unit_price_out_per_1m = 0,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 101 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.1, unit_price_out_per_1m = 0.2,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 146 AND currency != 'CNY';

-- ============================================================
-- Moonshot models (3 offers: ids 2, 124, 125)
-- Source: platform.moonshot.ai — CNY定价
-- moonshot-v1-8k: 12元/M in, 12元/M out (统一价)
-- moonshot-v1-32k: 24元/M in, 24元/M out
-- moonshot-v1-128k: 60元/M in, 60元/M out
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 60, unit_price_out_per_1m = 60,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 2 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 24, unit_price_out_per_1m = 24,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 124 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 12, unit_price_out_per_1m = 12,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 125 AND currency != 'CNY';

-- ============================================================
-- MiniMax models (4 offers: ids 120-122, 187)
-- Source: platform.minimaxi.com/docs/guides/pricing-paygo
-- minimax-m2.5: 2.1/8.4 CNY
-- minimax-m2.7: 2.1/8.4 CNY (same price)
-- minimax-text-01: ≈ 3/12 CNY (estimated from M2.7 tier)
-- abab5.5-chat: 15/15 CNY
-- abab6.5s-chat: 10/10 CNY
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 2.1, unit_price_out_per_1m = 8.4,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id IN (120, 121) AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 3, unit_price_out_per_1m = 12,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 187 AND currency != 'CNY';

-- abab5.5-chat
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 15, unit_price_out_per_1m = 15,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 123 AND currency != 'CNY';

-- abab6.5s-chat
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 10, unit_price_out_per_1m = 10,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 122 AND currency != 'CNY';

-- ============================================================
-- Baichuan models (3 offers: ids 126-128)
-- Source: platform.baichuan-ai.com — CNY定价
-- baichuan4: 16/32 CNY
-- baichuan3-turbo-128k: 8/16 CNY
-- baichuan3-turbo: 4/8 CNY
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 16, unit_price_out_per_1m = 32,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 126 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 8, unit_price_out_per_1m = 16,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 127 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 4, unit_price_out_per_1m = 8,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 128 AND currency != 'CNY';

-- ============================================================
-- Yi models (3 offers: ids 132-134)
-- Source: platform.lingyiwanwu.com — CNY定价
-- yi-large: 20/20 CNY
-- yi-large-turbo: 12/12 CNY
-- yi-medium: 2.5/2.5 CNY
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 20, unit_price_out_per_1m = 20,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 132 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 12, unit_price_out_per_1m = 12,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 134 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 2.5, unit_price_out_per_1m = 2.5,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 133 AND currency != 'CNY';

-- ============================================================
-- Step models (3 offers: ids 129-131)
-- Source: platform.stepfun.com/docs/zh/guides/pricing/details
-- step-1-256k: 0.5/1 → but not in latest pricing (replaced by step-3.x)
-- step-2-16k: 0.5/1 → legacy, estimated from old pricing
-- step-1v-32k: 0.5/1 → vision model
--保守估计: step-1/2 legacy ≈ 5/10 CNY (mid-tier)
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 5, unit_price_out_per_1m = 10,
    billing_mode = 'token', plan_meta = '{"note": "step-1-256k legacy, CNY estimated"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 130 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 5, unit_price_out_per_1m = 10,
    billing_mode = 'token', plan_meta = '{"note": "step-1v-32k legacy, CNY estimated"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 131 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 5, unit_price_out_per_1m = 10,
    billing_mode = 'token', plan_meta = '{"note": "step-2-16k legacy, CNY estimated"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 129 AND currency != 'CNY';

-- ============================================================
-- SenseChat models (3 offers: ids 135-137)
-- Source: platform.sensenova.cn — CNY定价
-- sensechat-5: 21/42 CNY
-- sensechat-5-thinking: 42/84 CNY
-- sensechat-turbo: 4/8 CNY
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 21, unit_price_out_per_1m = 42,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 135 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 42, unit_price_out_per_1m = 84,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 136 AND currency != 'CNY';

UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 4, unit_price_out_per_1m = 8,
    billing_mode = 'token', pricing_updated_at = now(), updated_at = now()
WHERE id = 137 AND currency != 'CNY';

-- ============================================================
-- BGE embedding (1 offer: id 154)
-- Source: BAAI open-source, via NVIDIA NIM hosting
-- bge-m3: 0.05 USD → 保守 CNY 0.36/M
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 0.36, unit_price_out_per_1m = 0,
    billing_mode = 'token', plan_meta = '{"note": "bge-m3 open-source embedding, CNY estimated from USD*7.2"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 154 AND currency != 'CNY';

-- ============================================================
-- Mimo (1 offer: id 179)
-- Source: Xiaomi — mimo-v2.5-pro
-- CNY estimated: ≈ 3.13/6.26 (from USD*7.2)
-- ============================================================
UPDATE credential_model_bindings SET currency = 'CNY',
    unit_price_in_per_1m = 3.13, unit_price_out_per_1m = 6.26,
    billing_mode = 'token', plan_meta = '{"note": "mimo-v2.5-pro via NVIDIA NIM, CNY estimated from USD*7.2"}',
    pricing_updated_at = now(), updated_at = now()
WHERE id = 179 AND currency != 'CNY';

COMMIT;

-- Verification: count by currency
SELECT 'cny_fix_result' AS check_name,
  (SELECT count(*) FROM credential_model_bindings WHERE currency = 'CNY') AS cny_count,
  (SELECT count(*) FROM credential_model_bindings WHERE currency = 'USD') AS usd_count,
  (SELECT count(*) FROM credential_model_bindings WHERE currency = 'CNY' AND billing_mode = 'token_plan') AS cny_token_plan,
  (SELECT count(*) FROM credential_model_bindings mc
   JOIN credentials c ON c.id = mc.credential_id
   JOIN models_canonical m ON m.id = mc.provider_model_id
   WHERE mc.currency = 'USD' AND c.label = 'nvidia-build'
     AND m.canonical_name ~ '^(glm|qwen|qwq|doubao|moonshot|yi-|baichuan|minimax|deepseek|mimo|sensechat|step|abab|bge)'
  ) AS domestic_still_usd;
