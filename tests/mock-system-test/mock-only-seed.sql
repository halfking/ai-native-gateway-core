-- ====================================================================
-- mock-only seed: four fake vendors × three fake models, isolated IDs
-- ====================================================================
-- Scope guards (all enforced, not assumed):
--   * IDs live in the 95001+ band — disjoint from the legacy 9001/9010+
--     loadtest seeds so re-runs and cleanups can never collide.
--   * Provider codes, model names and credential labels all carry the
--     `mock-` prefix. The final scrub block in this file disables every
--     provider/credential that does NOT match, so the gateway's routable
--     set is provably mock-only (asserted again by the stack script).
--   * secret_ciphertext pairs with LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY
--     = AwoRGB8mLTQ7QklQV15lbHN6gYiPlp2kq7K5wMfO1dw (the pairing already
--     proven by sql/scripts/03-local-mock-credential.sql); plaintext is
--     the throwaway "sk-local-mock-not-a-real-key".
-- ====================================================================

BEGIN;

-- ── cleanup previous mock-only seed (idempotent) ────────────────────
DELETE FROM public.credential_model_bindings WHERE credential_id BETWEEN 95001 AND 95999;
DELETE FROM public.provider_models             WHERE provider_id BETWEEN 95001 AND 95999;
DELETE FROM public.credentials                 WHERE id BETWEEN 95001 AND 95999;
DELETE FROM public.providers                   WHERE id BETWEEN 95001 AND 95999;

-- ── four fake vendors (container DNS names inside the compose net) ──
INSERT INTO public.providers (
    id, tenant_id, code, display_name, kind, category, protocol,
    base_url, egress_profile, domestic, enabled, manual_disabled
) VALUES
    (95001, 'default', 'mock-vendor-alpha', 'Mock Vendor Alpha', 'cloud', 'self_host', 'openai-completions', 'http://mock-alpha:18101', 'direct', true,  true, false),
    (95002, 'default', 'mock-vendor-beta',  'Mock Vendor Beta',  'cloud', 'self_host', 'openai-completions', 'http://mock-beta:18101',  'direct', true,  true, false),
    (95003, 'default', 'mock-vendor-gamma', 'Mock Vendor Gamma', 'cloud', 'self_host', 'openai-completions', 'http://mock-gamma:18101', 'direct', true,  true, false),
    (95004, 'default', 'mock-vendor-delta', 'Mock Vendor Delta', 'cloud', 'self_host', 'openai-completions', 'http://mock-delta:18101', 'direct', true,  true, false);

-- ── every vendor serves the same three fake models ⇒ one model bucket
--    spans four credentials across four vendors (the "model bucket"
--    under test: drain one vendor, the bucket re-routes) ──────────────
INSERT INTO public.provider_models (
    id, provider_id, tenant_id, raw_model_name, canonical_raw_name,
    standardized_name, outbound_model_name, available
) SELECT
    95010 + (p.id - 95001) * 3 + m.ord,
    p.id, 'default',
    m.name, m.name, m.name, m.name, true
FROM public.providers p
CROSS JOIN (VALUES
    (0, 'mock-model-fast'),
    (1, 'mock-model-long'),
    (2, 'mock-model-stream')
) AS m(ord, name)
WHERE p.id BETWEEN 95001 AND 95004;

-- ── one credential per vendor, roomy limits for 200-client pressure ──
INSERT INTO public.credentials (
    id, provider_id, tenant_id, label, secret_ciphertext, secret_kid,
    trust_level, status, lifecycle_status, availability_state,
    quota_state, health_status, circuit_state, manual_disabled,
    fp_slot_limit, concurrency_limit
) VALUES
    (95001, 95001, 'default', 'mock-vendor-alpha-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready', 'ok', 'unknown', 'closed', false, 200, 400),
    (95002, 95002, 'default', 'mock-vendor-beta-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready', 'ok', 'unknown', 'closed', false, 200, 400),
    (95003, 95003, 'default', 'mock-vendor-gamma-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready', 'ok', 'unknown', 'closed', false, 200, 400),
    (95004, 95004, 'default', 'mock-vendor-delta-key',
     E'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
     'legacy', 'trusted', 'active', 'active', 'ready', 'ok', 'unknown', 'closed', false, 200, 400);

-- ── credential ↔ model bindings (4 vendors × 3 models) ──────────────
INSERT INTO public.credential_model_bindings (
    id, credential_id, provider_model_id, routing_tier, weight,
    manual_priority, available
) SELECT
    95010 + (pm.provider_id - 95001) * 3
        + (pm.id - 95010 - (pm.provider_id - 95001) * 3),
    pm.provider_id, pm.id, 2, 100, 99, true
FROM public.provider_models pm
WHERE pm.provider_id BETWEEN 95001 AND 95004;

-- ── canonical registration: the context-budget gate derives each
--    model bucket's context window from models_canonical; without a
--    canonical row the budget is (0,0) and every request is rejected
--    with context_budget_exceeded. Register the three mock models with
--    a generous window and link every provider_models row to it. ─────
INSERT INTO public.models_canonical (
    canonical_name, display_name, family, modality, context_window,
    status, source
)
SELECT m.name, m.disp, 'mock', 'text', 131072, 'active', 'db'
FROM (VALUES
    ('mock-model-fast',   'Mock Fast'),
    ('mock-model-long',   'Mock Long'),
    ('mock-model-stream', 'Mock Stream')
) AS m(name, disp)
WHERE NOT EXISTS (
    SELECT 1 FROM public.models_canonical mc WHERE mc.canonical_name = m.name
);

UPDATE public.provider_models pm
SET canonical_id = mc.id
FROM public.models_canonical mc
WHERE pm.provider_id BETWEEN 95001 AND 95004
  AND pm.raw_model_name = mc.canonical_name
  AND pm.canonical_id IS DISTINCT FROM mc.id;

COMMIT;

-- ====================================================================
-- ★ mock-only scrub: disable every non-mock provider and credential.
--   This is the auditable guarantee that the data plane and every
--   probe worker can only ever reach mock containers — real vendor
--   rows from the base seed stay in the DB but are disabled by policy.
--   The stack script re-asserts this after boot (count must be 0).
-- ====================================================================
UPDATE public.providers
SET enabled = false, manual_disabled = true, updated_at = now()
WHERE code NOT LIKE 'mock-vendor-%';

UPDATE public.credentials
SET status = 'disabled', manual_disabled = true, updated_at = now()
WHERE provider_id NOT IN (SELECT id FROM public.providers WHERE code LIKE 'mock-vendor-%');

-- ── verification (visible in pg init logs and the stack gate) ───────
SELECT 'mock_providers'      AS check, count(*)::text AS value FROM public.providers  WHERE code LIKE 'mock-vendor-%'
UNION ALL
SELECT 'mock_credentials'    AS check, count(*)::text AS value FROM public.credentials WHERE id BETWEEN 95001 AND 95004
UNION ALL
SELECT 'mock_bindings'       AS check, count(*)::text AS value FROM public.credential_model_bindings WHERE credential_id BETWEEN 95001 AND 95004
UNION ALL
SELECT 'mock_routable'       AS check, count(*)::text AS value FROM public.v_routable_credential_models WHERE credential_id BETWEEN 95001 AND 95004 AND is_routable
UNION ALL
SELECT 'nonmock_providers_disabled' AS check, count(*)::text AS value FROM public.providers  WHERE code NOT LIKE 'mock-vendor-%' AND (enabled = true OR COALESCE(manual_disabled,false) = false)
UNION ALL
SELECT 'nonmock_credentials_active' AS check, count(*)::text AS value FROM public.credentials WHERE provider_id NOT IN (SELECT id FROM public.providers WHERE code LIKE 'mock-vendor-%') AND (status <> 'disabled' OR COALESCE(manual_disabled,false) = false);
