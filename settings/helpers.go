package settings

import (
	"encoding/json"
	"log/slog"
)

// Helpers for reading platform-scoped settings with hot-reload support.
//
// Every Get* call goes through Global.EffectiveValue(), which reads from
// DB (settings_kv) → env → default. Because Global is backed by a live
// DB connection and the DB is updated atomically, every call sees the
// latest committed value — no caching layer to invalidate.
//
// Pattern (matches session_audit/config.go, outputcompliance/interceptor.go):
//
//	if settings.Global != nil {
//	    sp := settings.Global.Spec("probe.hot_retention_hours")
//	    if sp != nil {
//	        raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
//	        if err == nil && len(raw) > 0 {
//	            var v int
//	            if json.Unmarshal(raw, &v) == nil { ... }
//	        }
//	    }
//	}
//
// These helpers wrap that boilerplate so call sites stay short.

// GetPlatformInt reads a platform-scoped integer setting.
// Returns fallback if Global is nil, key is unknown, or value is malformed.
func GetPlatformInt(key string, fallback int) int {
	return getPlatformInt(key, fallback)
}

// GetPlatformBool reads a platform-scoped boolean setting.
func GetPlatformBool(key string, fallback bool) bool {
	return getPlatformBool(key, fallback)
}

// GetPlatformString reads a platform-scoped string setting.
func GetPlatformString(key, fallback string) string {
	return getPlatformString(key, fallback)
}

// GetPlatformFloat reads a platform-scoped float setting.
func GetPlatformFloat(key string, fallback float64) float64 {
	return getPlatformFloat(key, fallback)
}

// GetPlatformDuration reads a platform-scoped duration setting (stored as seconds).
func GetPlatformDuration(key string, fallbackSeconds int64) int64 {
	return int64(getPlatformInt(key, int(fallbackSeconds)))
}

func getPlatformInt(key string, fallback int) int {
	if Global == nil {
		return fallback
	}
	sp := Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("settings: int unmarshal failed", "key", key, "error", err)
		return fallback
	}
	return v
}

func getPlatformBool(key string, fallback bool) bool {
	if Global == nil {
		return fallback
	}
	sp := Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("settings: bool unmarshal failed", "key", key, "error", err)
		return fallback
	}
	return v
}

func getPlatformString(key, fallback string) string {
	if Global == nil {
		return fallback
	}
	sp := Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("settings: string unmarshal failed", "key", key, "error", err)
		return fallback
	}
	return v
}

func getPlatformFloat(key string, fallback float64) float64 {
	if Global == nil {
		return fallback
	}
	sp := Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("settings: float unmarshal failed", "key", key, "error", err)
		return fallback
	}
	return v
}
