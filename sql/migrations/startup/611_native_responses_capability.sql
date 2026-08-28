-- Migration 611: binding-scoped native OpenAI Responses capability.
-- Default-off: an absent row or supported=false never enables native forwarding.

BEGIN;

CREATE TABLE IF NOT EXISTS credential_model_capabilities (
    id BIGSERIAL PRIMARY KEY,
    credential_model_binding_id BIGINT NOT NULL
        REFERENCES credential_model_bindings(id) ON DELETE CASCADE,
    capability TEXT NOT NULL,
    supported BOOLEAN NOT NULL DEFAULT FALSE,
    last_tested_at TIMESTAMPTZ,
    evidence_json JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT credential_model_capabilities_capability_check
        CHECK (capability IN ('native_responses_nonstream')),
    CONSTRAINT credential_model_capabilities_binding_capability_key
        UNIQUE (credential_model_binding_id, capability)
);

CREATE INDEX IF NOT EXISTS idx_credential_model_capabilities_binding
    ON credential_model_capabilities (credential_model_binding_id)
    WHERE supported = TRUE;

COMMENT ON TABLE credential_model_capabilities IS
    'Verified capabilities scoped to one credential-model binding; missing rows are disabled.';
COMMENT ON COLUMN credential_model_capabilities.supported IS
    'Explicit opt-in. FALSE is the safe default until the capability is verified.';

COMMIT;
