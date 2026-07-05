-- Auto-generated from 2026-06-12-credentials-with-plan-type.csv
-- Updates credential_model_bindings.plan_meta JSONB column

BEGIN;

UPDATE credential_model_bindings SET plan_meta = '{"input_per_1m": "0.14", "output_per_1m": "0.28", "cache_hit_per_1m": "0.0028", "currency": "USD"}'::jsonb, pricing_source = 'imported' WHERE id = 17;
UPDATE credential_model_bindings SET plan_meta = '{"input_per_1m": "0.435", "output_per_1m": "0.87", "cache_hit_per_1m": "0.003625", "currency": "USD"}'::jsonb, pricing_source = 'imported' WHERE id = 16;
UPDATE credential_model_bindings SET plan_meta = '{"input_per_1m": "0.30", "output_per_1m": "1.20", "cache_read_per_1m": "0.06", "cache_write_per_1m": "0.375"}'::jsonb, pricing_source = 'imported' WHERE id = 18;
UPDATE credential_model_bindings SET plan_meta = '{"input_per_1m": "0.30", "output_per_1m": "1.20", "cache_read_per_1m": "0.06", "permanent_50_percent_off": true}'::jsonb, pricing_source = 'imported' WHERE id = 24;
UPDATE credential_model_bindings SET plan_meta = '{"input_per_1m": "0.30", "output_per_1m": "1.20"}'::jsonb, pricing_source = 'imported' WHERE id = 23;
UPDATE credential_model_bindings SET plan_meta = '{"monthly_cny": "99", "validity_days": "30", "tier": "Standard", "input_per_1m": "0.14", "output_per_1m": "0.28"}'::jsonb, pricing_source = 'imported' WHERE id = 36;
UPDATE credential_model_bindings SET plan_meta = '{"monthly_cny": "99", "validity_days": "30", "tier": "Standard", "input_per_1m": "0.14", "output_per_1m": "0.28"}'::jsonb, pricing_source = 'imported' WHERE id = 33;
UPDATE credential_model_bindings SET plan_meta = '{"monthly_cny": "99", "validity_days": "30", "tier": "Standard", "input_per_1m": "0.14", "output_per_1m": "0.28", "modality": "multimodal"}'::jsonb, pricing_source = 'imported' WHERE id = 22;
UPDATE credential_model_bindings SET plan_meta = '{"modality": "embedding", "unit": "per_image", "note": "NEEDS_REVIEW"}'::jsonb, pricing_source = 'imported' WHERE id = 61;
UPDATE credential_model_bindings SET plan_meta = '{"custom_endpoint": true, "note": "NEEDS_REVIEW"}'::jsonb, pricing_source = 'imported' WHERE id = 64;
UPDATE credential_model_bindings SET plan_meta = '{"modality": "embedding", "unit": "per_image", "note": "NEEDS_REVIEW"}'::jsonb, pricing_source = 'imported' WHERE id = 42;

COMMIT;
