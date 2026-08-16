package sync

import (
	"context"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

type Seed struct {
	ProviderID   int
	CredentialID int
	RawModel     string
	TenantID     string
	Available    bool
}

type Syncer struct {
	rdb    *redis.Client
	prefix string
}

func NewSyncer(rdb *redis.Client, prefix string) *Syncer {
	return &Syncer{rdb: rdb, prefix: prefix}
}

func (s *Syncer) nodeKey(tenant string, cid int, raw string) string {
	return store.NodeKeyForTenant(s.prefix, tenant, cid, raw)
}

func (s *Syncer) UpsertNodeSeed(ctx context.Context, seed Seed) error {
	avail := "0"
	if seed.Available {
		avail = "1"
	}
	return s.rdb.HSet(ctx, s.nodeKey(seed.TenantID, seed.CredentialID, seed.RawModel),
		"available", avail,
		"source_priority", "0",
		"generation", "1",
		"seed_source", "warmup",
	).Err()
}
