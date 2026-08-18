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
// exposes identity-CAS Write/Read plus a CASCheckpoint Lua script, while
// MetadataStore owns the epoch-fenced Advance / idempotent Initialize path.

var metadataIdentityWriteScript = redis.NewScript(`
local owner = redis.call('HGET', KEYS[1], 'owner')
local ledger = redis.call('HGET', KEYS[1], 'ledger_id')
if (owner == false) ~= (ledger == false) then
  return redis.error_reply('incomplete migration metadata identity')
end
if owner ~= false then
  if owner ~= ARGV[1] or ledger ~= ARGV[2] or
     redis.call('HGET', KEYS[1], 'mode') ~= ARGV[3] or
     redis.call('HGET', KEYS[1], 'cutover_epoch') ~= ARGV[4] or
     redis.call('HGET', KEYS[1], 'started_at') ~= ARGV[5] or
     redis.call('HGET', KEYS[1], 'preflight_checksum') ~= ARGV[7] or
     redis.call('HGET', KEYS[1], 'checkpoint') ~= ARGV[8] or
     (redis.call('HGET', KEYS[1], 'rollback_deadline') or '') ~= ARGV[9] then
    return 'conflict'
  end
end
redis.call('HSET', KEYS[1],
  'owner', ARGV[1], 'ledger_id', ARGV[2], 'mode', ARGV[3],
  'cutover_epoch', ARGV[4], 'started_at', ARGV[5], 'updated_at', ARGV[6],
  'preflight_checksum', ARGV[7], 'checkpoint', ARGV[8])
if ARGV[9] == '' then
  redis.call('HDEL', KEYS[1], 'rollback_deadline')
else
  redis.call('HSET', KEYS[1], 'rollback_deadline', ARGV[9])
end
return 'ok'
`)

type MetadataHash struct {
	Prefix string
	RDB    *redis.Client
}

// Write atomically establishes the immutable run snapshot. Repeating the
// same snapshot is idempotent; any identity or authorization-field change is
// rejected so a cleanup gate cannot be rewritten in place.
func (s *MetadataHash) Write(ctx context.Context, m Metadata) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if s.Prefix == "" || s.RDB == nil {
		return fmt.Errorf("migration: metadata store: missing prefix/redis client")
	}
	deadline := ""
	if !m.RollbackDeadline.IsZero() {
		deadline = m.RollbackDeadline.UTC().Format(time.RFC3339Nano)
	}
	result, err := metadataIdentityWriteScript.Run(ctx, s.RDB, []string{MetadataKey(s.Prefix)},
		m.Owner, m.LedgerID, string(m.Mode), fmt.Sprintf("%d", m.CutoverEpoch),
		m.StartedAt.UTC().Format(time.RFC3339Nano), m.UpdatedAt.UTC().Format(time.RFC3339Nano),
		m.PreflightChecksum, string(m.Checkpoint), deadline).Text()
	if err != nil {
		return fmt.Errorf("migration: write metadata identity: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("migration: metadata identity already owned by another run")
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
			if cur == false or tonumber(cur) == nil or tonumber(cur) ~= tonumber(ARGV[1]) then
				return 0
			end
			redis.call('HSET', KEYS[1],
				'checkpoint', ARGV[2],
				'cutover_epoch', tostring(tonumber(ARGV[1]) + 1),
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
