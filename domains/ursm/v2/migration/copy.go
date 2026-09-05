package migration

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"
)

// CopyStatus is the per-item outcome enum for the copy phase (doc 14 §6.1,
// 15 §4). It is independent from Classification because a migratable item
// can still end up Skipped or Failed when its live state disagrees with the
// preflight snapshot (e.g., source expired, generation mismatch).
type CopyStatus string

const (
	CopyStatusCopied    CopyStatus = "copied"
	CopyStatusSkipped   CopyStatus = "skipped"
	CopyStatusFailed    CopyStatus = "failed"
	CopyStatusRefused   CopyStatus = "refused"
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
//     more than the PTTL observed at scan time
//     (this prevents copy from extending live state)
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
	if it.Classification != ClassificationMigratable {
		res.Reason = "not migratable"
		return res
	}
	if it.SourceKey == "" || it.CanonicalKey == "" {
		res.Status = CopyStatusRefused
		res.Reason = "missing source or canonical key"
		return res
	}
	if it.SourceKey == it.CanonicalKey {
		res.Status = CopyStatusRefused
		res.Reason = "source key equals canonical key"
		return res
	}
	// PTTL probe (doc 14 §6.1). Normalize Redis protocol sentinels before
	// branching; go-redis may represent -1/-2 as -1ns/-2ns while test
	// clients commonly return millisecond durations.
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
	sourcePTTLMs := pttlMillis(pttl)
	res.SourcePTTLMs = sourcePTTLMs
	switch {
	case sourcePTTLMs == -2:
		res.Status = CopyStatusSkipped
		res.Reason = "source missing (pttl=-2)"
		return res
	case sourcePTTLMs == -1:
		// Preserve no-TTL behaviour.
	case sourcePTTLMs <= 0:
		res.Status = CopyStatusSkipped
		res.Reason = "source expired (pttl<=0)"
		return res
	case it.PTTLMs > 0 && sourcePTTLMs > it.PTTLMs:
		res.Status = CopyStatusRefused
		res.Reason = fmt.Sprintf("pttl grew past scan snapshot: now=%d scan=%d", sourcePTTLMs, it.PTTLMs)
		return res
	}

	// Snapshot the source fields; enforce generation / checksum fencing
	// before mutating the target.
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	fields, err := redissafe.SafeHGetAll(ctx, c.RDB, it.SourceKey)
	if err != nil {
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			res.Status = CopyStatusSkipped
			res.Reason = "source expired before hash read"
			return res
		}
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
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	tgtFields, targetErr := redissafe.SafeHGetAll(ctx, c.RDB, it.CanonicalKey)
	if targetErr != nil && !errors.Is(targetErr, redissafe.ErrKeyNotFound) {
		res.Status = CopyStatusRefused
		res.Reason = "canonical target read failed: " + targetErr.Error()
		return res
	}
	if targetErr == nil && len(tgtFields) > 0 {
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
		anyMap := make(map[string]any, len(fields))
		for k, v := range fields {
			anyMap[k] = v
		}
		pipe.HSet(ctx, it.CanonicalKey, anyMap)
	}
	switch {
	case sourcePTTLMs == -1:
		// no TTL: do not call PEXPIRE
	case sourcePTTLMs > 0:
		// Never extend the source lifetime beyond the scan snapshot.
		cap := sourcePTTLMs
		if it.PTTLMs > 0 && cap > it.PTTLMs {
			cap = it.PTTLMs
		}
		if cap <= 0 {
			res.Status = CopyStatusSkipped
			res.Reason = "pttl decayed to <=0 since scan"
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

// ============================================================================
// Per-entry copier retained from the feature branch (parallel API style:
// callers iterate the preflight output themselves rather than loading the
// ledger). Both implementations honour doc 14 §6.1 — generation + field
// checksum fencing, TTL preservation via PTTL subtraction.
// ============================================================================

var (
	ErrEntryGenerationChanged = errors.New("ursm.v2: source generation changed")
	ErrEntryTargetConflict    = errors.New("ursm.v2: canonical target conflict")
	ErrEntryChecksumChanged   = errors.New("ursm.v2: source checksum changed")
)

// EntryCopyStatus is the per-entry outcome enum for the per-entry copier.
// EntryCopyStatus is the per-entry outcome enum for the per-entry copier
// (parallel API style retained alongside Copy in this file).
type EntryCopyStatus string

const (
	EntryCopyApplied        EntryCopyStatus = "applied"
	EntryCopyAlreadyApplied EntryCopyStatus = "already_applied"
	EntryCopySkippedExpired EntryCopyStatus = "skipped_expired"
)

// EntryCopyResult is the per-entry update emitted by EntryCopier.CopyHash.
// EntryCopyResult is the per-entry update emitted by
// (*EntryCopier).CopyHash. Status captures the LUA-side outcome
// (applied / already_applied / skipped_expired).
type EntryCopyResult struct{ Status EntryCopyStatus }

//go:embed copy_hash.lua
var copyHashSrc string

var copyHashScript = redis.NewScript(copyHashSrc)

// EntryCopier copies one preflighted HASH at a time, using generation +
// serialized-value fencing. PTTL is read just before EVAL and elapsed time
// is subtracted from the source's remaining lifetime; the Lua script clamps
// the target again against a second PTTL read, so copy latency cannot
// extend state lifetime.
type EntryCopier struct {
	rdb    *redis.Client
	prefix string
}

// NewEntryCopier builds a per-entry copier bound to prefix (used by the
// Lua script's canonical-key derivation).
func NewEntryCopier(rdb *redis.Client, prefix string) *EntryCopier {
	return &EntryCopier{rdb: rdb, prefix: prefix}
}

// CopyHash copies one preflighted HASH using generation + serialized-value
// fencing. PTTL is read just before EVAL and elapsed time is subtracted
// from the source's remaining lifetime; the Lua script clamps the target
// again against a second PTTL read, so copy latency cannot extend state
// lifetime.
func (c *EntryCopier) CopyHash(ctx context.Context, entry EntryRecord) (EntryCopyResult, error) {
	if c == nil || c.rdb == nil {
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: copy requires redis")
	}
	if entry.Class != ClassificationMigratable || entry.SourceKey == "" || entry.TargetKey == "" ||
		entry.SourceKey == entry.TargetKey {
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: copy entry is not a distinct migratable source")
	}
	started := time.Now()
	pttl, err := c.rdb.PTTL(ctx, entry.SourceKey).Result()
	if err != nil {
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: copy source pttl: %w", err)
	}
	sourcePTTLMs := pttlMillis(pttl)
	switch {
	case sourcePTTLMs == -2:
		return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
	case sourcePTTLMs == 0:
		return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
	case sourcePTTLMs < -1:
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: invalid source pttl %v", pttl)
	}
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	fields, err := redissafe.SafeHGetAll(ctx, c.rdb, entry.SourceKey)
	if err != nil {
		// SafeHGetAll maps a key that expired between the PTTL probe and the
		// read to ErrKeyNotFound; keep the bare-HGETALL contract (empty map →
		// skipped-expired) instead of escalating routine expiry to an error.
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
		}
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: copy source fields: %w", err)
	}
	if len(fields) == 0 {
		return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
	}
	if entry.Generation != parseGeneration(fields["generation"]) {
		return EntryCopyResult{}, ErrEntryGenerationChanged
	}
	if checksumFields(fields) != entry.FieldChecksum {
		return EntryCopyResult{}, ErrEntryChecksumChanged
	}
	// Account for the time spent reading source state before EVAL. For a
	// persistent source pass 0; Lua re-reads PTTL and preserves persistence.
	requestedTTL := int64(0)
	if sourcePTTLMs > 0 {
		remaining := time.Duration(sourcePTTLMs)*time.Millisecond - time.Since(started)
		if remaining <= 0 {
			return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
		}
		requestedTTL = remaining.Milliseconds()
		if requestedTTL <= 0 {
			return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
		}
	}
	args := make([]interface{}, 0, 3+len(fields)*2)
	args = append(args, fmt.Sprintf("%d", entry.Generation), fmt.Sprintf("%d", requestedTTL), fmt.Sprintf("%d", len(fields)))
	fieldNames := make([]string, 0, len(fields))
	for field := range fields {
		fieldNames = append(fieldNames, field)
	}
	sort.Strings(fieldNames)
	for _, field := range fieldNames {
		args = append(args, field, fields[field])
	}
	marker := entry.TargetKey + ":migration_copy:" + entry.FieldChecksum
	result, err := redissafe.RunScript(ctx, c.rdb, copyHashScript, "copy_hash.lua",
		[]string{entry.SourceKey, entry.TargetKey, marker}, args...).Text()
	if err != nil {
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: copy eval: %w", err)
	}
	switch result {
	case "applied":
		return EntryCopyResult{Status: EntryCopyApplied}, nil
	case "already":
		return EntryCopyResult{Status: EntryCopyAlreadyApplied}, nil
	case "expired":
		return EntryCopyResult{Status: EntryCopySkippedExpired}, nil
	case "generation_changed":
		return EntryCopyResult{}, ErrEntryGenerationChanged
	case "checksum_changed":
		return EntryCopyResult{}, ErrEntryChecksumChanged
	case "conflict":
		return EntryCopyResult{}, ErrEntryTargetConflict
	default:
		return EntryCopyResult{}, fmt.Errorf("ursm.v2: unknown copy result %q", result)
	}
}
