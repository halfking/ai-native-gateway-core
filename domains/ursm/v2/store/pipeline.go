package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type NodeQuery struct {
	TenantID     string
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
		cmds[i] = pipe.HGetAll(ctx, NodeKeyForTenant(prefix, q.TenantID, q.CredentialID, q.RawModel))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("ursm.v2: pipeline exec: %w", err)
	}
	out := make([]api.NodeView, len(qs))
	now := time.Now()
	for i, c := range cmds {
		raw, err := c.Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ursm.v2: hgetall: %w", err)
		}
		v := api.NodeView{TenantID: qs[i].TenantID, CredentialID: qs[i].CredentialID, RawModel: qs[i].RawModel}
		v.Available = raw["available"] == "1"
		v.Reason = raw["last_err"]
		v.HealthStatus = raw["health"]
		v.SrcPriority = atoi(raw["source_priority"])
		v.Generation = atoi64(raw["generation"])
		v.FailStreak = int(atoi64(raw["fail_streak"]))
		v.SR1m = parseFloat(raw["sr_1m"])
		v.SR5m = parseFloat(raw["sr_5m"])
		v.SR30m = parseFloat(raw["sr_30m"])
		v.Samples1m = atoi(raw["samples_1m"])
		v.Samples5m = atoi(raw["samples_5m"])
		v.Samples30m = atoi(raw["samples_30m"])
		v.LatP50Ms = atoi(raw["lat_p50_ms"])
		v.LatP95Ms = atoi(raw["lat_p95_ms"])
		v.LatEWMA = atoi(raw["lat_ewma_ms"])

		// Parse cool_until_ms and check if node is in cooling period
		if coolUntilMsStr := raw["cool_until_ms"]; coolUntilMsStr != "" {
			coolUntilMs := atoi64(coolUntilMsStr)
			if coolUntilMs > 0 {
				coolUntil := time.UnixMilli(coolUntilMs)
				v.CoolUntil = coolUntil
				// If in cooling period, mark as unavailable
				if coolUntil.After(now) {
					v.Available = false
					if v.Reason == "" {
						v.Reason = "in_cool_until"
					}
				}
			}
		}

		// Check if disabled flag is set (fallback check)
		if raw["disabled"] == "1" && v.Available {
			// Only mark unavailable if not already marked by cool_until
			if v.CoolUntil == (time.Time{}) || v.CoolUntil.After(now) {
				v.Available = false
				v.Reason = "node_disabled"
			}
		}

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
