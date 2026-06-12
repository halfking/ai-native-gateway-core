-- tier_2 xiaomi token-plan: pricing_plans + credential_model_bindings
-- Generated 2026-06-12 18:35 (after fixing ON CONFLICT issue)

BEGIN;

-- Close any prior token_plan rows for these credential+model combos
UPDATE pricing_plans SET effective_to = NOW() WHERE credential_id = 9 AND model_canonical_id = 3 AND plan_type = 'token_plan' AND effective_to IS NULL;
UPDATE pricing_plans SET effective_to = NOW() WHERE credential_id = 9 AND model_canonical_id = 5 AND plan_type = 'token_plan' AND effective_to IS NULL;
UPDATE pricing_plans SET effective_to = NOW() WHERE credential_id = 9 AND model_canonical_id = 7 AND plan_type = 'token_plan' AND effective_to IS NULL;
UPDATE pricing_plans SET effective_to = NOW() WHERE credential_id = 9 AND model_canonical_id = 8 AND plan_type = 'token_plan' AND effective_to IS NULL;
UPDATE pricing_plans SET effective_to = NOW() WHERE credential_id = 9 AND model_canonical_id = 9 AND plan_type = 'token_plan' AND effective_to IS NULL;

-- Insert token_plan rows
INSERT INTO pricing_plans (scope, provider_id, credential_id, model_canonical_id, plan_type, currency, plan_json, source, confidence, scraped_url) VALUES ('credential', 1, 9, 3, 'token_plan', 'USD', '{"input_per_1m": "10.0", "output_per_1m": "30.0", "tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate"}'::jsonb, 'scraped', 0.95, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan');
INSERT INTO pricing_plans (scope, provider_id, credential_id, model_canonical_id, plan_type, currency, plan_json, source, confidence, scraped_url) VALUES ('credential', 1, 9, 5, 'token_plan', 'USD', '{"input_per_1m": "15.0", "output_per_1m": "60.0", "tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate"}'::jsonb, 'scraped', 0.95, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan');
INSERT INTO pricing_plans (scope, provider_id, credential_id, model_canonical_id, plan_type, currency, plan_json, source, confidence, scraped_url) VALUES ('credential', 1, 9, 7, 'token_plan', 'USD', '{"input_per_1m": "2.0", "output_per_1m": "8.0", "tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate"}'::jsonb, 'scraped', 0.95, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan');
INSERT INTO pricing_plans (scope, provider_id, credential_id, model_canonical_id, plan_type, currency, plan_json, source, confidence, scraped_url) VALUES ('credential', 1, 9, 8, 'token_plan', 'USD', '{"input_per_1m": "1.1", "output_per_1m": "4.4", "tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate"}'::jsonb, 'scraped', 0.95, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan');
INSERT INTO pricing_plans (scope, provider_id, credential_id, model_canonical_id, plan_type, currency, plan_json, source, confidence, scraped_url) VALUES ('credential', 1, 9, 9, 'token_plan', 'USD', '{"input_per_1m": "1.1", "output_per_1m": "4.4", "tier": "Standard", "currency": "USD", "tier_meta": "Standard xiaomi token-plan rate"}'::jsonb, 'scraped', 0.95, 'https://platform.xiaomi.com/docs/xiaomi-mimo-tokenplan');

-- Update credential_model_bindings (snapshot) to billing_mode=token_plan
-- gpt-4-turbo (offer_id=34)
UPDATE credential_model_bindings SET unit_price_in_per_1m = 10.0, unit_price_out_per_1m = 30.0, currency = 'USD', billing_mode = 'token_plan', pricing_source = 'imported', pricing_updated_at = now(), plan_meta = '{"tier":"Standard","unit_price_in_per_1m":10.0,"unit_price_out_per_1m":30.0,"currency":"USD","note":"xiaomi token-plan tier Standard"}'::jsonb WHERE id = 34;
-- o1 (offer_id=37)
UPDATE credential_model_bindings SET unit_price_in_per_1m = 15.0, unit_price_out_per_1m = 60.0, currency = 'USD', billing_mode = 'token_plan', pricing_source = 'imported', pricing_updated_at = now(), plan_meta = '{"tier":"Standard","unit_price_in_per_1m":15.0,"unit_price_out_per_1m":60.0,"currency":"USD","note":"xiaomi token-plan tier Standard"}'::jsonb WHERE id = 37;
-- o3 (offer_id=45)
UPDATE credential_model_bindings SET unit_price_in_per_1m = 2.0, unit_price_out_per_1m = 8.0, currency = 'USD', billing_mode = 'token_plan', pricing_source = 'imported', pricing_updated_at = now(), plan_meta = '{"tier":"Standard","unit_price_in_per_1m":2.0,"unit_price_out_per_1m":8.0,"currency":"USD","note":"xiaomi token-plan tier Standard"}'::jsonb WHERE id = 45;
-- o3-mini (offer_id=46)
UPDATE credential_model_bindings SET unit_price_in_per_1m = 1.1, unit_price_out_per_1m = 4.4, currency = 'USD', billing_mode = 'token_plan', pricing_source = 'imported', pricing_updated_at = now(), plan_meta = '{"tier":"Standard","unit_price_in_per_1m":1.1,"unit_price_out_per_1m":4.4,"currency":"USD","note":"xiaomi token-plan tier Standard"}'::jsonb WHERE id = 46;
-- o4-mini (offer_id=47)
UPDATE credential_model_bindings SET unit_price_in_per_1m = 1.1, unit_price_out_per_1m = 4.4, currency = 'USD', billing_mode = 'token_plan', pricing_source = 'imported', pricing_updated_at = now(), plan_meta = '{"tier":"Standard","unit_price_in_per_1m":1.1,"unit_price_out_per_1m":4.4,"currency":"USD","note":"xiaomi token-plan tier Standard"}'::jsonb WHERE id = 47;

COMMIT;