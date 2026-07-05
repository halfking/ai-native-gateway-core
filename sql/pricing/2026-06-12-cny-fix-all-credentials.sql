-- 2026_06_12_cny_fix_all_credentials.sql
-- Fix: ALL domestic credential offers must use CNY currency
-- Affected credentials:
--   6  minimax-prod-1 (5 offers: Claude/GPT via MiniMax proxy)
--   7  roocode/zhipu  (5 offers: Llama via Zhipu proxy)
--   9  xiaomi-token-plan (9 offers: OpenAI models via xiaomi)
--   11 demo-tokenplan (1 offer: nemoguard still USD)
--   12 hzx-normal/volcano-normal (2 offers: nemotron still USD)
--
-- Rule: If credential belongs to a domestic provider → CNY
--   minimax, zhipu, xiaomi, volcano-tokenplan, volcano-normal = domestic
--   nvidia, evol, vapeur, scnet = international (USD ok for non-Chinese models)
--
-- Date: 2026-06-12

BEGIN;

-- ============================================================
-- 1. minimax-prod-1 (cred_id=6): 5 offers
--    MiniMax is domestic Chinese provider → ALL models CNY
--    These are proxy models (Claude/GPT via MiniMax routing)
--    MiniMax charges in CNY internally, so the price *displayed* should be CNY
-- ============================================================
UPDATE credential_model_bindings SET
  currency = 'CNY', billing_mode = 'token',
  pricing_updated_at = now(), updated_at = now()
WHERE credential_id = 6 AND currency != 'CNY';

-- Fix billing_mode from per_token to token
UPDATE credential_model_bindings SET
  billing_mode = 'token', updated_at = now()
WHERE credential_id = 6 AND billing_mode = 'per_token';

-- ============================================================
-- 2. roocode/zhipu (cred_id=7): 5 offers
--    Zhipu is domestic Chinese provider → ALL models CNY
--    These are proxy models (Llama via Zhipu routing)
-- ============================================================
UPDATE credential_model_bindings SET
  currency = 'CNY', billing_mode = 'token',
  pricing_updated_at = now(), updated_at = now()
WHERE credential_id = 7 AND currency != 'CNY';

UPDATE credential_model_bindings SET
  billing_mode = 'token', updated_at = now()
WHERE credential_id = 7 AND billing_mode = 'per_token';

-- ============================================================
-- 3. xiaomi-token-plan (cred_id=9): 9 offers
--    Xiaomi is domestic → ALL models CNY
--    billing_mode should be token_plan (subscription credential)
--    Price: xiaomi token_plan 定价 (per xiaomi official)
--    gpt-4-turbo=10/30, gpt-4o=0.14/0.28, gpt-4o-mini=0.14/0.28,
--    gpt-3.5-turbo=0.14/0.28, o1=15/60, o1-mini=0.435/0.87,
--    o3=2/8, o3-mini=1.1/4.4, o4-mini=1.1/4.4
-- ============================================================
UPDATE credential_model_bindings SET
  currency = 'CNY', billing_mode = 'token_plan',
  plan_meta = COALESCE(plan_meta, '{"note": "xiaomi token-plan subscription"}')::jsonb,
  pricing_updated_at = now(), updated_at = now()
WHERE credential_id = 9 AND billing_mode != 'token_plan';

UPDATE credential_model_bindings SET
  currency = 'CNY',
  pricing_updated_at = now(), updated_at = now()
WHERE credential_id = 9 AND currency != 'CNY';

-- ============================================================
-- 4. demo-tokenplan (cred_id=11): 1 offer (id=42 nemoguard)
--    Volcano tokenplan is domestic → CNY
-- ============================================================
UPDATE credential_model_bindings SET
  currency = 'CNY', billing_mode = 'token_plan',
  plan_meta = COALESCE(plan_meta, '{"note": "volcano token-plan (demo credential)"}')::jsonb,
  pricing_updated_at = now(), updated_at = now()
WHERE id = 42 AND currency != 'CNY';

-- Also fix the other demo-tokenplan offers that are still per_token
UPDATE credential_model_bindings SET
  billing_mode = 'token_plan', updated_at = now()
WHERE credential_id = 11 AND billing_mode = 'per_token';

-- ============================================================
-- 5. hzx-normal/volcano-normal (cred_id=12): 2 offers in USD
--    Volcano normal is domestic → ALL models CNY
--    id=61 nemotron-ultra: 2→6 USD → CNY
--    id=64 nemotron-super: 0.5→1.5 USD → CNY
-- ============================================================
UPDATE credential_model_bindings SET
  currency = 'CNY', billing_mode = 'token',
  pricing_updated_at = now(), updated_at = now()
WHERE credential_id = 12 AND currency != 'CNY';

UPDATE credential_model_bindings SET
  billing_mode = 'token', updated_at = now()
WHERE credential_id = 12 AND billing_mode = 'per_token';

-- ============================================================
-- 6. Global backfill: per_token → token (safety net, excludes token_plan/code_plan)
-- ============================================================
UPDATE credential_model_bindings SET billing_mode = 'token', updated_at = now()
WHERE billing_mode = 'per_token' AND billing_mode NOT IN ('token_plan', 'code_plan');

COMMIT;

-- Verification
SELECT 'cny_all_fix' AS check_name,
  (SELECT count(*) FROM credential_model_bindings WHERE currency = 'CNY') AS cny_total,
  (SELECT count(*) FROM credential_model_bindings WHERE currency = 'USD') AS usd_total,
  (SELECT count(*) FROM credential_model_bindings WHERE billing_mode = 'per_token') AS per_token_remaining,
  (SELECT count(*) FROM credential_model_bindings WHERE billing_mode = 'token_plan') AS token_plan_total;
