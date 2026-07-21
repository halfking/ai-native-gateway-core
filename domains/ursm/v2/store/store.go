package store

import (
	_ "embed"

	"github.com/redis/go-redis/v9"
)

//go:embed apply_decision.lua
var applyDecisionSrc string

var ApplyDecisionScript = redis.NewScript(applyDecisionSrc)

type Store struct{ rdb *redis.Client }

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb} }
