// Top-level env helpers used by main().
//
// Extracted from main.go as part of the P0 main.go split refactor.
// See docs/refactor-plans/main-go-split.md for the full plan.
//
// All functions here are package-private helpers; behaviour is unchanged
// from the original implementation in main.go.
package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

func gatewayEventSecret() string {
	if value := strings.TrimSpace(os.Getenv("AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("OUTBOX_HMAC_SECRET")); value != "" {
		slog.Warn("OUTBOX_HMAC_SECRET is deprecated; use AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET")
		return value
	}
	return ""
}

// positiveDurationEnv parses a positive Go duration from key. Missing, zero,
// negative, and malformed values fall back to the supplied default.
func positiveDurationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		slog.Warn("invalid positive duration env, using default",
			"key", key, "value", value, "default", fallback.String())
		return fallback
	}
	return duration
}

// positiveIntEnv parses a positive integer from key. Missing, zero,
// negative, and malformed values fall back to the supplied default.
// Used for capacities / limits (e.g. TELEMETRY_FALLBACK_BUFFER_CAP).
func positiveIntEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		slog.Warn("invalid positive int env, using default",
			"key", key, "value", value, "default", fallback)
		return fallback
	}
	return parsed
}

// envBoolOff returns true when the env var is set to one of:
// "0", "false", "off", "no" (case-insensitive). Returns false
// (i.e. feature enabled) when unset or set to a truthy value.
// Used for kill-switch style flags where the operator types the
// explicit disable value to take the feature offline.
func envBoolOff(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

func liveStreamCachedDurationsFromEnv() (time.Duration, time.Duration) {
	ttl := positiveDurationEnv("LLM_GATEWAY_LIVE_STREAM_CACHED_TTL", admin.LiveStreamLaneRetention)
	cleanup := positiveDurationEnv("LLM_GATEWAY_LIVE_STREAM_CACHED_CLEANUP_INTERVAL", ttl)
	return ttl, cleanup
}

// bootRetryBudgetEnv parses a non-negative duration used as a bounded
// startup retry budget (2026-09-04 availability work). Unlike
// positiveDurationEnv, an explicit "0" is honoured as "single attempt, no
// retry"; missing and malformed values fall back to the supplied default.
func bootRetryBudgetEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		slog.Warn("invalid boot retry budget env, using default",
			"key", key, "value", value, "default", fallback.String())
		return fallback
	}
	return duration
}

// openDBWithBootRetry opens the configured database with a bounded startup
// retry budget (2026-09-04 availability work). An empty URL returns nil —
// the intentional DB-less mode — exactly like db.Open. When Open fails
// (ping or migrations) and the budget has not elapsed, it waits 2s and
// retries, so a PostgreSQL that recovers shortly after the gateway boots no
// longer permanently bricks the process into no-DB mode. The budget is
// checked BETWEEN attempts: failing pings cost ≤3s each, keeping the
// worst-case boot inside the documented systemd TimeoutStartSec=90s window.
// LLM_GATEWAY_DB_BOOT_RETRY_SECONDS (default 20s, 0 = single attempt).
func openDBWithBootRetry(ctx context.Context, databaseURL string) *db.DB {
	if databaseURL == "" {
		return nil
	}
	budget := bootRetryBudgetEnv("LLM_GATEWAY_DB_BOOT_RETRY_SECONDS", 20*time.Second)
	deadline := time.Now().Add(budget)
	var lastErr error
	for attempt := 1; ; attempt++ {
		conn, err := db.Open(ctx, databaseURL)
		if err == nil {
			if attempt > 1 {
				slog.Info("postgres connected after boot retry", "attempts", attempt)
			}
			return conn
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		slog.Warn("postgres unreachable at boot, retrying",
			"attempt", attempt, "error", err, "budget", budget.String())
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			slog.Warn("postgres disabled (shutdown during boot retry)", "error", lastErr)
			return nil
		}
	}
	slog.Warn("postgres disabled", "error", lastErr, "retry_budget", budget.String())
	return nil // Prevent using a closed connection pool
}

// keyStoreSyncIntervalFromEnv parses LLM_GATEWAY_KEYSTORE_SYNC_INTERVAL
// (2026-09-04 api-keys in-memory replica). Missing/malformed/negative values
// fall back to the 5-minute default; an explicit 0 disables the feature and
// restores pure lazy per-key verification.
func keyStoreSyncIntervalFromEnv() time.Duration {
	value := strings.TrimSpace(os.Getenv("LLM_GATEWAY_KEYSTORE_SYNC_INTERVAL"))
	if value == "" {
		return 5 * time.Minute
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		slog.Warn("invalid key store sync interval env, using default",
			"key", "LLM_GATEWAY_KEYSTORE_SYNC_INTERVAL", "value", value, "default", "5m")
		return 5 * time.Minute
	}
	return duration
}

// keyStoreSnapshotDirFromEnv parses LLM_GATEWAY_KEYSTORE_SNAPSHOT_DIR
// (2026-09-04 cold-start gear). Default "./data/keystore"; the explicit
// values "off"/"disable"/"none" (case-insensitive) disable snapshot
// persistence entirely.
func keyStoreSnapshotDirFromEnv() string {
	value := strings.TrimSpace(os.Getenv("LLM_GATEWAY_KEYSTORE_SNAPSHOT_DIR"))
	switch strings.ToLower(value) {
	case "":
		return "./data/keystore"
	case "off", "disable", "none":
		return ""
	default:
		return value
	}
}

// pingRedisWithBootRetry pings the session Redis client until it answers or
// the budget elapses (checked between attempts; each ping is bounded by a
// 5s timeout). Returns the last error. Keeps container start-order races
// and short Redis failovers from disabling sessions/URSM for the whole
// process lifetime. LLM_GATEWAY_REDIS_BOOT_RETRY_SECONDS (default 30s,
// 0 = single attempt).
func pingRedisWithBootRetry(client *session.RedisClient, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for attempt := 1; ; attempt++ {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = client.Ping(pingCtx)
		pingCancel()
		if lastErr == nil {
			if attempt > 1 {
				slog.Info("redis reachable after boot retry", "attempts", attempt)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		slog.Warn("redis ping failed at boot, retrying",
			"attempt", attempt, "error", lastErr, "budget", budget.String())
		time.Sleep(2 * time.Second)
	}
}

// sessionAuditApprovalTimeoutFromEnv 读取 SESSION_AUDIT_APPROVAL_TIMEOUT
// 环境变量并解析为 time.Duration。支持 "30s" / "15m" / "1h" 格式。
// 无效输入或缺失时退化为 15m。2026-06-27 audit fix。
func sessionAuditApprovalTimeoutFromEnv() time.Duration {
	return positiveDurationEnv("SESSION_AUDIT_APPROVAL_TIMEOUT", 15*time.Minute)
}

// parseModelFallbackEnv parses LLM_GATEWAY_MODEL_FALLBACK env var.
//
// Format: "primary=fb1,fb2;primary2=fb3"
// Example: "claude-sonnet-4-20250514=gpt-4o-2024-11-20;deepseek-chat=gpt-4o-mini"
func parseModelFallbackEnv(raw string) map[string][]string {
	m := make(map[string][]string)
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		primary := strings.TrimSpace(parts[0])
		if primary == "" {
			continue
		}
		for _, fb := range strings.Split(parts[1], ",") {
			fb = strings.TrimSpace(fb)
			if fb != "" {
				m[primary] = append(m[primary], fb)
			}
		}
	}
	return m
}

// useNewProbeMode controls whether the legacy probe workers
// (bg/self_check_worker.go featured mode, bg/credential_probe_v2.go,
// bg/model_probe.go, bg/passive_probe_listener.go,
// bg/active_probe_worker.go) start.  When true (the default since
// 2026-07-14) the new bg/credential_selfcheck.go + bg/node_probe.go
// + bg/system_health.go own the probe/self-check surface; the legacy
// workers are skipped to fix the "1 minute ≥ 2 probes" frequency
// issue observed on 252.  Set LLM_GATEWAY_USE_NEW_PROBE_MODE=false
// to roll back to the legacy behavior.
func useNewProbeMode() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_USE_NEW_PROBE_MODE")))
	if v == "" {
		return true // default to the new mode per the 2026-07-14 spec rewrite
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// shouldStartNewProbeWorkers controls the new probe/self-check worker group.
// Each member relies on the system API key directly or shares the group's
// lifecycle, so partial startup is deliberately avoided.
// canStartGatewayDependentNewProbes gates the new probe/self-check worker
// group on a usable system API key. The check intentionally re-reads
// EnsureSystemAPIKeyFromEnv so deployments that do not have a system
// key cached in DB (or whose key generation fails) skip all four
// gateway-dependent workers in one go instead of each running with an
// empty Authorization header.
func canStartGatewayDependentNewProbes(apiKey string) bool {
	if strings.TrimSpace(apiKey) == "" {
		return false
	}
	return true
}

func shouldStartNewProbeWorkers(apiKey string) bool {
	return useNewProbeMode() && canStartGatewayDependentNewProbes(apiKey)
}

// localGatewayProbeAPIKey returns the key accepted by the local gateway's
// static AuthMiddleware. Database system keys authenticate other surfaces but
// must not replace this value for loopback probe requests.
func localGatewayProbeAPIKey(apiKey string) string {
	return strings.TrimSpace(apiKey)
}
