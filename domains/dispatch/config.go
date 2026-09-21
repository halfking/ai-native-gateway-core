package dispatch

import (
	"runtime"

	"github.com/kaixuan/llm-gateway-go/hotconfig"
)

// Config holds the hot-reloadable dispatch tuning knobs. All keys MUST carry
// the `llmgw_` prefix or hotconfig's WHERE key LIKE 'llmgw_%' filter skips
// them (see hotconfig/hotconfig.go:87). Mirror of the LoadNodeFailoverConfig
// pattern (executors/node_failover.go:110).
//
// ── Capacity layering (会话优化 v4 R1.3/R1.8, three DISTINCT semantics) ──
// Do not collapse these into one knob:
//
//  1. Observation projection (domains/requestjourney): 100 total / 30 per
//     model / 100 per node — DISPLAY capacity only (llmgw_request_history_*).
//     Being squeezed out of a projection never affects execution.
//  2. Scheduling queues (this package): MaxQueueDepth = 300 per Tier-1 model
//     lane and Tier-2 credential lane — EXECUTION admission bound
//     (llmgw_dispatch_max_queue_depth). Full ⇒ immediate overflow (R1.3);
//     the same bound doubles as the registry's unfinished admission limit so
//     pending+in-flight work can never exceed one queue's worth (R1.8).
//  3. Request registry (registry.go): RegistryCapacity = 1000 total entries
//     — bookkeeping bound, NOT a rejection threshold
//     (llmgw_dispatch_registry_capacity). Pressure evicts the oldest
//     completed entry; CompletedWatermark = 50 (soft, FIFO by completed_at).
type Config struct {
	// TotalQueueCapacity is the Tier-0 total FIFO admission bound — a
	// bounded waiting room from Submit until the request moves into its
	// model lane. The slot is released on that hand-off (or client cancel),
	// NOT held for the request's full lifecycle: model/credential queues and
	// in-flight forwards are governed by MaxQueueDepth and the governors.
	// Registry bookkeeping does not participate in this bound. Default 1000.
	TotalQueueCapacity int

	// MaxQueueDepth is the per-credential Tier-2 (and per-model Tier-1)
	// queue capacity (the "waiting room"). 0 = unlimited (discouraged).
	// Default 300 since v4 (was 1024): full ⇒ reject immediately with an
	// overflow error instead of buffering unbounded work (R1.3).
	MaxQueueDepth int
	// MaxQueueWaitMS is the longest a request may wait across the pipeline
	// before the forwarder gives up pacing and routes to failover.
	// Default 0 since v4 (was 5000): admission is zero-wait — a saturated
	// governor fails over immediately instead of parking the request
	// (R1.3). Values <= 0 mean NO wait (the old 30s hard floor was
	// removed; see Pipeline.queueWaitBudget).
	MaxQueueWaitMS int
	// StatsBuffer is the Tier-3 event-bus channel capacity. Events are
	// dropped (and counted) when full so stats never block the hot path.
	StatsBuffer int
	// DispatcherWorkers is the ① Model Dispatcher worker-pool size (执行器
	// 数量, v6 G-Ⅰ). Zero-value/absent config falls back to
	// AdaptiveWorkerCount() — CPU cores minus one — because dispatchers do
	// CPU-side scheduling work (model resolve, route, requeue tagging).
	// Upstream HTTP forwarding is deliberately NOT sized by CPU: per-attempt
	// goroutines are bounded by the per-credential Governors
	// (concurrency/RPM/TPM), which is what aligns output to the vendor's
	// declared limits.
	DispatcherWorkers int
	// FailoverWorkers is the ③ Failover Mover worker-pool size (same
	// adaptive default as DispatcherWorkers).
	FailoverWorkers int
	// RetryPerCredential is the same-credential retry budget before a
	// credential is marked tried and the mover switches credentials. 0 = no
	// same-cred retry (switch immediately on pre-firstbyte failure).
	RetryPerCredential int
	// RegistryCapacity bounds the request lifecycle registry's total
	// entries. NOT a rejection threshold: pressure evicts the oldest
	// completed entry (R1.1). Hotconfig: llmgw_dispatch_registry_capacity.
	RegistryCapacity int
	// CompletedWatermark is the registry's completed-entry soft watermark:
	// completed entries beyond it are evicted FIFO by completed_at.
	// Pending/in-flight entries are never evicted (R1.8). Hotconfig:
	// llmgw_dispatch_completed_watermark. 0 keeps the default.
	CompletedWatermark int
	// DimensionTTLSeconds is the per-entry TTL of the per-dimension
	// membership index (v6 G-Ⅳ, 分维队列). 0 keeps the default (900).
	// Hotconfig: llmgw_dispatch_dimension_ttl_seconds.
	DimensionTTLSeconds int
	// DimensionCapacity is the per-dimension ring capacity (v6 G-Ⅳ).
	// 0 keeps the default (128); negative disables the index.
	// Hotconfig: llmgw_dispatch_dimension_capacity.
	DimensionCapacity int
}

// adaptiveWorkerBounds clamp AdaptiveWorkerCount.
const (
	adaptiveWorkerMin = 2
	adaptiveWorkerMax = 32
)

// AdaptiveWorkerCount returns the default executor-pool size: CPU cores minus
// one, clamped to [2, 32] (v6 G-Ⅰ). The "-1" reserves one core for the
// runtime (GC, netpoll) and the request-handling goroutines feeding the
// queues. Hotconfig keys can still pin an exact count.
func AdaptiveWorkerCount() int {
	n := runtime.NumCPU() - 1
	if n < adaptiveWorkerMin {
		n = adaptiveWorkerMin
	}
	if n > adaptiveWorkerMax {
		n = adaptiveWorkerMax
	}
	return n
}

// DefaultConfig returns conservative defaults used when hotconfig is absent.
func DefaultConfig() Config {
	workers := AdaptiveWorkerCount()
	return Config{
		TotalQueueCapacity:  1000,
		MaxQueueDepth:       300,
		MaxQueueWaitMS:      0,
		StatsBuffer:         256,
		DispatcherWorkers:   workers,
		FailoverWorkers:     workers,
		RetryPerCredential:  MaxNodeFailures - 1,
		RegistryCapacity:    DefaultRegistryCapacity,
		CompletedWatermark:  DefaultCompletedWatermark,
		DimensionTTLSeconds: DefaultDimensionTTLSeconds,
		DimensionCapacity:   DefaultDimensionCapacity,
	}
}

// LoadConfig reads dispatch tuning from hotconfig (live-reloadable). hotCfg
// may be nil (returns DefaultConfig). Worker counts default to the adaptive
// CPU-based value when the key is unset.
func LoadConfig(hotCfg *hotconfig.Config) Config {
	if hotCfg == nil {
		return DefaultConfig()
	}
	workers := AdaptiveWorkerCount()
	return Config{
		TotalQueueCapacity:  clampInt(hotCfg.GetInt("llmgw_dispatch_total_queue_capacity", 1000), 1, 100000),
		MaxQueueDepth:       clampInt(hotCfg.GetInt("llmgw_dispatch_max_queue_depth", 300), 0, 100000),
		MaxQueueWaitMS:      clampInt(hotCfg.GetInt("llmgw_dispatch_max_queue_wait_ms", 0), 0, 60000),
		StatsBuffer:         clampInt(hotCfg.GetInt("llmgw_dispatch_stats_buffer", 256), 0, 4096),
		DispatcherWorkers:   clampInt(hotCfg.GetInt("llmgw_dispatch_dispatcher_workers", workers), 1, 256),
		FailoverWorkers:     clampInt(hotCfg.GetInt("llmgw_dispatch_failover_workers", workers), 1, 256),
		RetryPerCredential:  clampInt(hotCfg.GetInt("llmgw_dispatch_retry_per_credential", MaxNodeFailures-1), 0, MaxNodeFailures-1),
		RegistryCapacity:    clampInt(hotCfg.GetInt(HotKeyRegistryCapacity, DefaultRegistryCapacity), 1, 100000),
		CompletedWatermark:  clampInt(hotCfg.GetInt(HotKeyCompletedWatermark, DefaultCompletedWatermark), 0, 100000),
		DimensionTTLSeconds: clampInt(hotCfg.GetInt("llmgw_dispatch_dimension_ttl_seconds", DefaultDimensionTTLSeconds), 1, 86400),
		DimensionCapacity:   clampInt(hotCfg.GetInt("llmgw_dispatch_dimension_capacity", DefaultDimensionCapacity), -1, 100000),
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
