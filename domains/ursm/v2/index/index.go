package index

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type Query struct {
	Tenant    string
	Canonical string
	Profile   string
	Modality  string
}

type Candidate struct {
	CredentialID int
	RawModel     string
	Score        float64
}

type Index struct {
	rdb    *redis.Client
	prefix string
}

func New(rdb *redis.Client, prefix string) *Index {
	return &Index{rdb: rdb, prefix: prefix}
}

func (i *Index) key(q Query) string {
	return fmt.Sprintf("%sidx:model:%s:%s:%s:%s", i.prefix, q.Tenant, q.Canonical, q.Profile, q.Modality)
}

func (i *Index) member(c Candidate) string {
	return fmt.Sprintf("%d:%s", c.CredentialID, c.RawModel)
}

func (i *Index) Upsert(ctx context.Context, q Query, c Candidate) error {
	return i.rdb.ZAdd(ctx, i.key(q), redis.Z{Score: c.Score, Member: i.member(c)}).Err()
}

func (i *Index) Remove(ctx context.Context, q Query, c Candidate) error {
	return i.rdb.ZRem(ctx, i.key(q), i.member(c)).Err()
}

func (i *Index) Query(ctx context.Context, q Query, limit int) ([]Candidate, error) {
	if limit <= 0 {
		limit = 100
	}
	zs, err := i.rdb.ZRangeWithScores(ctx, i.key(q), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(zs))
	for _, z := range zs {
		m, ok := z.Member.(string)
		if !ok {
			continue
		}
		c := Candidate{Score: z.Score}
		var cid int
		if _, err := fmt.Sscanf(m, "%d:", &cid); err == nil {
			c.CredentialID = cid
			c.RawModel = m[len(fmt.Sprintf("%d:", cid)):]
		}
		out = append(out, c)
	}
	return out, nil
}
