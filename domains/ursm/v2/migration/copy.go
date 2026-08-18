package migration

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrGenerationChanged = errors.New("ursm.v2: source generation changed")
	ErrTargetConflict    = errors.New("ursm.v2: canonical target conflict")
	ErrChecksumChanged   = errors.New("ursm.v2: source checksum changed")
)

type CopyStatus string

const (
	CopyApplied        CopyStatus = "applied"
	CopyAlreadyApplied CopyStatus = "already_applied"
	CopySkippedExpired CopyStatus = "skipped_expired"
)

type CopyResult struct{ Status CopyStatus }

//go:embed copy_hash.lua
var copyHashSrc string

var copyHashScript = redis.NewScript(copyHashSrc)

type Copier struct {
	rdb    *redis.Client
	prefix string
}

func NewCopier(rdb *redis.Client, prefix string) *Copier {
	return &Copier{rdb: rdb, prefix: prefix}
}

// CopyHash copies one preflighted HASH using generation + serialized-value
// fencing. PTTL is read just before EVAL and elapsed time is subtracted from
// the source's remaining lifetime; the Lua script clamps the target again
// against a second PTTL read, so copy latency cannot extend state lifetime.
func (c *Copier) CopyHash(ctx context.Context, entry Entry) (CopyResult, error) {
	if c == nil || c.rdb == nil {
		return CopyResult{}, fmt.Errorf("ursm.v2: copy requires redis")
	}
	if entry.Class != ClassMigratable || entry.TargetKey == "" {
		return CopyResult{}, fmt.Errorf("ursm.v2: copy entry is not migratable")
	}
	started := time.Now()
	pttl, err := c.rdb.PTTL(ctx, entry.SourceKey).Result()
	if err != nil {
		return CopyResult{}, fmt.Errorf("ursm.v2: copy source pttl: %w", err)
	}
	if pttl == -2*time.Nanosecond {
		return CopyResult{Status: CopySkippedExpired}, nil
	}
	if pttl == 0 {
		return CopyResult{Status: CopySkippedExpired}, nil
	}
	if pttl < 0 && pttl != -1*time.Nanosecond {
		return CopyResult{}, fmt.Errorf("ursm.v2: invalid source pttl %v", pttl)
	}
	fields, err := c.rdb.HGetAll(ctx, entry.SourceKey).Result()
	if err != nil {
		return CopyResult{}, fmt.Errorf("ursm.v2: copy source fields: %w", err)
	}
	if len(fields) == 0 {
		return CopyResult{Status: CopySkippedExpired}, nil
	}
	if entry.Generation != parseGeneration(fields["generation"]) {
		return CopyResult{}, ErrGenerationChanged
	}
	if checksumFields(fields) != entry.FieldChecksum {
		return CopyResult{}, ErrChecksumChanged
	}
	// Account for the time spent reading source state before EVAL. For a
	// persistent source pass 0; Lua re-reads PTTL and preserves persistence.
	requestedTTL := int64(0)
	if pttl > 0 {
		remaining := pttl - time.Since(started)
		if remaining <= 0 {
			return CopyResult{Status: CopySkippedExpired}, nil
		}
		requestedTTL = remaining.Milliseconds()
		if requestedTTL <= 0 {
			return CopyResult{Status: CopySkippedExpired}, nil
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
	result, err := copyHashScript.Run(ctx, c.rdb,
		[]string{entry.SourceKey, entry.TargetKey, marker}, args...).Text()
	if err != nil {
		return CopyResult{}, fmt.Errorf("ursm.v2: copy eval: %w", err)
	}
	switch result {
	case "applied":
		return CopyResult{Status: CopyApplied}, nil
	case "already":
		return CopyResult{Status: CopyAlreadyApplied}, nil
	case "expired":
		return CopyResult{Status: CopySkippedExpired}, nil
	case "generation_changed":
		return CopyResult{}, ErrGenerationChanged
	case "checksum_changed":
		return CopyResult{}, ErrChecksumChanged
	case "conflict":
		return CopyResult{}, ErrTargetConflict
	default:
		return CopyResult{}, fmt.Errorf("ursm.v2: unknown copy result %q", result)
	}
}
