package migration

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// RunRecord is the durable identity of one migration attempt. A second run
// with the same ledger_id is rejected because ledger IDs are non-reusable
// (doc 14 §3); only the run owner may update the live row.
type RunRecord struct {
	LedgerID          string
	Owner             string
	KeySchemaMode     store.KeySchemaMode
	PreflightChecksum string
	Checkpoint        Checkpoint
	RollbackDeadline  string
	Total             int
	Migratable        int
	CanonicalPresent  int
	Ambiguous         int
	Excluded          int
}

// PGStore owns the durable migration ledger. The PG layer never infers
// tenant/model from a key string and never deletes entries out of band;
// cleanup uses the recorded rows as the only eligible targets.
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

func (p *PGStore) OpenRun(ctx context.Context, run RunRecord) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if run.LedgerID == "" || run.Owner == "" || run.Checkpoint == "" {
		return fmt.Errorf("ursm.v2: run record requires ledger_id, owner and checkpoint")
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO ursm_key_migration_runs (
		    ledger_id, owner, key_schema_mode, preflight_checksum,
		    preflight_total, preflight_migratable, preflight_canonical_present,
		    preflight_ambiguous, preflight_excluded, checkpoint, rollback_deadline
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (ledger_id) DO UPDATE SET
		    owner = EXCLUDED.owner,
		    preflight_checksum = EXCLUDED.preflight_checksum,
		    preflight_total = EXCLUDED.preflight_total,
		    preflight_migratable = EXCLUDED.preflight_migratable,
		    preflight_canonical_present = EXCLUDED.preflight_canonical_present,
		    preflight_ambiguous = EXCLUDED.preflight_ambiguous,
		    preflight_excluded = EXCLUDED.preflight_excluded,
		    checkpoint = EXCLUDED.checkpoint,
		    updated_at = NOW()
	`, run.LedgerID, run.Owner, run.KeySchemaMode.String(), run.PreflightChecksum,
		run.Total, run.Migratable, run.CanonicalPresent, run.Ambiguous, run.Excluded,
		string(run.Checkpoint), run.RollbackDeadline)
	if err != nil {
		return fmt.Errorf("ursm.v2: open migration run: %w", err)
	}
	return nil
}

func (p *PGStore) UpsertEntries(ctx context.Context, ledgerID string, entries []EntryRecord) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("ursm.v2: begin ledger upsert tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, e := range entries {
		tenant, credential, raw := "", 0, ""
		if e.Tuple != nil {
			tenant, credential, raw = e.Tuple.TenantID, e.Tuple.CredentialID, e.Tuple.RawModel
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ursm_key_migration_entries (
			    ledger_id, source_key, target_key, classification, schema_origin, key_type,
			    pttl_ms, generation, field_checksum,
			    tuple_tenant, tuple_credential, tuple_raw_model, state
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'classified')
			ON CONFLICT (ledger_id, source_key) DO UPDATE SET
			    target_key = EXCLUDED.target_key,
			    classification = EXCLUDED.classification,
			    schema_origin = EXCLUDED.schema_origin,
			    key_type = EXCLUDED.key_type,
			    pttl_ms = EXCLUDED.pttl_ms,
			    generation = EXCLUDED.generation,
			    field_checksum = EXCLUDED.field_checksum,
			    tuple_tenant = EXCLUDED.tuple_tenant,
			    tuple_credential = EXCLUDED.tuple_credential,
			    tuple_raw_model = EXCLUDED.tuple_raw_model,
			    updated_at = NOW()
		`, ledgerID, e.SourceKey, e.TargetKey, string(e.Class), e.Schema.String(), e.Type,
			e.PTTLMillis, e.Generation, e.FieldChecksum,
			tenant, credential, raw); err != nil {
			return fmt.Errorf("ursm.v2: upsert ledger entry %s: %w", e.SourceKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ursm.v2: commit ledger upsert tx: %w", err)
	}
	return nil
}