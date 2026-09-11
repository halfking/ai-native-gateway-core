package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
)

// ProbeHealthEvidence reads node hashes directly from Redis in batched
// pipelines — all primary keys go through one SafeHGetAllPipeline call (a
// TYPE exec followed by an HGETALL exec) and dual-mode legacy fallbacks go
// through at most one more such call, so a gate batch costs at most four
// pipeline execs instead of two serial HGetAll round-trips per model. Key
// selection mirrors PipelineNodeViews (doc 14 §5.2.1/§5.3): legacy reads the
// legacy key only; dual reads the canonical key and falls back to the
// exact-tuple legacy key on a miss (or straight to legacy when the canonical
// grammar cannot represent the tuple); canonical never falls back. It
// deliberately does not reuse PipelineNodeViews itself: probe preflight must
// not inherit the routing reader's mirror, expired-cool half-open, or
// availability defaults.
func (s *Store) ProbeHealthEvidence(ctx context.Context, prefix, tenant string, credentialID int, models []string) ([]api.ProbeHealthEvidence, error) {
	out := make([]api.ProbeHealthEvidence, len(models))
	if s == nil || s.rdb == nil {
		return nil, ErrRedisUnavailable
	}
	if len(models) == 0 {
		return out, nil
	}

	type slot struct {
		model    string
		primary  string // key read in the first pipeline; "" ⇒ no read at all
		fallback string // legacy key used in dual mode when the primary misses
		raw      map[string]string
	}
	mode := s.schemaMode
	slots := make([]slot, len(models))
	primaryKeys := make([]string, 0, len(models))
	primaryIndexes := make([]int, 0, len(models))
	for i, model := range models {
		sl := slot{model: model}
		if mode == KeySchemaModeLegacy {
			sl.primary = NodeKeyForTenant(prefix, tenant, credentialID, model)
		} else if k2, err := K2KeySetForTenant(prefix, tenant, credentialID, model); err == nil {
			sl.primary = k2.Node
			if mode == KeySchemaModeDual {
				sl.fallback = NodeKeyForTenant(prefix, tenant, credentialID, model)
			}
		} else if mode == KeySchemaModeDual {
			sl.primary = NodeKeyForTenant(prefix, tenant, credentialID, model)
		}
		if sl.primary != "" {
			primaryKeys = append(primaryKeys, sl.primary)
			primaryIndexes = append(primaryIndexes, i)
		}
		slots[i] = sl
	}
	primaryResults, err := redissafe.SafeHGetAllPipeline(ctx, s.rdb.Pipeline(), primaryKeys)
	if err != nil {
		return nil, fmt.Errorf("ursm.v2: probe evidence read: %w", err)
	}
	now := time.Now()
	pending := make([]int, 0, len(models))
	for j, i := range primaryIndexes {
		sl := &slots[i]
		res := primaryResults[j]
		if res.Err != nil && !errors.Is(res.Err, redissafe.ErrKeyNotFound) {
			return nil, fmt.Errorf("ursm.v2: probe evidence node %q: %w", sl.primary, res.Err)
		}
		sl.raw = res.Fields
		if len(sl.raw) == 0 && sl.fallback != "" {
			pending = append(pending, i)
		}
	}
	for i, sl := range slots {
		if sl.fallback == "" || len(sl.raw) > 0 {
			out[i] = probeEvidenceFromHash(sl.model, sl.raw, now)
		}
	}
	if len(pending) > 0 {
		fallbackKeys := make([]string, len(pending))
		for j, i := range pending {
			fallbackKeys[j] = slots[i].fallback
		}
		fallbackResults, err := redissafe.SafeHGetAllPipeline(ctx, s.rdb.Pipeline(), fallbackKeys)
		if err != nil {
			return nil, fmt.Errorf("ursm.v2: probe evidence fallback read: %w", err)
		}
		for j, i := range pending {
			res := fallbackResults[j]
			if res.Err != nil && !errors.Is(res.Err, redissafe.ErrKeyNotFound) {
				return nil, fmt.Errorf("ursm.v2: probe evidence fallback node %q: %w", slots[i].fallback, res.Err)
			}
			out[i] = probeEvidenceFromHash(slots[i].model, res.Fields, now)
		}
	}
	return out, nil
}

func probeEvidenceFromHash(model string, raw map[string]string, now time.Time) api.ProbeHealthEvidence {
	e := api.ProbeHealthEvidence{RawModel: model}
	if len(raw) == 0 {
		return e
	}
	available, ok := boolField(raw, "available")
	if !ok {
		return e
	}
	disabled, ok := boolFieldDefault(raw, "disabled", false)
	if !ok {
		return e
	}
	hold, ok := boolFieldDefault(raw, "manual_hold", false)
	if !ok {
		return e
	}
	failStreak, ok := nonNegativeInt(raw, "fail_streak", 0)
	if !ok {
		return e
	}
	coolUntil, ok := nonNegativeInt(raw, "cool_until_ms", 0)
	if !ok {
		return e
	}
	health := raw["health"]
	if health != "" && !api.IsValidHealthStatus(health) {
		return e
	}

	requestAt, requestAtPresent, ok := timestampField(raw, "last_request_at_ms")
	if !ok {
		return e
	}
	if requestAtPresent {
		failed, ok := boolField(raw, "last_request_failed")
		if !ok {
			return e
		}
		e.LastRequestAt = time.UnixMilli(requestAt)
		e.LastRequestFailed = failed
	} else if legacyRequestSamples(raw) {
		// Old hashes recorded events but cannot distinguish the latest request
		// outcome. Do not manufacture "no request" evidence from that state.
		return e
	}
	errorAt, errorAtPresent, ok := timestampField(raw, "last_request_error_at_ms")
	if !ok {
		return e
	}
	if errorAtPresent {
		e.LastRequestErrorAt = time.UnixMilli(errorAt)
	}

	e.Known = true
	e.Healthy = available && !disabled && !hold && coolUntil <= now.UnixMilli() && failStreak == 0 && raw["last_err"] == "" && (health == "" || health == api.HealthStatusHealthy)
	return e
}

func boolField(raw map[string]string, name string) (bool, bool) {
	s, present := raw[name]
	if !present {
		return false, false
	}
	switch s {
	case "0":
		return false, true
	case "1":
		return true, true
	default:
		return false, false
	}
}

func boolFieldDefault(raw map[string]string, name string, defaultValue bool) (bool, bool) {
	if _, present := raw[name]; !present {
		return defaultValue, true
	}
	return boolField(raw, name)
}

func nonNegativeInt(raw map[string]string, name string, defaultValue int64) (int64, bool) {
	s, present := raw[name]
	if !present || s == "" {
		return defaultValue, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 0
}

func timestampField(raw map[string]string, name string) (value int64, present bool, ok bool) {
	s, present := raw[name]
	if !present {
		return 0, false, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, true, false
	}
	return n, true, true
}

func legacyRequestSamples(raw map[string]string) bool {
	for _, field := range []string{"event_seq", "samples_1m", "samples_5m", "samples_30m"} {
		if n, ok := nonNegativeInt(raw, field, 0); !ok || n > 0 {
			return true
		}
	}
	return false
}
