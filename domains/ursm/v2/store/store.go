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

type Store struct{ rdb *redis.Client }

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

// WithRedis returns a new *Store backed by the supplied redis client. It
// does not mutate the receiver; callers should retain the returned value.
// Used by Manager.SetRedisForTest so integration tests can swap the redis
// client (e.g. after simulating a restart) without rebuilding the manager.
func (s *Store) WithRedis(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
}
