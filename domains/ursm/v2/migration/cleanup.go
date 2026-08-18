package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CleanupStatus is the per-item outcome for the cleanup phase (doc 14 §6.2).
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
	// ForceRollback when true allows cleanup to proceed even when
	// Mode=legacy. Off by default: rollback preserves evidence (doc 14 §6.3).
	ForceRollback bool
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
	if c.Opts.Mode == ModeLegacy && !c.Opts.ForceRollback {
		return nil, fmt.Errorf("migration: cleanup refused in mode=legacy (use --force-rollback to override; rollback preserves evidence per doc 14 §6.3)")
	}
	now := c.Opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	items, err := c.Ledger.LoadAll()
	if err != nil {
		return nil, err
	}
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
			res := c.cleanupOne(ctx, it, now())
			results = append(results, res)
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
		res := c.cleanupOne(ctx, it, now())
		results = append(results, res)
		if err := c.Ledger.Append(res.Item); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (c *Cleanup) cleanupOne(ctx context.Context, it Item, now time.Time) CleanupResult {
	res := CleanupResult{Item: it, Status: CleanupStatusSkipped}
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
	exists, err := c.RDB.Exists(ctx, it.SourceKey).Result()
	if err != nil {
		res.Status = CleanupStatusRefused
		res.Reason = "exists probe failed: " + err.Error()
		return res
	}
	if exists == 0 {
		res.Status = CleanupStatusSkipped
		res.Reason = "source already absent"
		res.Item.Status = StatusCleaned
		return res
	}
	// Live checksum must match the ledger's snapshot, otherwise the live
	// state has drifted and we refuse to delete (doc 14 §6.2: cleanup is
	// exact-key + checksum).
	fields, err := c.RDB.HGetAll(ctx, it.SourceKey).Result()
	if err != nil {
		res.Status = CleanupStatusRefused
		res.Reason = "hgetall failed: " + err.Error()
		return res
	}
	if it.FieldChecksum != "" && fieldChecksum(fields) != it.FieldChecksum {
		res.Status = CleanupStatusRefused
		res.Reason = "source checksum mismatch"
		return res
	}
	if err := c.RDB.Del(ctx, it.SourceKey).Err(); err != nil {
		res.Status = CleanupStatusRefused
		res.Reason = "del failed: " + err.Error()
		return res
	}
	res.Status = CleanupStatusDeleted
	res.Item.Status = StatusCleaned
	res.Item.CleanedAtUnixMs = now.UnixMilli()
	return res
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

type CleanerEntryResult struct{ Status CleanerEntryStatus }

type CleanerBatchResult struct {
	Deleted int
	Next    int
}

// EntryCleaner is the parallel per-entry counterpart to Cleanup. It only
// receives entries already recorded by the authoritative ledger and has
// no SCAN method: broad SCAN+DEL cleanup is forbidden by doc 14 §6.2.
type EntryCleaner struct{ rdb *redis.Client }

// NewEntryCleaner builds a per-entry cleaner around the given Redis client.
func NewEntryCleaner(rdb *redis.Client) *EntryCleaner { return &EntryCleaner{rdb: rdb} }

// DeleteExact deletes an exact source hash only after its current field
// checksum still equals the preflight ledger checksum. Any mutation after
// preflight preserves the source for review; cleanup never tries to merge
// canonical state backward into it.
func (c *EntryCleaner) DeleteExact(ctx context.Context, entry Entry) (CleanerEntryResult, error) {
	if c == nil || c.rdb == nil {
		return CleanerEntryResult{}, fmt.Errorf("ursm.v2: cleanup requires redis")
	}
	if entry.Class != ClassMigratable || entry.SourceKey == "" || entry.FieldChecksum == "" {
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
	// Exact source key only. There is intentionally no prefix/pattern
	// expansion and no delete of derived or canonical keys.
	if err := c.rdb.Del(ctx, entry.SourceKey).Err(); err != nil {
		return CleanerEntryResult{}, fmt.Errorf("ursm.v2: cleanup delete %s: %w", entry.SourceKey, err)
	}
	return CleanerEntryResult{Status: CleanerDeleted}, nil
}

// DeleteBatch processes at most limit ledger entries, allowing an operator
// to pause after any batch and resume from CleanerBatchResult.Next. Limit
// <= 0 is rejected so a caller cannot accidentally request unbounded
// cleanup.
func (c *EntryCleaner) DeleteBatch(ctx context.Context, entries []Entry, limit int) (CleanerBatchResult, error) {
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
