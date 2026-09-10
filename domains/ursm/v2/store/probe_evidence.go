package store

import (
	"context"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// ProbeHealthEvidence reads one node hash directly from Redis. It deliberately
// does not use PipelineNodeViews: probe preflight must not inherit the routing
// reader's mirror, expired-cool half-open, or availability defaults.
func (s *Store) ProbeHealthEvidence(ctx context.Context, prefix, tenant string, credentialID int, models []string) ([]api.ProbeHealthEvidence, error) {
	out := make([]api.ProbeHealthEvidence, len(models))
	if s == nil || s.rdb == nil {
		return nil, ErrRedisUnavailable
	}
	for i, model := range models {
		out[i].RawModel = model
		raw, err := s.probeEvidenceHash(ctx, prefix, tenant, credentialID, model)
		if err != nil {
			return nil, err
		}
		out[i] = probeEvidenceFromHash(model, raw, time.Now())
	}
	return out, nil
}

func (s *Store) probeEvidenceHash(ctx context.Context, prefix, tenant string, credentialID int, model string) (map[string]string, error) {
	legacy := NodeKeyForTenant(prefix, tenant, credentialID, model)
	if s.schemaMode == KeySchemaModeLegacy {
		return s.rdb.HGetAll(ctx, legacy).Result()
	}
	k2, err := K2KeySetForTenant(prefix, tenant, credentialID, model)
	if err != nil {
		if s.schemaMode == KeySchemaModeDual {
			return s.rdb.HGetAll(ctx, legacy).Result()
		}
		return nil, nil // canonical grammar cannot represent this tuple
	}
	raw, err := s.rdb.HGetAll(ctx, k2.Node).Result()
	if err != nil || len(raw) != 0 || s.schemaMode == KeySchemaModeCanonical {
		return raw, err
	}
	return s.rdb.HGetAll(ctx, legacy).Result()
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
