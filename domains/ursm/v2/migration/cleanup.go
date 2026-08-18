package migration

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type CleanupStatus string

const (
	CleanupDeleted           CleanupStatus = "deleted"
	CleanupSkippedChecksum   CleanupStatus = "skipped_checksum"
	CleanupSkippedIneligible CleanupStatus = "skipped_ineligible"
	CleanupSkippedMissing    CleanupStatus = "skipped_missing"
)

type CleanupResult struct{ Status CleanupStatus }

type BatchResult struct {
	Deleted int
	Next    int
}

// Cleaner only receives entries already recorded by the authoritative
// ledger. It deliberately has no SCAN method: broad `SCAN ... DEL` cleanup
// is forbidden by doc 14 §6.2.
type Cleaner struct{ rdb *redis.Client }

func NewCleaner(rdb *redis.Client) *Cleaner { return &Cleaner{rdb: rdb} }

// DeleteExact deletes an exact source hash only after its current field
// checksum still equals the preflight ledger checksum. Any mutation after
// preflight preserves the source for review; cleanup never tries to merge
// canonical state backward into it.
func (c *Cleaner) DeleteExact(ctx context.Context, entry Entry) (CleanupResult, error) {
	if c == nil || c.rdb == nil {
		return CleanupResult{}, fmt.Errorf("ursm.v2: cleanup requires redis")
	}
	if entry.Class != ClassMigratable || entry.SourceKey == "" || entry.FieldChecksum == "" {
		return CleanupResult{Status: CleanupSkippedIneligible}, nil
	}
	fields, err := c.rdb.HGetAll(ctx, entry.SourceKey).Result()
	if err != nil {
		return CleanupResult{}, fmt.Errorf("ursm.v2: cleanup read %s: %w", entry.SourceKey, err)
	}
	if len(fields) == 0 {
		return CleanupResult{Status: CleanupSkippedMissing}, nil
	}
	if checksumFields(fields) != entry.FieldChecksum {
		return CleanupResult{Status: CleanupSkippedChecksum}, nil
	}
	// Exact source key only. There is intentionally no prefix/pattern
	// expansion and no delete of derived or canonical keys.
	if err := c.rdb.Del(ctx, entry.SourceKey).Err(); err != nil {
		return CleanupResult{}, fmt.Errorf("ursm.v2: cleanup delete %s: %w", entry.SourceKey, err)
	}
	return CleanupResult{Status: CleanupDeleted}, nil
}

// DeleteBatch processes at most limit ledger entries, allowing an operator
// to pause after any batch and resume from BatchResult.Next. Limit <= 0 is
// rejected so a caller cannot accidentally request unbounded cleanup.
func (c *Cleaner) DeleteBatch(ctx context.Context, entries []Entry, limit int) (BatchResult, error) {
	if limit <= 0 {
		return BatchResult{}, fmt.Errorf("ursm.v2: cleanup batch limit must be positive")
	}
	result := BatchResult{}
	for i, entry := range entries {
		if i >= limit {
			result.Next = i
			return result, nil
		}
		outcome, err := c.DeleteExact(ctx, entry)
		if err != nil {
			return result, err
		}
		if outcome.Status == CleanupDeleted {
			result.Deleted++
		}
		result.Next = i + 1
	}
	return result, nil
}
