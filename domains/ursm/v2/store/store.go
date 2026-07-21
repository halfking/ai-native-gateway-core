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
