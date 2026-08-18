package migration

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed cleanup_hash.lua
var cleanupHashSrc string

var cleanupHashScript = redis.NewScript(cleanupHashSrc)

type compareDeleteResult string

const (
	compareDeleteDeleted   compareDeleteResult = "deleted"
	compareDeleteMissing   compareDeleteResult = "missing"
	compareDeleteChanged   compareDeleteResult = "changed"
	compareDeleteWrongType compareDeleteResult = "wrong_type"
)

func deleteHashIfUnchanged(ctx context.Context, rdb *redis.Client, key string, expected map[string]string) (compareDeleteResult, error) {
	args := make([]interface{}, 0, 1+len(expected)*2)
	args = append(args, strconv.Itoa(len(expected)))
	fields := make([]string, 0, len(expected))
	for field := range expected {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		args = append(args, field, expected[field])
	}
	result, err := cleanupHashScript.Run(ctx, rdb, []string{key}, args...).Text()
	if err != nil {
		return "", err
	}
	switch compareDeleteResult(result) {
	case compareDeleteDeleted, compareDeleteMissing, compareDeleteChanged, compareDeleteWrongType:
		return compareDeleteResult(result), nil
	default:
		return "", fmt.Errorf("migration: cleanup compare-delete unexpected result %q", result)
	}
}

type CleanupStatus string

const (
	CleanupStatusDeleted   CleanupStatus = "deleted"
	CleanupStatusSkipped   CleanupStatus = "skipped"
	CleanupStatusRefused   CleanupStatus = "refused"
	CleanupStatusPreserved CleanupStatus = "preserved"
)

// CleanupOptions controls the exact-ledger delete pass. RateLimitPerSec=0
// disables rate limiting (useful for tests).
type CleanupOptions struct {
	Mode            Mode
	Now             func() time.Time
	RateLimitPerSec int
	// Authorization is the already cross-checked durable cleanup authority.
	// Cleanup refuses to delete without it.
	Authorization *CleanupAuthorization
	// Claim marks a copied durable entry fenced before Redis deletion.
	Claim func(Item) error
	// ReleaseClaim returns a fenced entry to copied when deletion is refused.
	ReleaseClaim func(Item) error
	// MarkCleaned persists audit state only after Redis deletion succeeds.
	MarkCleaned func(Item) error
	// ForceRollback is retained only for CLI compatibility. It never bypasses
	// the durable cleanup authorization or rollback deadline.
	ForceRollback bool
}

// CleanupRun is the durable run record needed to authorize a cleanup pass.
// PG-backed callers populate it from the migration run row; Metadata is the
// live Redis fence that must agree with it before any source key is deleted.
type CleanupRun struct {
	Owner             string
	LedgerID          string
	Mode              Mode
	PreflightChecksum string
	RollbackDeadline  time.Time
	Checkpoint        Checkpoint
}

// CleanupAuthorization contains the durable run identity and the exact
// entries approved for cleanup. It is deliberately separate from the local
// NDJSON ledger: the latter is an audit artifact, not deletion authority.
type CleanupAuthorization struct {
	Metadata Metadata
	Run      CleanupRun
	Items    []Item
}

func (a *CleanupAuthorization) validate(now time.Time) error {
	if a == nil {
		return fmt.Errorf("migration: cleanup authorization is required")
	}
	if err := a.Metadata.Validate(); err != nil {
		return fmt.Errorf("migration: cleanup metadata: %w", err)
	}
	if a.Run.Owner == "" || a.Run.LedgerID == "" || a.Run.PreflightChecksum == "" {
		return fmt.Errorf("migration: cleanup run identity is incomplete")
	}
	if a.Metadata.Owner != a.Run.Owner {
		return fmt.Errorf("migration: cleanup authorization owner mismatch")
	}
	if a.Metadata.LedgerID != a.Run.LedgerID {
		return fmt.Errorf("migration: cleanup authorization ledger_id mismatch")
	}
	if a.Metadata.Mode != a.Run.Mode {
		return fmt.Errorf("migration: cleanup authorization mode mismatch")
	}
	if a.Metadata.PreflightChecksum != a.Run.PreflightChecksum {
		return fmt.Errorf("migration: cleanup authorization preflight checksum mismatch")
	}
	if a.Metadata.Checkpoint != CheckpointCleanup || a.Run.Checkpoint != CheckpointCleanup {
		return fmt.Errorf("migration: cleanup authorization checkpoint must be cleanup")
	}
	if a.Metadata.Mode == ModeLegacy || a.Run.Mode == ModeLegacy {
		return fmt.Errorf("migration: cleanup authorization refused in mode=legacy")
	}
	if a.Metadata.RollbackDeadline.IsZero() || a.Run.RollbackDeadline.IsZero() {
		return fmt.Errorf("migration: cleanup rollback deadline is required")
	}
	if !a.Metadata.RollbackDeadline.Equal(a.Run.RollbackDeadline) {
		return fmt.Errorf("migration: cleanup authorization rollback deadline mismatch")
	}
	if now.Before(a.Run.RollbackDeadline) {
		return fmt.Errorf("migration: cleanup rollback deadline has not elapsed")
	}
	if len(a.Items) == 0 {
		return fmt.Errorf("migration: cleanup authorization has no eligible entries")
	}
	return nil
}

// LoadCleanupAuthorization cross-checks the frozen Redis metadata HASH with
// the durable PostgreSQL run/entry ledger. A caller receives only complete,
// copied, exact hash entries; every other row is preserved for audit.
func LoadCleanupAuthorization(ctx context.Context, metadata *MetadataHash, pg *PGStore) (*CleanupAuthorization, error) {
	if metadata == nil {
		return nil, fmt.Errorf("migration: cleanup metadata store is required")
	}
	if pg == nil {
		return nil, fmt.Errorf("migration: cleanup PG store is required")
	}
	live, err := metadata.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration: cleanup read Redis metadata: %w", err)
	}
	if live.LedgerID == "" {
		return nil, fmt.Errorf("migration: cleanup Redis metadata is missing")
	}
	run, err := pg.LoadRun(ctx, live.LedgerID)
	if err != nil {
		return nil, fmt.Errorf("migration: cleanup load PG run: %w", err)
	}
	if err := pg.HasApprovedCleanupPromotion(ctx, run.LedgerID, run.Owner, run.CutoverEpoch); err != nil {
		return nil, fmt.Errorf("migration: cleanup durable promotion evidence: %w", err)
	}
	entries, err := pg.LoadEntries(ctx, live.LedgerID)
	if err != nil {
		return nil, fmt.Errorf("migration: cleanup load PG entries: %w", err)
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if entry.Classification != ClassificationMigratable || entry.State != StatusCopied ||
			entry.Type != "hash" || entry.SourceKey == "" || entry.TargetKey == "" ||
			entry.SourceKey == entry.TargetKey || entry.FieldChecksum == "" {
			continue
		}
		items = append(items, Item{
			SourceKey:      entry.SourceKey,
			CanonicalKey:   entry.TargetKey,
			KeyType:        entry.Type,
			SchemaSource:   entry.Schema.String(),
			Classification: entry.Classification,
			Generation:     entry.Generation,
			FieldChecksum:  entry.FieldChecksum,
			Status:         entry.State,
		})
	}
	return &CleanupAuthorization{
		Metadata: live,
		Run: CleanupRun{
			Owner:             run.Owner,
			LedgerID:          run.LedgerID,
			Mode:              Mode(run.KeySchemaMode.String()),
			PreflightChecksum: run.PreflightChecksum,
			RollbackDeadline:  run.RollbackDeadlineTime,
			Checkpoint:        run.Checkpoint,
		},
		Items: items,
	}, nil
}

// CleanupResult is the per-item ledger update emitted by Cleanup.
type CleanupResult struct {
	Item   Item
	Status CleanupStatus
	Reason string
}

// Cleanup runs the exact-key ledger deletion (doc 14 §6.2). Only ledger
// items with StatusCopied AND matching live field_checksum AND matching
// live key are deleted. The job is rate-limited, pauseable (ctx cancel)
// and resumable (a second pass skips items already marked cleaned).
type Cleanup struct {
	Prefix string
	Ledger *Ledger
	RDB    *redis.Client
	Opts   CleanupOptions
}

// Cleanup iterates the ledger and deletes source keys whose item is in
// StatusCopied (i.e. already copied and verified). All other items are
// skipped or refused; nothing in this function ever deletes a key absent from
// the ledger (doc 14 §6.2 forbids SCAN+DEL).
func (c *Cleanup) Cleanup(ctx context.Context) ([]CleanupResult, error) {
	if c.Prefix == "" {
		return nil, fmt.Errorf("migration: cleanup: prefix is required")
	}
	if c.Ledger == nil {
		return nil, fmt.Errorf("migration: cleanup: ledger is required")
	}
	if c.RDB == nil {
		return nil, fmt.Errorf("migration: cleanup: redis client is required")
	}
	now := c.Opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if err := c.Opts.Authorization.validate(now()); err != nil {
		return nil, err
	}
	if c.Opts.Mode != "" && c.Opts.Mode != c.Opts.Authorization.Run.Mode {
		return nil, fmt.Errorf("migration: cleanup requested mode does not match durable authorization")
	}
	items := c.Opts.Authorization.Items
	rate := c.Opts.RateLimitPerSec
	if rate > 0 {
		interval := time.Second / time.Duration(rate)
		tokens := time.NewTicker(interval)
		defer tokens.Stop()
		var results []CleanupResult
		for _, it := range items {
			select {
			case <-ctx.Done():
				return results, ctx.Err()
			case <-tokens.C:
			}
			res, err := c.processAuthorizedItem(ctx, it, now())
			results = append(results, res)
			if err != nil {
				return results, err
			}
			if err := c.Ledger.Append(res.Item); err != nil {
				return results, err
			}
		}
		return results, nil
	}
	var results []CleanupResult
	for _, it := range items {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}
		res, err := c.processAuthorizedItem(ctx, it, now())
		results = append(results, res)
		if err != nil {
			return results, err
		}
		if err := c.Ledger.Append(res.Item); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (c *Cleanup) processAuthorizedItem(ctx context.Context, it Item, now time.Time) (CleanupResult, error) {
	eligible := it.Classification == ClassificationMigratable && it.SourceKey != "" &&
		it.CanonicalKey != "" && it.SourceKey != it.CanonicalKey
	switch it.Status {
	case StatusCopied, StatusVerified, StatusObserved:
	default:
		eligible = false
	}
	claimed := false
	if eligible && c.Opts.Claim != nil {
		if err := c.Opts.Claim(it); err != nil {
			return CleanupResult{Item: it, Status: CleanupStatusRefused, Reason: "cleanup claim failed: " + err.Error()}, nil
		}
		claimed = true
	}
	res := c.cleanupOne(ctx, it, now)
	if res.Item.Status == StatusCleaned {
		if c.Opts.MarkCleaned != nil {
			if err := c.Opts.MarkCleaned(res.Item); err != nil {
				return res, err
			}
		}
	} else if claimed && c.Opts.ReleaseClaim != nil {
		if err := c.Opts.ReleaseClaim(it); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (c *Cleanup) cleanupOne(ctx context.Context, it Item, now time.Time) CleanupResult {
	res := CleanupResult{Item: it, Status: CleanupStatusSkipped}
	if it.Classification == ClassificationCanonicalPresent ||
		(it.CanonicalKey != "" && it.SourceKey == it.CanonicalKey) {
		res.Status = CleanupStatusPreserved
		res.Reason = "canonical source is never eligible for cleanup"
		return res
	}
	if it.Classification != ClassificationMigratable || it.SourceKey == "" || it.CanonicalKey == "" {
		res.Reason = "not a complete migratable source"
		return res
	}
	switch it.Status {
	case StatusCleaned:
		res.Reason = "already cleaned"
		return res
	case StatusCopied, StatusVerified, StatusObserved:
		// eligible for deletion
	default:
		res.Reason = fmt.Sprintf("status=%s not eligible", it.Status)
		return res
	}
	fields, err := c.RDB.HGetAll(ctx, it.SourceKey).Result()
	if err != nil {
		res.Status = CleanupStatusRefused
		res.Reason = "hgetall failed: " + err.Error()
		return res
	}
	if len(fields) == 0 {
		res.Status = CleanupStatusSkipped
		res.Reason = "source already absent"
		res.Item.Status = StatusCleaned
		return res
	}
	if it.FieldChecksum == "" || fieldChecksum(fields) != it.FieldChecksum {
		res.Status = CleanupStatusRefused
		res.Reason = "source checksum mismatch"
		return res
	}
	outcome, err := deleteHashIfUnchanged(ctx, c.RDB, it.SourceKey, fields)
	if err != nil {
		res.Status = CleanupStatusRefused
		res.Reason = "compare-delete failed: " + err.Error()
		return res
	}
	switch outcome {
	case compareDeleteDeleted:
		res.Status = CleanupStatusDeleted
		res.Item.Status = StatusCleaned
		res.Item.CleanedAtUnixMs = now.UnixMilli()
		return res
	case compareDeleteMissing:
		res.Status = CleanupStatusSkipped
		res.Reason = "source already absent"
		res.Item.Status = StatusCleaned
		return res
	case compareDeleteChanged:
		res.Status = CleanupStatusRefused
		res.Reason = "source checksum mismatch"
		return res
	case compareDeleteWrongType:
		res.Status = CleanupStatusRefused
		res.Reason = "source key is not a hash"
		return res
	default:
		res.Status = CleanupStatusRefused
		res.Reason = "compare-delete returned unknown result"
		return res
	}
}

// ============================================================================
// Per-entry exact-key cleaner retained from the feature branch (parallel
// API style: callers iterate the preflight output themselves rather than
// loading the ledger). Both implementations honour doc 14 §6.2 — exact key
// only, no SCAN+DEL — and verify the live checksum before deleting.
// ============================================================================

// CleanerEntryStatus is the per-entry outcome enum for the per-entry
// cleaner (parallel API style retained alongside Cleanup above).
type CleanerEntryStatus string

const (
	CleanerDeleted           CleanerEntryStatus = "deleted"
	CleanerSkippedChecksum   CleanerEntryStatus = "skipped_checksum"
	CleanerSkippedIneligible CleanerEntryStatus = "skipped_ineligible"
	CleanerSkippedMissing    CleanerEntryStatus = "skipped_missing"
)

// CleanerEntryResult is the per-entry outcome emitted by
// (*EntryCleaner).DeleteExact.
type CleanerEntryResult struct{ Status CleanerEntryStatus }

// CleanerBatchResult is the resumable summary emitted by
// (*EntryCleaner).DeleteBatch. Next is the index of the next entry to
// process on resume; Deleted is the count of source keys actually
// removed.
type CleanerBatchResult struct {
	Deleted int
	Next    int
}

// EntryCleaner is the parallel per-entry counterpart to Cleanup. It only
// receives entries already recorded by the authoritative ledger and has
// no SCAN method: broad SCAN+DEL cleanup is forbidden by doc 14 §6.2.
type EntryCleaner struct {
	rdb           *redis.Client
	authorization *CleanupAuthorization
}

// NewEntryCleaner builds a per-entry cleaner around the given Redis client.
// An authorization is required before an eligible entry can be deleted.
func NewEntryCleaner(rdb *redis.Client, authorization ...*CleanupAuthorization) *EntryCleaner {
	cleaner := &EntryCleaner{rdb: rdb}
	if len(authorization) > 0 {
		cleaner.authorization = authorization[0]
	}
	return cleaner
}

// DeleteExact deletes an exact source hash only after its current field
// checksum still equals the preflight ledger checksum. Any mutation after
// preflight preserves the source for review; cleanup never tries to merge
// canonical state backward into it.
func (c *EntryCleaner) DeleteExact(ctx context.Context, entry EntryRecord) (CleanerEntryResult, error) {
	if c == nil || c.rdb == nil {
		return CleanerEntryResult{}, fmt.Errorf("ursm.v2: cleanup requires redis")
	}
	if err := c.authorization.validate(time.Now().UTC()); err != nil {
		return CleanerEntryResult{Status: CleanerSkippedIneligible}, nil
	}
	if entry.Class != ClassificationMigratable || entry.SourceKey == "" || entry.TargetKey == "" || entry.FieldChecksum == "" ||
		entry.SourceKey == entry.TargetKey || !c.authorizes(entry) {
		return CleanerEntryResult{Status: CleanerSkippedIneligible}, nil
	}
	fields, err := c.rdb.HGetAll(ctx, entry.SourceKey).Result()
	if err != nil {
		return CleanerEntryResult{}, fmt.Errorf("ursm.v2: cleanup read %s: %w", entry.SourceKey, err)
	}
	if len(fields) == 0 {
		return CleanerEntryResult{Status: CleanerSkippedMissing}, nil
	}
	if checksumFields(fields) != entry.FieldChecksum {
		return CleanerEntryResult{Status: CleanerSkippedChecksum}, nil
	}
	outcome, err := deleteHashIfUnchanged(ctx, c.rdb, entry.SourceKey, fields)
	if err != nil {
		return CleanerEntryResult{}, fmt.Errorf("ursm.v2: cleanup compare-delete %s: %w", entry.SourceKey, err)
	}
	switch outcome {
	case compareDeleteDeleted:
		return CleanerEntryResult{Status: CleanerDeleted}, nil
	case compareDeleteMissing:
		return CleanerEntryResult{Status: CleanerSkippedMissing}, nil
	case compareDeleteChanged, compareDeleteWrongType:
		return CleanerEntryResult{Status: CleanerSkippedChecksum}, nil
	default:
		return CleanerEntryResult{}, fmt.Errorf("ursm.v2: cleanup compare-delete %s returned %q", entry.SourceKey, outcome)
	}
}

func (c *EntryCleaner) authorizes(entry EntryRecord) bool {
	for _, item := range c.authorization.Items {
		if item.SourceKey == entry.SourceKey && item.CanonicalKey == entry.TargetKey &&
			item.FieldChecksum == entry.FieldChecksum && item.Classification == entry.Class &&
			item.Status == StatusCopied {
			return true
		}
	}
	return false
}

// DeleteBatch processes at most limit ledger entries, allowing an operator
// to pause after any batch and resume from CleanerBatchResult.Next. Limit
// <= 0 is rejected so a caller cannot accidentally request unbounded
// cleanup.
func (c *EntryCleaner) DeleteBatch(ctx context.Context, entries []EntryRecord, limit int) (CleanerBatchResult, error) {
	if limit <= 0 {
		return CleanerBatchResult{}, fmt.Errorf("ursm.v2: cleanup batch limit must be positive")
	}
	result := CleanerBatchResult{}
	for i, entry := range entries {
		if i >= limit {
			result.Next = i
			return result, nil
		}
		outcome, err := c.DeleteExact(ctx, entry)
		if err != nil {
			return result, err
		}
		if outcome.Status == CleanerDeleted {
			result.Deleted++
		}
		result.Next = i + 1
	}
	return result, nil
}
