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
	CutoverEpoch      int64
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
			    preflight_ambiguous, preflight_excluded, checkpoint, cutover_epoch, rollback_deadline
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (ledger_id) DO NOTHING
	`, run.LedgerID, run.Owner, run.KeySchemaMode.String(), run.PreflightChecksum,
		run.Total, run.Migratable, run.CanonicalPresent, run.Ambiguous, run.Excluded,
		string(run.Checkpoint), run.CutoverEpoch, run.RollbackDeadline)
	if err != nil {
		return fmt.Errorf("ursm.v2: open migration run: %w", err)
	}
	var (
		owner, mode, checksum, checkpoint                 string
		storedDeadline                                    *time.Time
		storedEpoch                                       int64
		total, migratable, canonical, ambiguous, excluded int
	)
	err = p.pool.QueryRow(ctx, `
			SELECT owner, key_schema_mode, preflight_checksum, checkpoint, cutover_epoch,
			       rollback_deadline, preflight_total, preflight_migratable,
		       preflight_canonical_present, preflight_ambiguous, preflight_excluded
		FROM ursm_key_migration_runs WHERE ledger_id = $1`, run.LedgerID).Scan(
		&owner, &mode, &checksum, &checkpoint, &storedEpoch, &storedDeadline, &total,
		&migratable, &canonical, &ambiguous, &excluded)
	if err != nil {
		return fmt.Errorf("ursm.v2: verify migration run identity: %w", err)
	}
	storedDeadlineWire := ""
	if storedDeadline != nil {
		storedDeadlineWire = storedDeadline.UTC().Format(time.RFC3339Nano)
	}
	if owner != run.Owner || mode != run.KeySchemaMode.String() || checksum != run.PreflightChecksum ||
		checkpoint != string(run.Checkpoint) || storedEpoch != run.CutoverEpoch || storedDeadlineWire != run.RollbackDeadline ||
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
			       checkpoint, cutover_epoch, rollback_deadline, preflight_total,
		       preflight_migratable, preflight_canonical_present,
		       preflight_ambiguous, preflight_excluded
		FROM ursm_key_migration_runs WHERE ledger_id = $1`, ledgerID).Scan(
		&run.LedgerID, &run.Owner, &mode, &run.PreflightChecksum,
		&checkpoint, &run.CutoverEpoch, &deadline, &run.Total, &run.Migratable,
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

// AdvanceRun atomically applies one legal, approved checkpoint transition and
// records immutable evidence. PostgreSQL is the authorization source; Redis is
// only mirrored after this transaction commits.
func (p *PGStore) advanceRun(ctx context.Context, request PromotionRequest, now time.Time) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if err := validPromotion(request); err != nil {
		return err
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("ursm.v2: begin checkpoint transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if request.ExpectedCheckpoint == CheckpointCopy && request.Checkpoint == CheckpointCoverage {
		var incomplete int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM ursm_key_migration_entries
			WHERE ledger_id = $1 AND classification = 'migratable' AND state <> 'copied'`, request.LedgerID).Scan(&incomplete); err != nil {
			return fmt.Errorf("ursm.v2: check copy coverage: %w", err)
		}
		if incomplete != 0 {
			return fmt.Errorf("ursm.v2: coverage promotion requires every migratable entry copied")
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE ursm_key_migration_runs
		SET checkpoint = $1, key_schema_mode = $2, cutover_epoch = cutover_epoch + 1, updated_at = $3,
		    finished_at = CASE WHEN $1 IN ('done','rollback') THEN COALESCE(finished_at, $3) ELSE finished_at END
		WHERE ledger_id = $4 AND owner = $5 AND checkpoint = $6 AND cutover_epoch = $7`,
		string(request.Checkpoint), string(request.Mode), now, request.LedgerID, request.Owner,
		string(request.ExpectedCheckpoint), request.ExpectedEpoch)
	if err != nil {
		return fmt.Errorf("ursm.v2: advance migration run: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("ursm.v2: checkpoint transition fenced by owner, epoch, or predecessor")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ursm_key_migration_transitions (
			ledger_id, cutover_epoch, from_checkpoint, to_checkpoint, key_schema_mode,
			actor, approved_by, evidence_sha256, evidence_ref, reason, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		request.LedgerID, request.ExpectedEpoch+1, string(request.ExpectedCheckpoint), string(request.Checkpoint),
		string(request.Mode), request.Actor, request.ApprovedBy, request.EvidenceSHA256, request.EvidenceRef, request.Reason, now); err != nil {
		return fmt.Errorf("ursm.v2: record checkpoint evidence: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ursm.v2: commit checkpoint transition: %w", err)
	}
	return nil
}

// PersistCopyOutcome transitions exactly one immutable, currently classified
// entry. A replay after Redis succeeds is allowed only as an idempotent copied
// outcome with the same source snapshot and run fence.
func (p *PGStore) PersistCopyOutcome(ctx context.Context, outcome CopyOutcome) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if err := outcome.validate(); err != nil {
		return err
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE ursm_key_migration_entries AS entry
		SET state = $1,
		    copied_at = CASE WHEN $1 = 'copied' THEN COALESCE(entry.copied_at, NOW()) ELSE entry.copied_at END,
		    copied_pttl_ms = CASE WHEN $1 = 'copied' THEN COALESCE(entry.copied_pttl_ms, $2) ELSE entry.copied_pttl_ms END,
		    last_error = CASE WHEN $1 = 'copied' THEN '' ELSE $3 END,
		    updated_at = NOW()
		FROM ursm_key_migration_runs AS run
		WHERE entry.ledger_id = run.ledger_id
		  AND entry.ledger_id = $4 AND entry.source_key = $5 AND entry.target_key = $6
		  AND entry.generation = $7 AND entry.field_checksum = $8
		  AND entry.classification = 'migratable'
		  AND run.owner = $9 AND run.cutover_epoch = $10 AND run.checkpoint = 'copy'
		  AND (
			entry.state = 'classified'
			OR (entry.state = 'copied' AND $1 = 'copied')
		  )`,
		string(outcome.State), outcome.CopiedPTTLMs, outcome.LastError,
		outcome.LedgerID, outcome.SourceKey, outcome.TargetKey, outcome.Generation, outcome.FieldChecksum,
		outcome.Owner, outcome.Epoch)
	if err != nil {
		return fmt.Errorf("ursm.v2: persist copy outcome: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("ursm.v2: copy outcome fenced by immutable entry or run state")
	}
	return nil
}

// ReconcilePromotion checks that a previously committed PG transition is
// still the expected immutable transition before its Redis mirror is retried.
func (p *PGStore) ReconcilePromotion(ctx context.Context, request PromotionRequest) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if err := validPromotion(request); err != nil {
		return err
	}
	var found bool
	if err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ursm_key_migration_runs run
			JOIN ursm_key_migration_transitions transition
			  ON transition.ledger_id = run.ledger_id AND transition.cutover_epoch = run.cutover_epoch
			WHERE run.ledger_id = $1 AND run.owner = $2 AND run.cutover_epoch = $3
			  AND run.checkpoint = $4 AND run.key_schema_mode = $5
			  AND transition.from_checkpoint = $6 AND transition.to_checkpoint = $4
			  AND transition.actor = $7 AND transition.approved_by = $8
			  AND transition.evidence_sha256 = $9 AND transition.evidence_ref = $10
		)`, request.LedgerID, request.Owner, request.ExpectedEpoch+1, string(request.Checkpoint), string(request.Mode),
		string(request.ExpectedCheckpoint), request.Actor, request.ApprovedBy, request.EvidenceSHA256, request.EvidenceRef).Scan(&found); err != nil {
		return fmt.Errorf("ursm.v2: reconcile durable promotion: %w", err)
	}
	if !found {
		return errors.New("ursm.v2: durable promotion evidence does not match reconciliation request")
	}
	return nil
}

// HasApprovedCleanupPromotion verifies that cleanup is the current durable
// epoch and was reached through its append-only owner-approved evidence row.
func (p *PGStore) HasApprovedCleanupPromotion(ctx context.Context, ledgerID, owner string, epoch int64) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	var found bool
	if err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM ursm_key_migration_runs run
			JOIN ursm_key_migration_transitions transition
			  ON transition.ledger_id = run.ledger_id AND transition.cutover_epoch = run.cutover_epoch
			WHERE run.ledger_id = $1 AND run.owner = $2 AND run.cutover_epoch = $3
			  AND run.checkpoint = 'cleanup' AND run.key_schema_mode = 'canonical'
			  AND transition.from_checkpoint = 'observe' AND transition.to_checkpoint = 'cleanup'
			  AND transition.key_schema_mode = 'canonical'
			  AND transition.approved_by <> '' AND transition.evidence_sha256 <> '' AND transition.evidence_ref <> ''
		)`, ledgerID, owner, epoch).Scan(&found); err != nil {
		return fmt.Errorf("ursm.v2: check cleanup promotion evidence: %w", err)
	}
	if !found {
		return errors.New("ursm.v2: cleanup requires approved durable observe evidence")
	}
	return nil
}

// ClaimEntryForCleanup fences one copied entry before Redis deletion.
func (p *PGStore) ClaimEntryForCleanup(ctx context.Context, ledgerID, sourceKey, checksum string) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE ursm_key_migration_entries AS entry
		SET state = 'fenced', cleanup_claim_owner = run.owner, cleanup_claim_epoch = run.cutover_epoch,
		    cleanup_claimed_at = NOW(), updated_at = NOW()
		FROM ursm_key_migration_runs AS run
		JOIN ursm_key_migration_transitions AS transition
		  ON transition.ledger_id = run.ledger_id AND transition.cutover_epoch = run.cutover_epoch
		WHERE entry.ledger_id = run.ledger_id
		  AND entry.ledger_id = $1 AND entry.source_key = $2 AND entry.state = 'copied' AND entry.field_checksum = $3
		  AND run.checkpoint = 'cleanup' AND run.key_schema_mode = 'canonical'
		  AND transition.from_checkpoint = 'observe' AND transition.to_checkpoint = 'cleanup'
		  AND transition.approved_by <> '' AND transition.evidence_sha256 <> '' AND transition.evidence_ref <> ''`,
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
		UPDATE ursm_key_migration_entries AS entry
		SET state = 'copied', cleanup_claim_owner = '', cleanup_claim_epoch = NULL,
		    cleanup_claimed_at = NULL, updated_at = NOW()
		FROM ursm_key_migration_runs AS run
		WHERE entry.ledger_id = run.ledger_id
		  AND entry.ledger_id = $1 AND entry.source_key = $2 AND entry.state = 'fenced' AND entry.field_checksum = $3
		  AND entry.cleanup_claim_owner = run.owner AND entry.cleanup_claim_epoch = run.cutover_epoch`,
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
		UPDATE ursm_key_migration_entries AS entry
		SET state = 'cleaned', cleaned_at = COALESCE(cleaned_at, NOW()), cleanup_claim_owner = '',
		    cleanup_claim_epoch = NULL, cleanup_claimed_at = NULL, updated_at = NOW()
		FROM ursm_key_migration_runs AS run
		WHERE entry.ledger_id = run.ledger_id
		  AND entry.ledger_id = $1 AND entry.source_key = $2 AND entry.state = 'fenced' AND entry.field_checksum = $3
		  AND entry.cleanup_claim_owner = run.owner AND entry.cleanup_claim_epoch = run.cutover_epoch`,
		ledgerID, sourceKey, checksum)
	if err != nil {
		return fmt.Errorf("ursm.v2: mark migration entry cleaned: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("ursm.v2: mark migration entry cleaned: state or checksum mismatch")
	}
	return nil
}

// RecoverStaleCleanupClaim returns an abandoned fenced claim to copied after
// its lease expires. It never mutates Redis and therefore cannot authorize a
// deletion by itself.
func (p *PGStore) RecoverStaleCleanupClaim(ctx context.Context, ledgerID, sourceKey, owner string, epoch int64, lease time.Duration, now time.Time) error {
	if p == nil || p.pool == nil {
		return errors.New("ursm.v2: PGStore requires pgx pool")
	}
	if ledgerID == "" || sourceKey == "" || owner == "" || epoch < 0 || lease <= 0 {
		return errors.New("ursm.v2: stale cleanup recovery requires identity, epoch, and positive lease")
	}
	result, err := p.pool.Exec(ctx, `
		UPDATE ursm_key_migration_entries
		SET state = 'copied', cleanup_claim_owner = '', cleanup_claim_epoch = NULL,
		    cleanup_claimed_at = NULL, last_error = 'stale cleanup claim recovered', updated_at = $6
		WHERE ledger_id = $1 AND source_key = $2 AND state = 'fenced'
		  AND cleanup_claim_owner = $3 AND cleanup_claim_epoch = $4
		  AND cleanup_claimed_at IS NOT NULL AND cleanup_claimed_at <= $5`,
		ledgerID, sourceKey, owner, epoch, now.UTC().Add(-lease), now.UTC())
	if err != nil {
		return fmt.Errorf("ursm.v2: recover stale cleanup claim: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("ursm.v2: stale cleanup claim not expired or identity mismatch")
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
