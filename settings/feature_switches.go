package settings

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Feature switches (kill-switch) for hot modules.
//
// Set via environment variable at boot time. Format:
//   KILL_SESSION_COMPRESSION=1   → 关掉 session compression hook
//   KILL_SESSION_CACHE=1         → 关掉 session_cache (L1+L2+L3)
//   KILL_CIRCUIT_DEGRADATION=1   → 关掉 circuit breaker + credential degraded
//   KILL_RATE_LIMITER=1          → 关掉 RPM/TPM token bucket middleware
//   KILL_FP_SLOT=1               → 关掉 fingerprint slot prefilter/degraded 路径
//
// Default: all switches ENABLED (production-stable behaviour).
// Local test / chaos: set one or more to 1 to disable the module and
// observe the residual 503 stream. Production incidents: flip via
// systemd `Environment=KILL_SESSION_CACHE=1` and `systemctl restart`.
//
// Implemented as a sync.Once-cached read at first call so we read env
// exactly once; later override is by SIGHUP-reload in settings package
// (added in 2026-07-12 incident response).

var (
	featureOnce  sync.Once
	featureCache = map[string]bool{}
)

// IsEnabled reports whether the named feature is currently enabled.
//
// Unknown features are treated as enabled (fail-open).
func IsEnabled(name string) bool {
	featureOnce.Do(loadFeatureFlags)
	v, ok := featureCache[name]
	if !ok {
		return true
	}
	return v
}

// Snapshot returns the current on/off state of all known modules.
// Callers log this once at boot.
func Snapshot() map[string]bool {
	featureOnce.Do(loadFeatureFlags)
	out := make(map[string]bool, len(featureCache))
	for k, v := range featureCache {
		out[k] = v
	}
	return out
}

func loadFeatureFlags() {
	for _, name := range []string{
		"session_compression",
		"session_cache",
		"circuit_degradation",
		"rate_limiter",
		"fp_slot",
	} {
		featureCache[name] = readKillSwitch(name)
	}
	slog.Info("feature_switches_snapshot",
		"session_compression", featureCache["session_compression"],
		"session_cache", featureCache["session_cache"],
		"circuit_degradation", featureCache["circuit_degradation"],
		"rate_limiter", featureCache["rate_limiter"],
		"fp_slot", featureCache["fp_slot"],
	)
}

func readKillSwitch(name string) bool {
	env := "KILL_" + strings.ToUpper(snakeToCamel(name))
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return true
	}
	// Accept "1", "true", "yes", "on" (any non-empty truthy values disable).
	b, err := strconv.ParseBool(v)
	if err != nil {
		return v != "" // any non-empty value disables
	}
	return !b
}

func snakeToCamel(s string) string {
	return strings.ReplaceAll(s, "_", "_")
}
