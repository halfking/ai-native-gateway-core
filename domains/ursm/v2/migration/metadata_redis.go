package migration

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// MetadataKey is the Redis HASH key used for the k2 migration metadata.
// It lives under the same prefix as URSM v2 state but in a dedicated
// namespace ("mig:k2:meta") so we never collide with the existing
// "meta:ready" / "meta:epoch" / "meta:coverage" / "meta:node_invalidation"
// keys. Naming is part of the ledger column registry that 14 §3 defers to
// the implementation to freeze.
func MetadataKey(prefix string) string {
	return prefix + "mig:k2:meta"
}

// MetadataHash wraps the Redis HASH write/read operations for Metadata.
// Independent type from the LUA-CAS MetadataStore in metadata.go: this one
// exposes plain Write/Read plus a CASCheckpoint Lua script, while
// MetadataStore owns the epoch-fenced Advance / idempotent Initialize path.
type MetadataHash struct {
	Prefix string
	RDB    *redis.Client
}

// Write stores the metadata fields atomically. It uses HSET (not HSETNX) so
// the operator can re-publish a corrected snapshot; the immutable contract
// is the ledger_id (one-shot, doc 14 §0).
func (s *MetadataHash) Write(ctx context.Context, m Metadata) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if s.Prefix == "" || s.RDB == nil {
		return fmt.Errorf("migration: metadata store: missing prefix/redis client")
	}
	values := map[string]any{
		"owner":              m.Owner,
		"ledger_id":          m.LedgerID,
		"mode":               string(m.Mode),
		"cutover_epoch":      fmt.Sprintf("%d", m.CutoverEpoch),
		"started_at":         m.StartedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":         m.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"preflight_checksum": m.PreflightChecksum,
		"checkpoint":         string(m.Checkpoint),
	}
	if !m.RollbackDeadline.IsZero() {
		values["rollback_deadline"] = m.RollbackDeadline.UTC().Format(time.RFC3339Nano)
	}
	if err := s.RDB.HSet(ctx, MetadataKey(s.Prefix), values).Err(); err != nil {
		return fmt.Errorf("migration: hset metadata: %w", err)
	}
	return nil
}

// Read loads the metadata HASH. A missing key returns (zero, nil).
func (s *MetadataHash) Read(ctx context.Context) (Metadata, error) {
	if s.Prefix == "" || s.RDB == nil {
		return Metadata{}, fmt.Errorf("migration: metadata store: missing prefix/redis client")
	}
	values, err := s.RDB.HGetAll(ctx, MetadataKey(s.Prefix)).Result()
	if err != nil {
		return Metadata{}, fmt.Errorf("migration: hgetall metadata: %w", err)
	}
	if len(values) == 0 {
		return Metadata{}, nil
	}
	m, err := decodeMetadata(values)
	if err != nil {
		return Metadata{}, err
	}
	return m, nil
}

// CASCheckpoint performs a Lua-driven compare-and-swap on the cutover_epoch
// + checkpoint pair so concurrent migration runs cannot stomp on each
// other (doc 14 §4 state machine, doc 15 §4). The Lua returns 1 when the
// CAS succeeds, 0 otherwise.
func (s *MetadataHash) CASCheckpoint(ctx context.Context, expectedEpoch int64, newCheckpoint Checkpoint, now time.Time) error {
	if s.Prefix == "" || s.RDB == nil {
		return fmt.Errorf("migration: metadata store: missing prefix/redis client")
	}
	if !newCheckpoint.Valid() {
		return fmt.Errorf("migration: cas invalid checkpoint %q", newCheckpoint)
	}
	const script = `
		local cur = redis.call('HGET', KEYS[1], 'cutover_epoch')
		if cur ~= false and tonumber(cur) ~= tonumber(ARGV[1]) then
			return 0
		end
		redis.call('HSET', KEYS[1],
			'checkpoint', ARGV[2],
			'updated_at', ARGV[3])
		return 1
	`
	res, err := s.RDB.Eval(ctx, script, []string{MetadataKey(s.Prefix)},
		fmt.Sprintf("%d", expectedEpoch),
		string(newCheckpoint),
		now.UTC().Format(time.RFC3339Nano),
	).Int64()
	if err != nil {
		return fmt.Errorf("migration: cas checkpoint: %w", err)
	}
	if res != 1 {
		return fmt.Errorf("migration: cas checkpoint: epoch mismatch (expected %d)", expectedEpoch)
	}
	return nil
}

func decodeMetadata(values map[string]string) (Metadata, error) {
	get := func(k string) string { return values[k] }
	var (
		m   Metadata
		err error
	)
	m.Owner = get("owner")
	m.LedgerID = get("ledger_id")
	m.Mode = Mode(get("mode"))
	m.Checkpoint = Checkpoint(get("checkpoint"))
	if v := get("cutover_epoch"); v != "" {
		var n int64
		for _, r := range v {
			if r < '0' || r > '9' {
				return Metadata{}, fmt.Errorf("migration: cutover_epoch not numeric: %q", v)
			}
			n = n*10 + int64(r-'0')
		}
		m.CutoverEpoch = n
	}
	if v := get("started_at"); v != "" {
		m.StartedAt, err = time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return Metadata{}, fmt.Errorf("migration: started_at: %w", err)
		}
	}
	if v := get("updated_at"); v != "" {
		m.UpdatedAt, err = time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return Metadata{}, fmt.Errorf("migration: updated_at: %w", err)
		}
	}
	if v := get("rollback_deadline"); v != "" {
		m.RollbackDeadline, err = time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return Metadata{}, fmt.Errorf("migration: rollback_deadline: %w", err)
		}
	}
	m.PreflightChecksum = get("preflight_checksum")
	if err := m.Validate(); err != nil {
		return Metadata{}, err
	}
	return m, nil
}