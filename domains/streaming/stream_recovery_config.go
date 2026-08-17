package streaming

import "time"

// stream_recovery_config.go — 会话优化 v4 FR-12（R12.2/R12.6/R12.7 参数）
//
// StreamRecoveryConfig bounds the FR-12 stream-interruption recovery ladder.
// All knobs are hot-configurable by the integrator (settings_kv/hotconfig
// 通道，§4 非功能需求)；零值走 withDefaults 的规格默认。

// FR-12 defaults (R12.2/R12.7 + 参数总表 §5).
const (
	// DefaultStreamRecoveryMaxAttempts is stream_recovery_max_attempts.
	DefaultStreamRecoveryMaxAttempts = 3
	// DefaultSameNodeStreamRetries is same_node_stream_retries (L0).
	DefaultSameNodeStreamRetries = 2
	// DefaultHoldbackWindowMS is the L1 revocable window (5s or 20 chunks,
	// whichever first).
	DefaultHoldbackWindowMS = int64(5 * time.Second / time.Millisecond)
	// DefaultHoldbackMaxChunks is the L1 chunk half of the window.
	DefaultHoldbackMaxChunks = 20
	// DefaultAlignmentThresholdBP: L2 prefix-alignment score threshold in
	// basis points (0..10000). ≥ threshold 抑制重复前缀只转发 suffix.
	DefaultAlignmentThresholdBP = 9000
	// DefaultCommittedPrefixWindowBytes bounds the L2 CommittedPrefix byte
	// window per request (hash + 有限窗口字节, R12.2 L3).
	DefaultCommittedPrefixWindowBytes = 64 * 1024
	// DefaultCommittedPrefixCacheCapacity bounds how many requests keep a
	// CommittedPrefix entry (内存上限保护, R12.7 防风暴).
	DefaultCommittedPrefixCacheCapacity = 1024
)

// StreamRecoveryConfig is the request-scoped FR-12 knob set.
type StreamRecoveryConfig struct {
	// MaxRecoveryAttempts (stream_recovery_max_attempts): total recovery
	// attempts per request; reaching it degrades to L4 (UT-SR-09).
	MaxRecoveryAttempts int
	// SameNodeStreamRetries (same_node_stream_retries): L0 same-node resends
	// before moving off the node (UT-SR-02).
	SameNodeStreamRetries int
	// HoldbackWindowMS / HoldbackMaxChunks define the L1 revocable window
	// after the FIRST semantic content: whichever limit hits first closes
	// the window (UT-SR-03/04).
	HoldbackWindowMS  int64
	HoldbackMaxChunks int
	// AlignmentThresholdBP gates L2 prefix suppression (UT-SR-06).
	AlignmentThresholdBP uint16
	// CommittedPrefixWindowBytes / CommittedPrefixCacheCapacity bound the
	// L2 CommittedPrefix memory (UT-SR-05).
	CommittedPrefixWindowBytes   int
	CommittedPrefixCacheCapacity int
	// ContinuationEnabled toggles L3 (default OFF; whitelist-gated).
	ContinuationEnabled bool
	// ContinuationTaskTypes / ContinuationModels: L3 whitelist. Empty
	// whitelist = nobody qualifies even when ContinuationEnabled (默认关，
	// 显式白名单开启, UT-SR-07).
	ContinuationTaskTypes []string
	ContinuationModels    []string
	// AllowVisibleRestart: 运营策略明确允许 L4 visible restart (R12.6 条件四).
	AllowVisibleRestart bool
	// ClientStreamUndo: 客户端声明 X-Gw-Capabilities: stream-undo (R12.6
	// 条件三/R12.8)。由 ingress 从请求头解析后注入。
	ClientStreamUndo bool
}

// DefaultStreamRecoveryConfig returns the spec defaults.
func DefaultStreamRecoveryConfig() StreamRecoveryConfig {
	return StreamRecoveryConfig{}.withDefaults()
}

func (c StreamRecoveryConfig) withDefaults() StreamRecoveryConfig {
	if c.MaxRecoveryAttempts <= 0 {
		c.MaxRecoveryAttempts = DefaultStreamRecoveryMaxAttempts
	}
	if c.SameNodeStreamRetries <= 0 {
		c.SameNodeStreamRetries = DefaultSameNodeStreamRetries
	}
	if c.HoldbackWindowMS <= 0 {
		c.HoldbackWindowMS = DefaultHoldbackWindowMS
	}
	if c.HoldbackMaxChunks <= 0 {
		c.HoldbackMaxChunks = DefaultHoldbackMaxChunks
	}
	if c.AlignmentThresholdBP == 0 {
		c.AlignmentThresholdBP = DefaultAlignmentThresholdBP
	}
	if c.CommittedPrefixWindowBytes <= 0 {
		c.CommittedPrefixWindowBytes = DefaultCommittedPrefixWindowBytes
	}
	if c.CommittedPrefixCacheCapacity <= 0 {
		c.CommittedPrefixCacheCapacity = DefaultCommittedPrefixCacheCapacity
	}
	return c
}
