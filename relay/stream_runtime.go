package relay

import (
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
)

type streamRuntimeConfig struct {
	upstreamTimeout    time.Duration
	streamTimeout      time.Duration
	streamChunkTimeout time.Duration
	firstByteTimeout   time.Duration
	keepaliveInterval  time.Duration
}

var streamConfigStore atomic.Pointer[config.Store]

func SetConfigStore(store *config.Store) {
	streamConfigStore.Store(store)
}

func currentStreamRuntimeConfig() streamRuntimeConfig {
	if store := streamConfigStore.Load(); store != nil {
		if cfg := store.Get(); cfg != nil {
			return streamRuntimeConfig{
				upstreamTimeout:    durationSecondsOrDefault(cfg.UpstreamTimeout, 120*time.Second),
				streamTimeout:      durationSecondsOrDefault(cfg.StreamTimeout, 900*time.Second),
				streamChunkTimeout: durationSecondsOrDefault(cfg.StreamChunkTimeout, 300*time.Second),
				firstByteTimeout:   durationSecondsOrDefault(cfg.FirstByteTimeout, 30*time.Second),
				keepaliveInterval:  durationSecondsOrDefault(cfg.KeepaliveInterval, 15*time.Second),
			}
		}
	}
	return streamRuntimeConfig{
		upstreamTimeout:    envDurationSeconds("LLM_GATEWAY_UPSTREAM_TIMEOUT", 120*time.Second),
		streamTimeout:      envDurationSeconds("LLM_GATEWAY_STREAM_TIMEOUT", 900*time.Second),
		streamChunkTimeout: envDurationSeconds("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", 300*time.Second),
		firstByteTimeout:   envDurationSeconds("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", 30*time.Second),
		keepaliveInterval:  envDurationSeconds("LLM_GATEWAY_KEEPALIVE_INTERVAL", 15*time.Second),
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

func StreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().streamTimeout
}

func UpstreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().upstreamTimeout
}
