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

// PipelineNodeViews resolves node views honoring the store's key schema
// mode (doc 14 §5.2.1/§5.3):
//
//   - legacy: read the legacy key only (the historical behavior);
//   - dual: read the canonical key first; on a miss fall back to the legacy
//     key of the exact same tuple. Callers here always hold the true tuple,
//     so the fallback is exact-tuple by construction — the ledger-migratable
//     restriction governs scanned/discovered keys, not this path. Tuples the
//     canonical grammar cannot represent (empty tenant) read legacy directly;
//   - canonical: read the canonical key only and never fall back; a
//     legacy-only tuple surfaces as unavailable.
func (s *Store) PipelineNodeViews(ctx context.Context, prefix string, qs []NodeQuery) ([]api.NodeView, error) {
	if s == nil || s.rdb == nil {
		return nil, ErrRedisUnavailable
	}
	if len(qs) == 0 {
		return nil, nil
	}

	type slot struct {
		query    NodeQuery
		primary  string // key read in the first pipeline; "" ⇒ no read at all
		fallback string // legacy key used in dual mode when the primary misses
		cmd      *redis.MapStringStringCmd
	}
	mode := s.schemaMode
	slots := make([]slot, len(qs))
	pipe := s.rdb.Pipeline()
	for i, q := range qs {
		sl := slot{query: q}
		if mode == KeySchemaModeLegacy {
			sl.primary = NodeKeyForTenant(prefix, q.TenantID, q.CredentialID, q.RawModel)
		} else if k2, err := K2KeySetForTenant(prefix, q.TenantID, q.CredentialID, q.RawModel); err == nil {
			sl.primary = k2.Node
			if mode == KeySchemaModeDual {
				sl.fallback = NodeKeyForTenant(prefix, q.TenantID, q.CredentialID, q.RawModel)
			}
		} else if mode == KeySchemaModeDual {
			// The canonical grammar cannot represent this tuple (empty
			// tenant); legacy compatibility carries it (doc 14 §2).
			sl.primary = NodeKeyForTenant(prefix, q.TenantID, q.CredentialID, q.RawModel)
		}
		if sl.primary != "" {
			sl.cmd = pipe.HGetAll(ctx, sl.primary)
		}
		slots[i] = sl
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("ursm.v2: pipeline exec: %w", err)
	}

	now := time.Now()
	out := make([]api.NodeView, len(qs))
	pending := make([]int, 0, len(qs))
	for i, sl := range slots {
		var raw map[string]string
		if sl.cmd != nil {
			r, err := sl.cmd.Result()
			if err == nil {
				raw = r
			} else if err != redis.Nil {
				return nil, fmt.Errorf("ursm.v2: hgetall: %w", err)
			}
		}
		if len(raw) == 0 && sl.fallback != "" {
			pending = append(pending, i)
			continue
		}
		out[i] = nodeViewFromHash(slots[i].query, raw, now)
	}

	if len(pending) > 0 {
		pipe2 := s.rdb.Pipeline()
		for _, i := range pending {
			slots[i].cmd = pipe2.HGetAll(ctx, slots[i].fallback)
		}
		if _, err := pipe2.Exec(ctx); err != nil && err != redis.Nil {
			return nil, fmt.Errorf("ursm.v2: pipeline exec (fallback): %w", err)
		}
		for _, i := range pending {
			raw, err := slots[i].cmd.Result()
			if err != nil && err != redis.Nil {
				return nil, fmt.Errorf("ursm.v2: hgetall (fallback): %w", err)
			}
			out[i] = nodeViewFromHash(slots[i].query, raw, now)
		}
	}
	return out, nil
}

// nodeViewFromHash maps one node hash into a NodeView; an empty/missing hash
// yields the zero (unavailable) view for the query's tuple.
func nodeViewFromHash(q NodeQuery, raw map[string]string, now time.Time) api.NodeView {
	v := api.NodeView{TenantID: q.TenantID, CredentialID: q.CredentialID, RawModel: q.RawModel}
	if len(raw) == 0 {
		return v
	}
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
	v.EmptyResponses1m = atoi(raw["empty_responses_1m"])
	v.EmptyResponses5m = atoi(raw["empty_responses_5m"])
	v.EmptyResponses30m = atoi(raw["empty_responses_30m"])
	v.EmptyResponseRate1m = parseFloat(raw["empty_response_rate_1m"])
	v.EmptyResponseRate5m = parseFloat(raw["empty_response_rate_5m"])
	v.EmptyResponseRate30m = parseFloat(raw["empty_response_rate_30m"])
	v.LatP50Ms = atoi(raw["lat_p50_ms"])
	v.LatP95Ms = atoi(raw["lat_p95_ms"])
	v.LatEWMA = atoi(raw["lat_ewma_ms"])

	// Parse cool_until_ms and check if node is in cooling period.
	if coolUntilMsStr := raw["cool_until_ms"]; coolUntilMsStr != "" {
		coolUntilMs := atoi64(coolUntilMsStr)
		if coolUntilMs > 0 {
			coolUntil := time.UnixMilli(coolUntilMs)
			v.CoolUntil = coolUntil
			if coolUntil.After(now) {
				// Active cool window: still unavailable.
				v.Available = false
				if v.Reason == "" {
					v.Reason = "in_cool_until"
				}
			} else if !v.Available && raw["manual_hold"] != "1" {
				// 2026-08-18 fix (154 incident, minimax-m3 / cred 36):
				// an EXPIRED cool window must half-open the node. The write
				// side (record_request.lua:232) already treats
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
		if v.CoolUntil == (time.Time{}) || v.CoolUntil.After(now) {
			v.Available = false
			v.Reason = "node_disabled"
		}
	}

	return v
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
