-- Migration 292: unavailable_recover_at column
ALTER TABLE credential_model_bindings ADD COLUMN IF NOT EXISTS unavailable_recover_at TIMESTAMPTZ;
ALTER TABLE model_offers ADD COLUMN IF NOT EXISTS unavailable_recover_at TIMESTAMPTZ;
