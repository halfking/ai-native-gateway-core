package sync

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type Seed struct {
	ProviderID   int
	CredentialID int
	RawModel     string
	Available    bool
}

type Syncer struct {
	rdb    *redis.Client
	prefix string
}

func NewSyncer(rdb *redis.Client, prefix string) *Syncer {
	return &Syncer{rdb: rdb, prefix: prefix}
}

func (s *Syncer) nodeKey(cid int, raw string) string {
	return fmt.Sprintf("%snode:%d:%s", s.prefix, cid, raw)
}

func (s *Syncer) UpsertNodeSeed(ctx context.Context, seed Seed) error {
	avail := "0"
	if seed.Available {
		avail = "1"
	}
	return s.rdb.HSet(ctx, s.nodeKey(seed.CredentialID, seed.RawModel),
		"available", avail,
		"source_priority", "0",
		"generation", "1",
		"seed_source", "warmup",
	).Err()
}
