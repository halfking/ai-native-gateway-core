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
	enableEmptyStreamGate bool
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
			return streamRuntimeConfig{
				upstreamTimeout:    durationSecondsOrDefault(cfg.UpstreamTimeout, 120*time.Second),
				streamTimeout:      durationSecondsOrDefault(cfg.StreamTimeout, 900*time.Second),
				streamChunkTimeout: durationSecondsOrDefault(cfg.StreamChunkTimeout, 300*time.Second),
				// 2026-08-04: 120→180s default. Reasoning models with large
				// tool-call contexts often exceed 120s to first byte.
				firstByteTimeout:         durationSecondsOrDefault(cfg.FirstByteTimeout, 180*time.Second),
				keepaliveInterval:        durationSecondsOrDefault(cfg.KeepaliveInterval, 15*time.Second),
				enablePreStreamKeepalive: cfg.EnablePreStreamKeepalive || envKeepaliveEnabled,
				enableEmptyStreamGate:    cfg.EnableEmptyStreamGate && envEmptyGateEnabled,
			}
		}
	}
	return streamRuntimeConfig{
		upstreamTimeout:    envDurationSeconds("LLM_GATEWAY_UPSTREAM_TIMEOUT", 120*time.Second),
		streamTimeout:      envDurationSeconds("LLM_GATEWAY_STREAM_TIMEOUT", 900*time.Second),
		streamChunkTimeout: envDurationSeconds("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", 300*time.Second),
		// 2026-08-04: 120→180s default for thinking/long-running models.
		firstByteTimeout:         envDurationSeconds("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", 180*time.Second),
		keepaliveInterval:        envDurationSeconds("LLM_GATEWAY_KEEPALIVE_INTERVAL", 15*time.Second),
		enablePreStreamKeepalive: envKeepaliveEnabled,
		enableEmptyStreamGate:    envEmptyGateEnabled,
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

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1"
}

func StreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().streamTimeout
}

func UpstreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().upstreamTimeout
}
