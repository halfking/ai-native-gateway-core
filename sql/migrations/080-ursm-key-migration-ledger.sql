-- 080-ursm-key-migration-ledger.sql
-- Durable, audit-friendly ledger for the URSM k2 key migration
-- (docs/03-design/02-feature-design/会话优化v4/14-URSM Redis delimiter-safe
-- key兼容迁移冻结决策.md). One run records the immutable owner / ledger /
-- checkpoint / checksum identity; entries store one row per exact source
-- Redis key classified during preflight. Cleanup uses these rows to find
-- and verify exact source keys + field checksums; copy uses them to drive
-- the generation/field-CAS Lua script. Ambiguous and excluded rows are
-- kept here for review so the operator can decide them out-of-band
-- (doc 14 §4 forbids guessing tenant/model from a key).
--
-- No production data is touched by this migration. Tables are created with
-- IF NOT EXISTS, and the runtime ensure function in db/db.go mirrors this
-- DDL idempotently so gateway startup and the SQL runner can never drift.

BEGIN;

CREATE TABLE IF NOT EXISTS ursm_key_migration_runs (
    ledger_id          TEXT PRIMARY KEY,
    owner              TEXT NOT NULL,
    key_schema_mode    TEXT NOT NULL,
    preflight_checksum TEXT NOT NULL,
    preflight_total    INT  NOT NULL DEFAULT 0,
    preflight_migratable INT NOT NULL DEFAULT 0,
    preflight_canonical_present INT NOT NULL DEFAULT 0,
    preflight_ambiguous INT NOT NULL DEFAULT 0,
    preflight_excluded INT NOT NULL DEFAULT 0,
    checkpoint         TEXT NOT NULL,
    cutover_epoch      BIGINT NOT NULL DEFAULT 0,
    started_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at        TIMESTAMPTZ,
    CONSTRAINT ursm_key_migration_runs_checkpoint_chk CHECK (
        checkpoint IN ('preflight','copy','coverage','observe','cleanup','rollback','done')
    ),
    CONSTRAINT ursm_key_migration_runs_mode_chk CHECK (
        key_schema_mode IN ('legacy','dual','canonical')
    )
);

CREATE TABLE IF NOT EXISTS ursm_key_migration_entries (
    ledger_id        TEXT NOT NULL REFERENCES ursm_key_migration_runs(ledger_id) ON DELETE CASCADE,
    source_key       TEXT NOT NULL,
    target_key       TEXT NOT NULL DEFAULT '',
    classification   TEXT NOT NULL,
    schema_origin    TEXT NOT NULL DEFAULT '',
    key_type         TEXT NOT NULL DEFAULT '',
    pttl_ms          BIGINT NOT NULL DEFAULT -2,
    generation       BIGINT NOT NULL DEFAULT 0,
    field_checksum   TEXT NOT NULL DEFAULT '',
    tuple_tenant     TEXT NOT NULL DEFAULT '',
    tuple_credential BIGINT NOT NULL DEFAULT 0,
    tuple_raw_model  TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'classified',
    copied_at        TIMESTAMPTZ,
    copied_pttl_ms   BIGINT,
    cleaned_at       TIMESTAMPTZ,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (ledger_id, source_key),
    CONSTRAINT ursm_key_migration_entries_class_chk CHECK (
        classification IN ('migratable','canonical_present','ambiguous','excluded_non_authoritative')
    ),
    CONSTRAINT ursm_key_migration_entries_schema_chk CHECK (
        schema_origin IN ('','legacy','k2')
    ),
    CONSTRAINT ursm_key_migration_entries_state_chk CHECK (
        state IN ('classified','copied','cleaned','rolled_back','conflict','expired','fenced')
    )
);

CREATE INDEX IF NOT EXISTS ursm_key_migration_entries_state_idx
    ON ursm_key_migration_entries (ledger_id, state);
CREATE INDEX IF NOT EXISTS ursm_key_migration_entries_class_idx
    ON ursm_key_migration_entries (ledger_id, classification);

COMMIT;