package streaming

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
)

type streamRuntimeConfig struct {
	upstreamTimeout          time.Duration
	streamTimeout            time.Duration
	streamChunkTimeout       time.Duration
	firstByteTimeout         time.Duration
	keepaliveInterval        time.Duration
	enablePreStreamKeepalive bool
	// 2026-07-15: content-gate buffers the first few chunks before writing
	// to the client, so the executor can transparently failover an empty
	// upstream stream (notably NIM) before the client sees [DONE].
	enableEmptyStreamGate       bool
	emptyStreamEarlyEmptyChunks int
}

var streamConfigStore atomic.Pointer[config.Store]

func SetConfigStore(store *config.Store) {
	streamConfigStore.Store(store)
}

func currentStreamRuntimeConfig() streamRuntimeConfig {
	// 2026-08-04: default ON (was false). All-protocol pre-stream keepalive
	// is the primary fix for "agent task interrupted through the gateway" —
	// see handler.go startPreStreamKeepalive. Set the env to "false"/"0" to
	// opt back out.
	envKeepaliveEnabled := envBool("LLM_GATEWAY_ENABLE_PRE_STREAM_KEEPALIVE", true)
	envEmptyGateEnabled := envBool("LLM_GATEWAY_ENABLE_EMPTY_STREAM_GATE", true)
	if store := streamConfigStore.Load(); store != nil {
		if cfg := store.Get(); cfg != nil {
			earlyEmptyChunks := cfg.EmptyStreamEarlyEmptyChunks
			if raw := os.Getenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS"); raw != "" {
				earlyEmptyChunks = envNonNegativeInt("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", 3)
			} else if earlyEmptyChunks <= 0 {
				earlyEmptyChunks = 3
			}
			return streamRuntimeConfig{
				upstreamTimeout:    durationSecondsOrDefault(cfg.UpstreamTimeout, 120*time.Second),
				streamTimeout:      durationSecondsOrDefault(cfg.StreamTimeout, 900*time.Second),
				streamChunkTimeout: durationSecondsOrDefault(cfg.StreamChunkTimeout, 300*time.Second),
				// 2026-08-04: 120→180s default. Reasoning models with large
				// tool-call contexts often exceed 120s to first byte.
				firstByteTimeout:            durationSecondsOrDefault(cfg.FirstByteTimeout, 180*time.Second),
				keepaliveInterval:           durationSecondsOrDefault(cfg.KeepaliveInterval, 15*time.Second),
				enablePreStreamKeepalive:    cfg.EnablePreStreamKeepalive || envKeepaliveEnabled,
				enableEmptyStreamGate:       cfg.EnableEmptyStreamGate && envEmptyGateEnabled,
				emptyStreamEarlyEmptyChunks: earlyEmptyChunks,
			}
		}
	}
	earlyEmptyChunks := envNonNegativeInt("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", 3)
	return streamRuntimeConfig{
		upstreamTimeout:    envDurationSeconds("LLM_GATEWAY_UPSTREAM_TIMEOUT", 120*time.Second),
		streamTimeout:      envDurationSeconds("LLM_GATEWAY_STREAM_TIMEOUT", 900*time.Second),
		streamChunkTimeout: envDurationSeconds("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", 300*time.Second),
		// 2026-08-04: 120→180s default for thinking/long-running models.
		firstByteTimeout:            envDurationSeconds("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", 180*time.Second),
		keepaliveInterval:           envDurationSeconds("LLM_GATEWAY_KEEPALIVE_INTERVAL", 15*time.Second),
		enablePreStreamKeepalive:    envKeepaliveEnabled,
		enableEmptyStreamGate:       envEmptyGateEnabled,
		emptyStreamEarlyEmptyChunks: earlyEmptyChunks,
	}
}

func durationSecondsOrDefault(seconds int, def time.Duration) time.Duration {
	if seconds <= 0 {
		return def
	}
	return time.Duration(seconds) * time.Second
}

func envDurationSeconds(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	s, err := strconv.Atoi(v)
	if err != nil || s <= 0 {
		return def
	}
	return time.Duration(s) * time.Second
}

func envNonNegativeInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1"
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func StreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().streamTimeout
}

func UpstreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().upstreamTimeout
}

// ModelAliasPrefix returns the configured client-facing model name prefix
// that gets stripped before internal routing. Default "kx-". Empty string means disabled.
func ModelAliasPrefix() string {
	if store := streamConfigStore.Load(); store != nil {
		if cfg := store.Get(); cfg != nil {
			return cfg.ModelAliasPrefix
		}
	}
	return envOrDefault("LLM_GATEWAY_MODEL_ALIAS_PREFIX", "kx-")
}
