package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type NodeQuery struct {
	CredentialID int
	RawModel     string
}

func (s *Store) PipelineNodeViews(ctx context.Context, prefix string, qs []NodeQuery) ([]api.NodeView, error) {
	if s == nil || s.rdb == nil {
		return nil, ErrRedisUnavailable
	}
	if len(qs) == 0 {
		return nil, nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(qs))
	for i, q := range qs {
		cmds[i] = pipe.HGetAll(ctx, NodeKey(prefix, q.CredentialID, q.RawModel))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("ursm.v2: pipeline exec: %w", err)
	}
	out := make([]api.NodeView, len(qs))
	for i, c := range cmds {
		raw, err := c.Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ursm.v2: hgetall: %w", err)
		}
		v := api.NodeView{CredentialID: qs[i].CredentialID, RawModel: qs[i].RawModel}
		v.Available = raw["available"] == "1"
		v.Reason = raw["last_err"]
		v.SrcPriority = atoi(raw["source_priority"])
		v.Generation = atoi64(raw["generation"])
		v.FailStreak = int(atoi64(raw["fail_streak"]))
		out[i] = v
	}
	return out, nil
}

func atoi(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func atoi64(s string) int64 { return int64(atoi(s)) }
