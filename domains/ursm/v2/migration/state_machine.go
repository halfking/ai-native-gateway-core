package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PromotionRequest is an auditable request to move a durable migration run.
// It is intentionally library-only: command flags and NDJSON records cannot
// create an approval or move a run to cleanup.
type PromotionRequest struct {
	LedgerID           string
	Owner              string
	ExpectedCheckpoint Checkpoint
	ExpectedEpoch      int64
	Checkpoint         Checkpoint
	Mode               Mode
	Actor              string
	ApprovedBy         string
	EvidenceSHA256     string
	EvidenceRef        string
	Reason             string
}

// PromotionApprover is supplied by the owner-controlled approval boundary.
// A coordinator without an approver fails closed.
type PromotionApprover interface {
	ApprovePromotion(context.Context, PromotionRequest) error
}

// PromotionCoordinator commits the authoritative PostgreSQL transition before
// asking Redis to mirror it. A failed mirror leaves cleanup unavailable because
// cleanup requires exact PG/Redis agreement.
type PromotionCoordinator struct {
	PG       *PGStore
	Metadata *MetadataHash
	Approver PromotionApprover
	Now      func() time.Time
}

func (c *PromotionCoordinator) ReconcilePromotion(ctx context.Context, request PromotionRequest) error {
	if c == nil || c.PG == nil || c.Metadata == nil || c.Approver == nil {
		return errors.New("ursm.v2: promotion reconciliation requires PG, Redis metadata, and owner approver")
	}
	if err := c.Approver.ApprovePromotion(ctx, request); err != nil {
		return fmt.Errorf("ursm.v2: reconciliation approval: %w", err)
	}
	if err := c.PG.ReconcilePromotion(ctx, request); err != nil {
		return err
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	return c.Metadata.mirrorPromotion(ctx, request, now)
}

func (c *PromotionCoordinator) Promote(ctx context.Context, request PromotionRequest) error {
	if c == nil || c.PG == nil || c.Metadata == nil || c.Approver == nil {
		return errors.New("ursm.v2: promotion requires PG, Redis metadata, and owner approver")
	}
	if err := c.Approver.ApprovePromotion(ctx, request); err != nil {
		return fmt.Errorf("ursm.v2: promotion approval: %w", err)
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	if err := c.PG.advanceRun(ctx, request, now); err != nil {
		return err
	}
	if err := c.Metadata.mirrorPromotion(ctx, request, now); err != nil {
		return fmt.Errorf("ursm.v2: durable promotion committed but Redis mirror is unavailable; call ReconcilePromotion with the same owner-approved request: %w", err)
	}
	return nil
}

func validPromotion(request PromotionRequest) error {
	if request.LedgerID == "" || request.Owner == "" || request.Actor == "" || request.ApprovedBy == "" || request.EvidenceRef == "" {
		return errors.New("ursm.v2: promotion requires ledger_id, owner, actor, approved_by, and evidence_ref")
	}
	if request.ExpectedEpoch < 0 || !request.ExpectedCheckpoint.Valid() || !request.Checkpoint.Valid() || !request.Mode.Valid() {
		return errors.New("ursm.v2: promotion contains invalid epoch, checkpoint, or mode")
	}
	if len(request.EvidenceSHA256) != sha256.Size*2 {
		return errors.New("ursm.v2: promotion evidence_sha256 must be lowercase SHA-256")
	}
	decoded, err := hex.DecodeString(request.EvidenceSHA256)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(request.EvidenceSHA256) != request.EvidenceSHA256 {
		return errors.New("ursm.v2: promotion evidence_sha256 must be lowercase SHA-256")
	}
	if !legalCheckpointEdge(request.ExpectedCheckpoint, request.Checkpoint) {
		return fmt.Errorf("ursm.v2: illegal migration checkpoint transition %s -> %s", request.ExpectedCheckpoint, request.Checkpoint)
	}
	if request.Checkpoint == CheckpointDual && request.Mode != ModeDual {
		return errors.New("ursm.v2: dual checkpoint requires dual mode")
	}
	if request.ExpectedCheckpoint == CheckpointDual && request.Checkpoint == CheckpointObserve && request.Mode != ModeDual {
		return errors.New("ursm.v2: observe promotion must remain in dual mode")
	}
	if request.Checkpoint == CheckpointCleanup && request.Mode != ModeCanonical {
		return errors.New("ursm.v2: cleanup promotion requires canonical mode")
	}
	if request.Checkpoint == CheckpointRollback && request.Mode != ModeLegacy {
		return errors.New("ursm.v2: rollback promotion requires legacy mode")
	}
	return nil
}

func legalCheckpointEdge(from, to Checkpoint) bool {
	if to == CheckpointRollback {
		switch from {
		case CheckpointPreflight, CheckpointCopy, CheckpointCoverage, CheckpointObserve, CheckpointCleanup:
			return true
		}
	}
	switch from {
	case CheckpointPreflight:
		return to == CheckpointCopy
	case CheckpointCopy:
		return to == CheckpointCoverage
	case CheckpointCoverage:
		return to == CheckpointDual
	case CheckpointDual:
		return to == CheckpointObserve
	case CheckpointObserve:
		return to == CheckpointCleanup
	case CheckpointCleanup:
		return to == CheckpointDone
	}
	return false
}

// CopyOutcome is a durable classification of a Lua-protected copy result.
type CopyOutcome struct {
	LedgerID      string
	Owner         string
	Epoch         int64
	SourceKey     string
	TargetKey     string
	Generation    int64
	FieldChecksum string
	State         ItemStatus
	CopiedPTTLMs  *int64
	LastError     string
}

func (o CopyOutcome) validate() error {
	if o.LedgerID == "" || o.Owner == "" || o.SourceKey == "" || o.TargetKey == "" || o.FieldChecksum == "" || o.Epoch < 0 {
		return errors.New("ursm.v2: copy outcome requires fenced immutable entry identity")
	}
	if o.SourceKey == o.TargetKey {
		return errors.New("ursm.v2: copy outcome source and target must differ")
	}
	switch o.State {
	case StatusCopied:
		if o.CopiedPTTLMs == nil {
			return errors.New("ursm.v2: copied outcome requires target PTTL")
		}
	case StatusConflict, ItemStatus("expired"):
	default:
		return fmt.Errorf("ursm.v2: invalid durable copy outcome state %q", o.State)
	}
	return nil
}

// DurableCopy drives only durable PG entries through EntryCopier. It never
// consumes local NDJSON as authority, so a local file cannot elevate cleanup.
type DurableCopy struct {
	PG       *PGStore
	Metadata *MetadataHash
	Copier   *EntryCopier
	Now      func() time.Time
}

func (c *DurableCopy) CopyEntry(ctx context.Context, ledgerID, owner string, epoch int64, entry EntryRecord) (EntryCopyResult, error) {
	if c == nil || c.PG == nil || c.Metadata == nil || c.Copier == nil {
		return EntryCopyResult{}, errors.New("ursm.v2: durable copy requires PG, Redis metadata, and copier")
	}
	if err := c.authorize(ctx, ledgerID, owner, epoch); err != nil {
		return EntryCopyResult{}, err
	}
	result, err := c.Copier.CopyHash(ctx, entry)
	if err != nil {
		state := StatusConflict
		if !errors.Is(err, ErrEntryGenerationChanged) && !errors.Is(err, ErrEntryChecksumChanged) && !errors.Is(err, ErrEntryTargetConflict) {
			return EntryCopyResult{}, err
		}
		persistErr := c.PG.PersistCopyOutcome(ctx, CopyOutcome{
			LedgerID: ledgerID, Owner: owner, Epoch: epoch, SourceKey: entry.SourceKey, TargetKey: entry.TargetKey,
			Generation: entry.Generation, FieldChecksum: entry.FieldChecksum, State: state, LastError: err.Error(),
		})
		if persistErr != nil {
			return EntryCopyResult{}, persistErr
		}
		return EntryCopyResult{}, err
	}
	if result.Status == EntryCopySkippedExpired {
		if err := c.PG.PersistCopyOutcome(ctx, CopyOutcome{
			LedgerID: ledgerID, Owner: owner, Epoch: epoch, SourceKey: entry.SourceKey, TargetKey: entry.TargetKey,
			Generation: entry.Generation, FieldChecksum: entry.FieldChecksum, State: ItemStatus("expired"), LastError: "source expired before copy",
		}); err != nil {
			return EntryCopyResult{}, err
		}
		return result, nil
	}
	pttl, err := c.Copier.rdb.PTTL(ctx, entry.TargetKey).Result()
	if err != nil {
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: read copied target PTTL: %w", err)
	}
	milliseconds := pttl.Milliseconds()
	if err := c.PG.PersistCopyOutcome(ctx, CopyOutcome{
		LedgerID: ledgerID, Owner: owner, Epoch: epoch, SourceKey: entry.SourceKey, TargetKey: entry.TargetKey,
		Generation: entry.Generation, FieldChecksum: entry.FieldChecksum, State: StatusCopied, CopiedPTTLMs: &milliseconds,
	}); err != nil {
		return EntryCopyResult{}, err
	}
	return result, nil
}

func (c *DurableCopy) authorize(ctx context.Context, ledgerID, owner string, epoch int64) error {
	if ledgerID == "" || owner == "" || epoch < 0 {
		return errors.New("ursm.v2: durable copy requires ledger_id, owner, and epoch")
	}
	run, err := c.PG.LoadRun(ctx, ledgerID)
	if err != nil {
		return err
	}
	live, err := c.Metadata.Read(ctx)
	if err != nil {
		return err
	}
	if run.Owner != owner || run.CutoverEpoch != epoch || run.Checkpoint != CheckpointCopy ||
		live.Owner != owner || live.LedgerID != ledgerID || live.CutoverEpoch != epoch || live.Checkpoint != CheckpointCopy ||
		live.PreflightChecksum != run.PreflightChecksum || Mode(run.KeySchemaMode.String()) != live.Mode {
		return errors.New("ursm.v2: durable copy authorization mismatch")
	}
	return nil
}

func loadedEntryRecord(entry LoadedEntry) EntryRecord {
	return EntryRecord{
		SourceKey: entry.SourceKey, TargetKey: entry.TargetKey, Class: entry.Classification,
		Schema: entry.Schema, Type: entry.Type, Generation: entry.Generation, FieldChecksum: entry.FieldChecksum,
		Tuple: entry.Tuple,
	}
}
