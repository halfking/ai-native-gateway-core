-- 082-ursm-key-migration-state-machine.sql
-- Durable checkpoint promotion evidence and fenced-cleanup claim audit fields.
-- This migration is additive: it does not modify an immutable preflight ledger
-- snapshot or grant any k2 copy/cleanup execution authority.

BEGIN;

ALTER TABLE ursm_key_migration_entries
    ADD COLUMN IF NOT EXISTS cleanup_claim_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cleanup_claim_epoch BIGINT,
    ADD COLUMN IF NOT EXISTS cleanup_claimed_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS ursm_key_migration_transitions (
    ledger_id       TEXT NOT NULL REFERENCES ursm_key_migration_runs(ledger_id) ON DELETE CASCADE,
    cutover_epoch   BIGINT NOT NULL,
    from_checkpoint TEXT NOT NULL,
    to_checkpoint   TEXT NOT NULL,
    key_schema_mode TEXT NOT NULL,
    actor           TEXT NOT NULL,
    approved_by     TEXT NOT NULL,
    evidence_sha256 TEXT NOT NULL,
    evidence_ref    TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (ledger_id, cutover_epoch),
    CONSTRAINT ursm_key_migration_transitions_checkpoint_chk CHECK (
        from_checkpoint IN ('preflight','copy','coverage','dual','observe','cleanup')
        AND to_checkpoint IN ('copy','coverage','dual','observe','cleanup','done','rollback')
    ),
    CONSTRAINT ursm_key_migration_transitions_mode_chk CHECK (
        key_schema_mode IN ('legacy','dual','canonical')
    ),
    CONSTRAINT ursm_key_migration_transitions_evidence_sha256_chk CHECK (
        evidence_sha256 ~ '^[0-9a-f]{64}$'
    )
);

CREATE INDEX IF NOT EXISTS ursm_key_migration_entries_fenced_claim_idx
    ON ursm_key_migration_entries (ledger_id, state, cleanup_claimed_at);

COMMIT;
