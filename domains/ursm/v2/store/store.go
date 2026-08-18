package store

import (
	_ "embed"

	"github.com/redis/go-redis/v9"
)

//go:embed apply_decision.lua
var applyDecisionSrc string

var ApplyDecisionScript = redis.NewScript(applyDecisionSrc)

//go:embed apply_admin.lua
var applyAdminSrc string

var ApplyAdminScript = redis.NewScript(applyAdminSrc)

//go:embed apply_probe.lua
var applyProbeSrc string

var ApplyProbeScript = redis.NewScript(applyProbeSrc)

//go:embed clear_state.lua
var clearStateSrc string

// ClearStateScript clears cooling state and error counters for emergency repair.
var ClearStateScript = redis.NewScript(clearStateSrc)

type Store struct {
	rdb        *redis.Client
	schemaMode KeySchemaMode
}

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

// SetKeySchemaMode fixes the key schema mode for this store. Boot-only by
// contract (doc 14 §3): it must be set before the store serves requests,
// and any mode transition closes the recovery ready gate first.
func (s *Store) SetKeySchemaMode(mode KeySchemaMode) { s.schemaMode = mode }

// KeySchemaMode reports the mode this store was booted with.
func (s *Store) KeySchemaMode() KeySchemaMode {
	if s == nil {
		return KeySchemaModeLegacy
	}
	return s.schemaMode
}

// WithRedis returns a new *Store backed by the supplied redis client. It
// does not mutate the receiver; callers should retain the returned value.
// Used by Manager.SetRedisForTest so integration tests can swap the redis
// client (e.g. after simulating a restart) without rebuilding the manager.
// The schema mode carries over.
func (s *Store) WithRedis(rdb *redis.Client) *Store {
	return &Store{rdb: rdb, schemaMode: s.schemaMode}
}
