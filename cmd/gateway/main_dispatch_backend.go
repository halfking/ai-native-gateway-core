package main

// 分布式容量治理 Stage B + Stage C.2: composition root for the dispatch
// GovernorBackend + optional GovernorSnapshot observer.
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
// The forwarder consumes RedisEnforce for distributed concurrency slots.
// RPM and TPM retain their local token buckets until equivalent Redis token
// accounting is implemented. RedisShadow remains a non-gating observation mode.
//
// Stage C.2: LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER ∈ {"off", "observe"}
// (default "off") controls whether the Pipeline's 100ms-tick snapshot
// observer is wired. observe requires a non-nil Pipeline; the env flag
// itself only affects composition, not the runtime cost.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
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

// envGovernorObserver returns the trimmed, lowercased env value (empty
// string when unset) for the Stage C.2 snapshot observer. Empty string
// is treated as the default "off" mode.
func envGovernorObserver() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER")))
}

// envGovernorObserverTickMS reads the optional tick override. Returns
// (value, ok). Default in the composition root is 100ms.
func envGovernorObserverTickMS() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS"))
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		slog.Warn("dispatch: invalid LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS; ignoring",
			"value", raw, "error", err)
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
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

	// Stage C.2: optional snapshot observer. Wire after the backend so
	// the observer's first tick already sees the live backend kind.
	if tick, ok := resolveGovernorObserver(); ok {
		o := dispatch.NewGovernorSnapshotObserver(tick)
		p.SetGovernorSnapshotObserver(o)
		slog.Info("dispatch: governor snapshot observer wired",
			"tick_ms", tick.Milliseconds())
	}
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

// resolveGovernorObserver returns (tick, enabled) when the observer
// flag is observed; (0, false) otherwise. Tick is the configured
// sampling interval (defaults to 100ms when unset). Pure function — env
// reads happen via envGovernorObserver + envGovernorObserverTickMS,
// both package-level helpers that can be redirected in tests.
func resolveGovernorObserver() (time.Duration, bool) {
	mode := envGovernorObserver()
	switch mode {
	case "", "off":
		return 0, false
	case "observe":
		tick, _ := envGovernorObserverTickMS()
		if tick <= 0 {
			tick = 100 * time.Millisecond
		}
		return tick, true
	default:
		slog.Error("dispatch: unknown LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER value; observer disabled",
			"value", mode,
			"supported", []string{"off", "observe"})
		return 0, false
	}
}

// envCapacityAwareSort returns the trimmed, lowercased value of
// LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT (empty string when unset).
// Stage D: capacity-aware soft-rank in dispatchRoute.
func envCapacityAwareSort() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT")))
}

// resolveCapacityAwareSort returns (enabled, error). Enabled=true when
// the env flag is explicitly "on". Default off; bogus value returns an
// error and falls back to off (with the caller slog-warn).
func resolveCapacityAwareSort() (bool, error) {
	mode := envCapacityAwareSort()
	switch mode {
	case "", "off":
		return false, nil
	case "on":
		return true, nil
	default:
		return false, fmt.Errorf("dispatch: unknown LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT value %q", mode)
	}
}

// wireDispatchCapacityAwareSort (Stage D) is the composition-root entry
// for the capacity-aware soft-rank hook. Reads
// LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT and, when "on", wires
// Executor.SetCapacityAwareSort with a snapFn closure that defers to
// Pipeline.SnapshotForCred (the per-cred cache populated by the
// Stage C.2 observer tick). Default off → strict no-op, matches
// B.4 / C.2 env-flag style.
//
// The Executor is the wiring target (not Deps) because the field
// pair capacityAwareSortOn + capacityAwareSnapFn is owned by Executor
// and toggled at composition-root time, while Deps is constructed
// per-request in NewPipeline.
func wireDispatchCapacityAwareSort(e *executors.Executor, p *dispatch.Pipeline) {
	if e == nil || p == nil {
		return
	}
	enabled, err := resolveCapacityAwareSort()
	if err != nil {
		slog.Warn("dispatch: capacity-aware sort disabled", "error", err)
		return
	}
	if !enabled {
		return
	}
	e.SetCapacityAwareSort(true, dispatch.SnapshotFnFromProvider(p))
	slog.Info("dispatch: capacity-aware soft sort wired")
}
