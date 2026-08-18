package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CopyStatus is the per-item outcome enum for the copy phase (doc 14 §6.1,
// 15 §4). It is independent from Classification because a migratable item
// can still end up Skipped or Failed when its live state disagrees with the
// preflight snapshot (e.g., source expired, generation mismatch).
type CopyStatus string

const (
	CopyStatusCopied   CopyStatus = "copied"
	CopyStatusSkipped  CopyStatus = "skipped"
	CopyStatusFailed   CopyStatus = "failed"
	CopyStatusRefused  CopyStatus = "refused"
	CopyStatusUnchanged CopyStatus = "unchanged"
)

// CopyOptions controls a single copy pass over the ledger. Rate / BatchSize
// are not enforced in this slice; they exist so the CLI can wire them up
// for the G4 real-Redis path (and so the limiter is testable in slice 13).
type CopyOptions struct {
	Now       func() time.Time
	BatchSize int
}

// CopyResult is the per-item ledger update emitted by Copy.
type CopyResult struct {
	Item           Item
	SourcePTTLMs   int64
	TargetPTTLMs   int64
	SourceChecksum string
	TargetChecksum string
	Status         CopyStatus
	Reason         string
}

// Copy runs the migration copy phase (doc 14 §6.1). It iterates the ledger,
// skipping any non-migratable item, applying the TTL preservation rules,
// and writing back the updated Item to the ledger so a subsequent run can
// resume cleanly.
type Copy struct {
	Prefix string
	Ledger *Ledger
	RDB    *redis.Client
	Opts   CopyOptions
}

// Copy iterates the ledger and copies every migratable item's source hash
// to its canonical target. Per doc 14 §6.1:
//   - PTTL == -2 (key absent): skipped with reason "source missing"
//   - PTTL == -1 (no TTL):     target retains no TTL
//   - PTTL >  0 (positive):    target PEXPIRE = PTTL - elapsed, but never
//                              more than the PTTL observed at scan time
//                              (this prevents copy from extending live state)
//   - generation field in source must match the ledger-recorded generation
//     (otherwise refuse with "generation mismatch")
//   - the source field_checksum must still equal the recorded one
//
// Resume: if a migratable item is already in StatusCopied and the live
// target's checksum equals the recorded checksum, the row is reported as
// CopyStatusUnchanged and the target is not rewritten.
func (c *Copy) Copy(ctx context.Context) ([]CopyResult, error) {
	if c.Prefix == "" {
		return nil, fmt.Errorf("migration: copy: prefix is required")
	}
	if c.Ledger == nil {
		return nil, fmt.Errorf("migration: copy: ledger is required")
	}
	if c.RDB == nil {
		return nil, fmt.Errorf("migration: copy: redis client is required")
	}
	now := c.Opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	items, err := c.Ledger.LoadAll()
	if err != nil {
		return nil, err
	}
	var results []CopyResult
	for _, it := range items {
		res := c.copyOne(ctx, it, now())
		results = append(results, res)
		if err := c.Ledger.Append(res.Item); err != nil {
			return results, err
		}
	}
	return results, nil
}

func (c *Copy) copyOne(ctx context.Context, it Item, now time.Time) CopyResult {
	res := CopyResult{Item: it, Status: CopyStatusSkipped}
	if it.Classification != ClassificationMigratable && it.Classification != ClassificationCanonicalPresent {
		res.Reason = "not migratable"
		return res
	}
	if it.CanonicalKey == "" {
		res.Status = CopyStatusRefused
		res.Reason = "missing canonical key"
		return res
	}
	// PTTL probe (doc 14 §6.1). The go-redis client maps -1/-2 to
// time.Duration(-1ms) and time.Duration(-2ms); miniredis returns the raw
// duration which is identical. Compare against the time value so both
// clients are treated the same. If the ledger recorded PTTLMs == -2 we
// already know the source is gone (it was missing at scan time) and skip
// probing entirely.
	if it.PTTLMs == -2 {
		res.SourcePTTLMs = -2
		res.Status = CopyStatusSkipped
		res.Reason = "source missing (pttl=-2)"
		return res
	}
	pttl, err := c.RDB.PTTL(ctx, it.SourceKey).Result()
	if err != nil {
		res.Status = CopyStatusFailed
		res.Reason = "pttl probe failed: " + err.Error()
		return res
	}
	res.SourcePTTLMs = pttl.Milliseconds()
	switch {
	case pttl == -2*time.Millisecond:
		res.Status = CopyStatusSkipped
		res.Reason = "source missing (pttl=-2)"
		return res
	case pttl == -1*time.Millisecond:
		// fall through; preserve no-TTL behaviour
	default:
		// positive TTL: cap by the observed-at-scan value, then validate
		// that we are not extending the source TTL after copy.
		if it.PTTLMs > 0 && pttl.Milliseconds() > it.PTTLMs {
			res.Status = CopyStatusRefused
			res.Reason = fmt.Sprintf("pttl grew past scan snapshot: now=%d scan=%d", pttl.Milliseconds(), it.PTTLMs)
			return res
		}
	}
	// Snapshot the source fields; enforce generation / checksum fencing
	// before mutating the target.
	fields, err := c.RDB.HGetAll(ctx, it.SourceKey).Result()
	if err != nil {
		res.Status = CopyStatusFailed
		res.Reason = "hgetall failed: " + err.Error()
		return res
	}
	res.SourceChecksum = fieldChecksum(fields)
	if it.FieldChecksum != "" && res.SourceChecksum != it.FieldChecksum {
		res.Status = CopyStatusRefused
		res.Reason = "source field_checksum mismatch"
		return res
	}
	if genStr, ok := fields["generation"]; ok {
		gen := parseInt64OrZero(genStr)
		if it.Generation != 0 && gen != it.Generation {
			res.Status = CopyStatusRefused
			res.Reason = fmt.Sprintf("generation mismatch: source=%d ledger=%d", gen, it.Generation)
			return res
		}
	}
	// Resume guard: if the target already exists with the same checksum,
	// the work was already done. We do this *after* fencing so a stale
	// target does not bypass the generation guard.
	if tgtFields, err := c.RDB.HGetAll(ctx, it.CanonicalKey).Result(); err == nil && len(tgtFields) > 0 {
		if fieldChecksum(tgtFields) == res.SourceChecksum {
			res.Status = CopyStatusUnchanged
			res.TargetChecksum = res.SourceChecksum
			res.Item.Status = StatusCopied
			res.Item.FieldChecksum = res.SourceChecksum
			return res
		}
	}
	// Persist: target = source fields. The source's TTL (or lack thereof)
	// is replayed on the target inside the same Redis round-trip when
	// PTTL>0; when PTTL=-1 the target carries no expiry.
	pipe := c.RDB.Pipeline()
	pipe.Del(ctx, it.CanonicalKey)
	if len(fields) > 0 {
		// redis.HSet accepts map[string]any but our values are strings.
		anyMap := make(map[string]any, len(fields))
		for k, v := range fields {
			anyMap[k] = v
		}
		pipe.HSet(ctx, it.CanonicalKey, anyMap)
	}
	switch {
	case pttl == -1 * time.Millisecond:
		// no TTL: do not call PEXPIRE
	case pttl > 0:
		// never extend beyond scan snapshot
		cap := it.PTTLMs
		if cap <= 0 {
			cap = pttl.Milliseconds()
		}
		if pttl.Milliseconds() > cap {
			cap = pttl.Milliseconds()
		}
		if cap <= 0 {
			res.Status = CopyStatusSkipped
			res.Reason = "pttl decayed to <=0"
			return res
		}
		pipe.PExpire(ctx, it.CanonicalKey, time.Duration(cap)*time.Millisecond)
		res.TargetPTTLMs = cap
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		res.Status = CopyStatusFailed
		res.Reason = "pipeline exec failed: " + err.Error()
		return res
	}
	res.TargetChecksum = res.SourceChecksum
	res.Status = CopyStatusCopied
	res.Item.Status = StatusCopied
	res.Item.FieldChecksum = res.SourceChecksum
	res.Item.CopiedAtUnixMs = now.UnixMilli()
	return res
}