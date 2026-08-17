package dispatch

import (
	"github.com/kaixuan/llm-gateway-go/hotconfig"
)

// Config holds the hot-reloadable dispatch tuning knobs. All keys MUST carry
// the `llmgw_` prefix or hotconfig's WHERE key LIKE 'llmgw_%' filter skips
// them (see hotconfig/hotconfig.go:87). Mirror of the LoadNodeFailoverConfig
// pattern (executors/node_failover.go:110).
type Config struct {
	// MaxQueueDepth is the per-credential Tier-2 queue capacity (the
	// "waiting room"). 0 = unlimited (discouraged).
	MaxQueueDepth int
	// MaxQueueWaitMS is the longest a request may wait across the pipeline
	// before the forwarder gives up pacing and routes to failover.
	MaxQueueWaitMS int
	// StatsBuffer is the Tier-3 event-bus channel capacity. Events are
	// dropped (and counted) when full so stats never block the hot path.
	StatsBuffer int
	// DispatcherWorkers is the ① Model Dispatcher worker-pool size.
	DispatcherWorkers int
	// FailoverWorkers is the ③ Failover Mover worker-pool size.
	FailoverWorkers int
	// RetryPerCredential is the same-credential retry budget before a
	// credential is marked tried and the mover switches credentials. 0 = no
	// same-cred retry (switch immediately on pre-firstbyte failure).
	RetryPerCredential int
}

// DefaultConfig returns conservative defaults used when hotconfig is absent.
func DefaultConfig() Config {
	return Config{
		MaxQueueDepth:      1024,
		MaxQueueWaitMS:     5000,
		StatsBuffer:        256,
		DispatcherWorkers:  8,
		FailoverWorkers:    8,
		RetryPerCredential: MaxNodeFailures - 1,
	}
}

// LoadConfig reads dispatch tuning from hotconfig (live-reloadable). hotCfg
// may be nil (returns DefaultConfig).
func LoadConfig(hotCfg *hotconfig.Config) Config {
	if hotCfg == nil {
		return DefaultConfig()
	}
	return Config{
		MaxQueueDepth:      clampInt(hotCfg.GetInt("llmgw_dispatch_max_queue_depth", 1024), 0, 100000),
		MaxQueueWaitMS:     clampInt(hotCfg.GetInt("llmgw_dispatch_max_queue_wait_ms", 5000), 0, 60000),
		StatsBuffer:        clampInt(hotCfg.GetInt("llmgw_dispatch_stats_buffer", 256), 0, 4096),
		DispatcherWorkers:  clampInt(hotCfg.GetInt("llmgw_dispatch_dispatcher_workers", 8), 1, 256),
		FailoverWorkers:    clampInt(hotCfg.GetInt("llmgw_dispatch_failover_workers", 8), 1, 256),
		RetryPerCredential: clampInt(hotCfg.GetInt("llmgw_dispatch_retry_per_credential", MaxNodeFailures-1), 0, MaxNodeFailures-1),
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
