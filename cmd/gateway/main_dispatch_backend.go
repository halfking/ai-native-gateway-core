package main

// 分布式容量治理 Stage B: composition root for the dispatch GovernorBackend.
//
// wireDispatchGovernorBackend is invoked once from main.go right after
// SetQueueMirror. It reads LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND and
// selects the production backend:
//
//   - "" / "local"          → LocalBackend (the pre-stage-A default).
//   - "redis_shadow"        → RedisShadowBackend against redisClientForCache
//                             (when non-nil; otherwise falls back to Local
//                             with a warning). Never gates admission.
//   - "redis_enforce"       → RedisEnforceBackend (strict cluster-wide).
//                             Redis outage → wrapped ErrGovernorUnavailable,
//                             no memory fallback (the inverse of rpm_redis.go).
//   - any other value       → log error, fall back to LocalBackend.
//
// The per-spec governor factory (LocalBackend.New / RedisEnforce.New /
// RedisShadow.New) is wired here but NOT consumed yet — credForwarder.gov
// construction in domains/dispatch/forwarder.go stays on newGovernor(cred).
// Stage D/E owns the read path.

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// instanceIDForRedisBackend returns the free-form diagnostic identifier
// the Stage B backends surface via Name() (never a metric label key).
// We use HOSTNAME when set; otherwise fall back to "gateway".
func instanceIDForRedisBackend() string {
	if h := strings.TrimSpace(os.Getenv("HOSTNAME")); h != "" {
		return h
	}
	return "gateway"
}

// envGovernorBackend returns the trimmed, lowercased env value (empty
// string when unset) so the switch below is case-insensitive.
func envGovernorBackend() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND")))
}

// wireDispatchGovernorBackend is the composition-root entry point. It
// is safe to call with nil pipeline (no-op) and nil redis client
// (only the "local" mode is then available).
func wireDispatchGovernorBackend(p *dispatch.Pipeline, redisClient *redis.Client, instanceID string) {
	if p == nil {
		return
	}
	backend := resolveGovernorBackend(envGovernorBackend(), redisClient, instanceID)

	if err := backend.Open(context.Background()); err != nil {
		slog.Warn("dispatch: governor backend Open failed; backend not wired",
			"backend", backend.Kind(), "error", err)
		return
	}
	p.SetGovernorBackend(backend)
	slog.Info("dispatch: governor backend wired",
		"kind", backend.Kind(), "name", backend.Name())
}

// resolveGovernorBackend is the pure env-decision helper extracted from
// wireDispatchGovernorBackend so the env-flag table can be unit-tested
// without spinning up a Pipeline or a real Redis client (a nil client
// pins the test to the local-fallback paths).
func resolveGovernorBackend(mode string, redisClient *redis.Client, instanceID string) dispatch.GovernorBackend {
	switch mode {
	case "", "local":
		return dispatch.NewLocalBackend(instanceID)
	case "redis_shadow":
		if redisClient == nil {
			slog.Warn("dispatch: LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND=redis_shadow but Redis is disabled; falling back to local")
			return dispatch.NewLocalBackend(instanceID)
		}
		return dispatch.NewRedisShadowBackend(redisClient, instanceID)
	case "redis_enforce":
		if redisClient == nil {
			slog.Warn("dispatch: LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND=redis_enforce but Redis is disabled; falling back to local")
			return dispatch.NewLocalBackend(instanceID)
		}
		return dispatch.NewRedisEnforceBackend(redisClient, instanceID)
	default:
		slog.Error("dispatch: unknown LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND value; falling back to local",
			"value", mode,
			"supported", []string{"local", "redis_shadow", "redis_enforce"})
		return dispatch.NewLocalBackend(instanceID)
	}
}
