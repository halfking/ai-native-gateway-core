-- Migration 041: System settings for fingerprint slot control.
--
-- Five new platform-level settings under the `llmgw_` prefix:
--
--   1. llmgw_fp_slot_enabled (bool, default true)
--      Master switch. When false, all credentials get
--      fp_slot_limit = concurrency_limit (effectively no slot
--      throttling).
--
--   2. llmgw_fp_slot_max_per_credential (int, default 100)
--      Hard upper bound on fp_slot_limit at the per-credential level.
--
--   3. llmgw_fp_slot_default_ratio (numeric, default 0.25 = 1/4)
--      When fp_slot_limit is auto-derived from concurrency_limit.
--
--   4. llmgw_client_fingerprint_ttl_days (int, default 30)
--      After this many days without any traffic, the slot is released.
--
--   5. llmgw_fp_slot_max_total_clients (int, default 10000)
--      Global cap on distinct client fingerprints. LRU recycle when reached.

INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at) VALUES
    ('llmgw_fp_slot_enabled',                  'true'::jsonb,   'boolean',  'platform', 'fingerprint_slots', now()),
    ('llmgw_fp_slot_max_per_credential',       '100'::jsonb,    'integer',  'platform', 'fingerprint_slots', now()),
    ('llmgw_fp_slot_default_ratio',            '0.25'::jsonb,   'number',   'platform', 'fingerprint_slots', now()),
    ('llmgw_client_fingerprint_ttl_days',     '30'::jsonb,     'integer',  'platform', 'fingerprint_slots', now()),
    ('llmgw_fp_slot_max_total_clients',        '10000'::jsonb,  'integer',  'platform', 'fingerprint_slots', now())
ON CONFLICT (key) DO NOTHING;

CREATE OR REPLACE VIEW v_fp_slot_policy AS
SELECT
    COALESCE((SELECT (value #>> '{}')::BOOLEAN
              FROM settings_kv WHERE key = 'llmgw_fp_slot_enabled'), TRUE) AS enabled,
    COALESCE((SELECT (value #>> '{}')::INTEGER
              FROM settings_kv WHERE key = 'llmgw_fp_slot_max_per_credential'), 100) AS max_per_credential,
    COALESCE((SELECT (value #>> '{}')::NUMERIC
              FROM settings_kv WHERE key = 'llmgw_fp_slot_default_ratio'), 0.25) AS default_ratio,
    COALESCE((SELECT (value #>> '{}')::INTEGER
              FROM settings_kv WHERE key = 'llmgw_client_fingerprint_ttl_days'), 30) AS client_ttl_days,
    COALESCE((SELECT (value #>> '{}')::INTEGER
              FROM settings_kv WHERE key = 'llmgw_fp_slot_max_total_clients'), 10000) AS max_total_clients;

COMMENT ON VIEW v_fp_slot_policy IS
'Active fingerprint-slot policy derived from settings_kv. Used by admin UI and the credentialfpslot manager at boot.';