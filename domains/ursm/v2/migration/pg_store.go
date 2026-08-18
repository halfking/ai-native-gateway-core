package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// LoadedRun is the typed durable run snapshot used by cleanup authorization.
type LoadedRun struct {
	RunRecord
	RollbackDeadlineTime time.Time
}

// LoadedEntry is the durable per-source row used by cleanup. State and
// classification are returned as their frozen domain enums so callers cannot
// accidentally authorize a row by string formatting alone.
type LoadedEntry struct {
	SourceKey      string
	TargetKey      string
	Classification Classification
	Schema         store.KeySchema
	Type           string
	Generation     int64
	FieldChecksum  string
	Tuple          *store.ParsedNodeKey
	State          ItemStatus
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
	deadline, err := time.Parse(time.RFC3339Nano, run.RollbackDeadline)
	if err != nil || deadline.IsZero() {
		return fmt.Errorf("ursm.v2: run record requires RFC3339Nano rollback_deadline")
	}
	run.RollbackDeadline = deadline.UTC().Format(time.RFC3339Nano)
	_, err = p.pool.Exec(ctx, `
		INSERT INTO ursm_key_migration_runs (
		    ledger_id, owner, key_schema_mode, preflight_checksum,
		    preflight_total, preflight_migratable, preflight_canonical_present,
		    preflight_ambiguous, preflight_excluded, checkpoint, rollback_deadline
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (ledger_id) DO NOTHING
	`, run.LedgerID, run.Owner, run.KeySchemaMode.String(), run.PreflightChecksum,
		run.Total, run.Migratable, run.CanonicalPresent, run.Ambiguous, run.Excluded,
		string(run.Checkpoint), run.RollbackDeadline)
	if err != nil {
		return fmt.Errorf("ursm.v2: open migration run: %w", err)
	}
	var (
		owner, mode, checksum, checkpoint                 string
		storedDeadline                                    *time.Time
		total, migratable, canonical, ambiguous, excluded int
	)
	err = p.pool.QueryRow(ctx, `
		SELECT owner, key_schema_mode, preflight_checksum, checkpoint,
		       rollback_deadline, preflight_total, preflight_migratable,
		       preflight_canonical_present, preflight_ambiguous, preflight_excluded
		FROM ursm_key_migration_runs WHERE ledger_id = $1`, run.LedgerID).Scan(
		&owner, &mode, &checksum, &checkpoint, &storedDeadline, &total,
		&migratable, &canonical, &ambiguous, &excluded)
	if err != nil {
		return fmt.Errorf("ursm.v2: verify migration run identity: %w", err)
	}
	storedDeadlineWire := ""
	if storedDeadline != nil {
		storedDeadlineWire = storedDeadline.UTC().Format(time.RFC3339Nano)
	}
	if owner != run.Owner || mode != run.KeySchemaMode.String() || checksum != run.PreflightChecksum ||
		checkpoint != string(run.Checkpoint) || storedDeadlineWire != run.RollbackDeadline ||
		total != run.Total || migratable != run.Migratable || canonical != run.CanonicalPresent ||
		ambiguous != run.Ambiguous || excluded != run.Excluded {
		return fmt.Errorf("ursm.v2: migration ledger_id already belongs to a different immutable run")
	}
	return nil
}

// LoadRun returns the durable run snapshot. Missing rows are surfaced as
// pgx.ErrNoRows so an apply caller cannot confuse absence with an empty run.
func (p *PGStore) LoadRun(ctx context.Context, ledgerID string) (LoadedRun, error) {
	if p == nil || p.pool == nil {
		return LoadedRun{}, errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if ledgerID == "" {
		return LoadedRun{}, errors.New("ursm.v2: ledger_id is required")
	}
	var (
		run        LoadedRun
		mode       string
		checkpoint string
		deadline   *time.Time
	)
	err := p.pool.QueryRow(ctx, `
		SELECT ledger_id, owner, key_schema_mode, preflight_checksum,
		       checkpoint, rollback_deadline, preflight_total,
		       preflight_migratable, preflight_canonical_present,
		       preflight_ambiguous, preflight_excluded
		FROM ursm_key_migration_runs WHERE ledger_id = $1`, ledgerID).Scan(
		&run.LedgerID, &run.Owner, &mode, &run.PreflightChecksum,
		&checkpoint, &deadline, &run.Total, &run.Migratable,
		&run.CanonicalPresent, &run.Ambiguous, &run.Excluded)
	if err != nil {
		return LoadedRun{}, fmt.Errorf("ursm.v2: load migration run: %w", err)
	}
	parsedMode, err := store.ParseKeySchemaMode(mode)
	if err != nil {
		return LoadedRun{}, fmt.Errorf("ursm.v2: load migration run mode: %w", err)
	}
	run.KeySchemaMode = parsedMode
	run.Checkpoint = Checkpoint(checkpoint)
	if !run.Checkpoint.Valid() {
		return LoadedRun{}, fmt.Errorf("ursm.v2: load migration run checkpoint invalid: %q", checkpoint)
	}
	if deadline != nil {
		run.RollbackDeadlineTime = deadline.UTC()
		run.RollbackDeadline = deadline.UTC().Format(time.RFC3339Nano)
	}
	return run, nil
}

// LoadEntries returns all durable entries for a run in stable source-key order.
func (p *PGStore) LoadEntries(ctx context.Context, ledgerID string) ([]LoadedEntry, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("ursm.v2: PGStore requires pgx pool")
	}
	rows, err := p.pool.Query(ctx, `
		SELECT source_key, target_key, classification, schema_origin, key_type,
		       generation, field_checksum, tuple_tenant, tuple_credential,
		       tuple_raw_model, state
		FROM ursm_key_migration_entries
		WHERE ledger_id = $1 ORDER BY source_key`, ledgerID)
	if err != nil {
		return nil, fmt.Errorf("ursm.v2: load migration entries: %w", err)
	}
	defer rows.Close()
	var out []LoadedEntry
	for rows.Next() {
		var (
			e                    LoadedEntry
			class, schema, state string
			tenant, rawModel     string
			credential           int
		)
		if err := rows.Scan(&e.SourceKey, &e.TargetKey, &class, &schema, &e.Type,
			&e.Generation, &e.FieldChecksum, &tenant, &credential, &rawModel, &state); err != nil {
			return nil, fmt.Errorf("ursm.v2: scan migration entry: %w", err)
		}
		e.Classification = Classification(class)
		e.State = ItemStatus(state)
		if !validMigrationClassification(e.Classification) {
			return nil, fmt.Errorf("ursm.v2: invalid migration entry classification %q", class)
		}
		if !validMigrationState(e.State) {
			return nil, fmt.Errorf("ursm.v2: invalid migration entry state %q", state)
		}
		if schema != "" && schema != "legacy" && schema != "k2" {
			return nil, fmt.Errorf("ursm.v2: invalid migration entry schema %q", schema)
		}
		e.Schema = store.ParseKeySchema(schema)
		if tenant != "" || rawModel != "" {
			e.Tuple = &store.ParsedNodeKey{TenantID: tenant, CredentialID: credential, RawModel: rawModel}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ursm.v2: iterate migration entries: %w", err)
	}
	return out, nil
}

// ClaimEntryForCleanup fences one copied entry before Redis deletion.
func (p *PGStore) ClaimEntryForCleanup(ctx context.Context, ledgerID, sourceKey, checksum string) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE ursm_key_migration_entries SET state = 'fenced', updated_at = NOW()
		WHERE ledger_id = $1 AND source_key = $2 AND state = 'copied' AND field_checksum = $3`,
		ledgerID, sourceKey, checksum)
	if err != nil {
		return fmt.Errorf("ursm.v2: claim migration entry: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("ursm.v2: cleanup claim state or checksum mismatch")
	}
	return nil
}

// ReleaseEntryCleanupClaim returns a refused fenced entry to copied.
func (p *PGStore) ReleaseEntryCleanupClaim(ctx context.Context, ledgerID, sourceKey, checksum string) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE ursm_key_migration_entries SET state = 'copied', updated_at = NOW()
		WHERE ledger_id = $1 AND source_key = $2 AND state = 'fenced' AND field_checksum = $3`,
		ledgerID, sourceKey, checksum)
	if err != nil {
		return fmt.Errorf("ursm.v2: release cleanup claim: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("ursm.v2: cleanup claim release state or checksum mismatch")
	}
	return nil
}

// MarkEntryCleaned advances one copied/fenced entry only if its identity and checksum
// still match. It is safe to retry after a successful delete or missing source.
func (p *PGStore) MarkEntryCleaned(ctx context.Context, ledgerID, sourceKey, checksum string) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if ledgerID == "" || sourceKey == "" || checksum == "" {
		return errors.New("ursm.v2: cleanup state transition requires ledger_id, source_key and checksum")
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE ursm_key_migration_entries
		SET state = 'cleaned', cleaned_at = COALESCE(cleaned_at, NOW()), updated_at = NOW()
		WHERE ledger_id = $1 AND source_key = $2 AND state = 'fenced' AND field_checksum = $3`,
		ledgerID, sourceKey, checksum)
	if err != nil {
		return fmt.Errorf("ursm.v2: mark migration entry cleaned: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("ursm.v2: mark migration entry cleaned: state or checksum mismatch")
	}
	return nil
}

func validMigrationClassification(class Classification) bool {
	switch class {
	case ClassificationMigratable, ClassificationCanonicalPresent,
		ClassificationAmbiguous, ClassificationExcludedNonAuthoritative:
		return true
	}
	return false
}

func validMigrationState(state ItemStatus) bool {
	switch state {
	case StatusClassified, StatusCopied, StatusCleaned, StatusRolledBack,
		StatusConflict:
		return true
	}
	return state == "expired" || state == "fenced"
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
				ON CONFLICT (ledger_id, source_key) DO NOTHING
			`, ledgerID, e.SourceKey, e.TargetKey, string(e.Class), e.Schema.String(), e.Type,
			e.PTTLMillis, e.Generation, e.FieldChecksum,
			tenant, credential, raw); err != nil {
			return fmt.Errorf("ursm.v2: insert ledger entry %s: %w", e.SourceKey, err)
		}
		var (
			storedTarget, storedClass, storedSchema, storedType, storedChecksum string
			storedTenant, storedRaw, storedState                                string
			storedPTTL, storedGeneration, storedCredential                      int64
		)
		if err := tx.QueryRow(ctx, `
				SELECT target_key, classification, schema_origin, key_type, pttl_ms,
				       generation, field_checksum, tuple_tenant, tuple_credential,
				       tuple_raw_model, state
				FROM ursm_key_migration_entries WHERE ledger_id = $1 AND source_key = $2`, ledgerID, e.SourceKey).Scan(
			&storedTarget, &storedClass, &storedSchema, &storedType, &storedPTTL,
			&storedGeneration, &storedChecksum, &storedTenant, &storedCredential,
			&storedRaw, &storedState); err != nil {
			return fmt.Errorf("ursm.v2: verify ledger entry %s: %w", e.SourceKey, err)
		}
		if storedTarget != e.TargetKey || storedClass != string(e.Class) || storedSchema != e.Schema.String() ||
			storedType != e.Type || storedPTTL != e.PTTLMillis || storedGeneration != e.Generation ||
			storedChecksum != e.FieldChecksum || storedTenant != tenant || storedCredential != int64(credential) ||
			storedRaw != raw || (storedState != string(StatusClassified) && storedState != string(StatusCopied)) {
			return fmt.Errorf("ursm.v2: ledger entry %s already contains different immutable identity", e.SourceKey)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ursm.v2: commit ledger upsert tx: %w", err)
	}
	return nil
}
