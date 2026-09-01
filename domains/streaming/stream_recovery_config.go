package streaming

import (
	"os"
	"strconv"
	"time"
)

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

// envInt64 reads an integer env var, falling back to def when unset or
// unparsable. Shared by the FR-12 holdback knobs.
func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envInt(key string, def int) int {
	return int(envInt64(key, int64(def)))
}

// maxRecoveryHoldbackWindowMS is the largest millisecond duration that fits in
// time.Duration. Larger env values would overflow during conversion and must
// never turn a requested holdback into a negative duration.
const maxRecoveryHoldbackWindowMS = (1<<63 - 1) / int64(time.Millisecond)

// RecoveryHoldbackFromEnv returns the L1 revocable-window parameters that
// configure the per-attempt commit gate (AttemptCommitGate). The window is
// OPT-IN: it is disabled unless the operator explicitly enables it via env,
// because a non-zero window delays the client's first token and changes
// survival/failover timing (FR-12 L1, handoff P1 — requires canary
// validation before turning on broadly):
//
//   - LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS   (unset → disabled; e.g. 5000)
//   - LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS (used only when window set;
//     default 20 when the window var is present but this one is empty)
//
// When enabled, the first HoldbackMaxChunks semantic frames (or HoldbackWindow
// after the first frame, whichever first) are held in the attempt-local buffer
// WITHOUT committing to the client, so an in-window upstream interruption
// discards and fails over transparently instead of escalating to
// resume_blocked/committed_output. Unstable models (glm-5.2, minimax-m3) drop
// the connection a few chunks in far more often than they fail after a full
// response, so this converts most of those into invisible retries.
func RecoveryHoldbackFromEnv() (window time.Duration, maxChunks int) {
	if os.Getenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS") == "" {
		return 0, 0
	}
	windowMS := envInt64("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", 0)
	if windowMS <= 0 || windowMS > maxRecoveryHoldbackWindowMS {
		return 0, 0
	}
	maxChunks = envInt("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", DefaultHoldbackMaxChunks)
	if maxChunks <= 0 {
		maxChunks = DefaultHoldbackMaxChunks
	}
	return time.Duration(windowMS) * time.Millisecond, maxChunks
}

// RecoveryHoldbackForModel returns the L1 revocable-window parameters tuned
// for the specific model's stability characteristics. Unstable models (glm-5.2,
// minimax-m3, minimax-text-01) that frequently drop connections in the first
// few chunks receive extended holdback windows to convert those interruptions
// into transparent retries rather than committed_output / resume_blocked errors.
//
// The model-specific configuration takes precedence over env vars; if a model
// has no specific tuning, this falls back to RecoveryHoldbackFromEnv().
//
// Tuning rationale (2026-09-01 P0 fix):
//   - glm-5.2 / minimax-m3: observed to interrupt frequently within first 5-10
//     chunks, far more often than after stable output. Extended window (10s/50
//     chunks) covers the unstable startup period while keeping latency impact
//     bounded (10s is well under the 2h interactive deadline).
//   - Other models: retain default 5s/20 chunks or env override.
//
// Operators can override via model-specific env vars:
//   - LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS_<MODEL>
//   - LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS_<MODEL>
// where <MODEL> is the uppercase model name with hyphens replaced by underscores.
func RecoveryHoldbackForModel(model string) (window time.Duration, maxChunks int) {
	// Check model-specific env override first
	modelEnvKey := "LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS_" + toEnvKey(model)
	if os.Getenv(modelEnvKey) != "" {
		windowMS := envInt64(modelEnvKey, 0)
		if windowMS > 0 && windowMS <= maxRecoveryHoldbackWindowMS {
			chunksKey := "LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS_" + toEnvKey(model)
			maxChunks = envInt(chunksKey, DefaultHoldbackMaxChunks)
			if maxChunks <= 0 {
				maxChunks = DefaultHoldbackMaxChunks
			}
			return time.Duration(windowMS) * time.Millisecond, maxChunks
		}
	}

	// Apply model-specific defaults for unstable models
	if isUnstableModel(model) {
		// Extended window for models that frequently interrupt in first few chunks
		return 10 * time.Second, 50
	}

	// Fall back to global env or defaults
	return RecoveryHoldbackFromEnv()
}

// isUnstableModel reports whether the model has known instability characteristics
// that warrant an extended holdback window. This list is curated based on
// production observations and can be extended as new unstable models are identified.
func isUnstableModel(model string) bool {
	switch model {
	case "glm-5.2", "minimax-m3", "minimax-text-01":
		return true
	default:
		return false
	}
}

// toEnvKey converts a model name to an environment variable key suffix by
// uppercasing and replacing hyphens with underscores (e.g., "glm-5.2" → "GLM_5_2").
func toEnvKey(model string) string {
	var result []rune
	for _, r := range model {
		if r == '-' || r == '.' {
			result = append(result, '_')
		} else if r >= 'a' && r <= 'z' {
			result = append(result, r-'a'+'A')
		} else {
			result = append(result, r)
		}
	}
	return string(result)
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
