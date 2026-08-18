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
				if coolUntil.After(now) {
					// If in cooling period, mark as unavailable
					v.Available = false
					if v.Reason == "" {
						v.Reason = "in_cool_until"
					}
				} else if !v.Available && raw["manual_hold"] != "1" {
					// 2026-08-18 fix (154 incident, minimax-m3 / cred 36):
					// an EXPIRED cool window must half-open the node. The
					// write side (record_request.lua:232) already treats
					// "disabled=1 with expired cool_until" as recovered — but
					// only when a NEW event arrives at that node. The read
					// side kept returning the stale available="0" bit, the
					// router kept filtering the node out, so no event ever
					// arrived and the Lua recovery branch could never fire.
					// With the fast-probe queue also dead (fixed separately)
					// the node stayed unroutable until a process restart.
					// Reading an expired cool as available re-arms the
					// intended half-open circuit: if the next request fails,
					// record_request re-disables with exponential backoff.
					//
					// Audit guard (2026-08-18): manual_hold wins. apply_admin
					// .lua writes manual_hold=1 + available=0 WITHOUT touching
					// a pre-existing cool_until_ms, so an admin force_disable
					// issued during an auto-cool window must not be half-opened
					// back into rotation when that window expires.
					v.Available = true
				}
			}
		}

		// Check if disabled flag is set (fallback check). A disabled bit with
		// NO cool window at all (e.g. admin hold) still blocks; the expired
		// -cool half-open above takes precedence over the sticky bit.
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
