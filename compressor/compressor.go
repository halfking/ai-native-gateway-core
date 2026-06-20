// Package compressor - compressor.go (Round 47 / v7 T8)
//
// Three-mode dispatcher (v7 §2 / §3.4):
//   mode=0 (off)             → no compression, body passes through
//   mode=1 (auto_threshold)  → pre-request gate, compress if body exceeds
//                              cand.ContextWindow × fraction × charsPerToken
//   mode=2 (on_4xx)          → invoked only by executor_chat.go /
//                              executor_anthropic.go AFTER upstream returns
//                              context_length_exceeded 4xx (or heuristic)
//                              (see routing/context_summarize.go for the
//                              post-error recovery path)
//
// Per v7 §3.4 the dispatcher never decides WHERE to put summary content
// (that's rebuilder_openai.go / rebuilder_anthropic.go). It only decides:
//   1. Should this body be compressed? (mode gate)
//   2. Which path runs first? (mechanical → memora L1 → llm summary)
//   3. What telemetry fields do we write to request_logs.compression_*?
//
// The actual three-tier decompression logic stays in routing/context_summarize.go
// until T12 (v7 §7) refactors those helpers into this package. Until then,
// Compress() returns the original body and a structured "would-compress"
// envelope so callers can implement the multi-pass flow themselves.
//
// See docs/llm-gateway-go/2026-06-18-compression-v7-final.md §3.4 + §3.5.

package compressor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// Mode is the three-state compression gate (v7 §2).
//   ModeOff           (0)  → never compress
//   ModeAutoThreshold (1)  → pre-request gate, dynamic threshold
//   ModeOn4xx         (2)  → only after upstream context_length_exceeded 4xx
type Mode int

const (
	ModeOff Mode = iota
	ModeAutoThreshold
	ModeOn4xx
)

// String implements fmt.Stringer for logging / metrics labels.
func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeAutoThreshold:
		return "auto_threshold"
	case ModeOn4xx:
		return "on_4xx"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// envMode reads LLM_GATEWAY_COMPRESSION_MODE (v7 §2). Falls back to
// ModeOn4xx (the default per user Q1) on parse error or unset.
//
// 0 → ModeOff, 1 → ModeAutoThreshold, 2 → ModeOn4xx.
func envMode() Mode {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_COMPRESSION_MODE"))
	if raw == "" {
		return ModeOn4xx
	}
	switch raw {
	case "0":
		return ModeOff
	case "1":
		return ModeAutoThreshold
	case "2":
		return ModeOn4xx
	default:
		return ModeOn4xx
	}
}

// LoadMode resolves compression.mode via settings.Global (DB > env > default).
// Falls back to envModeLegacy() when settings.Global is not yet initialised
// (early-init paths, unit tests). Kept for hot-path speed: we cache the
// value in NewCompressor() so each request does NOT hit the registry.
func LoadMode() Mode {
	if settings.Global != nil {
		if sp := settings.Global.Spec("compression.mode"); sp != nil {
			v, _, err := settings.Global.EffectiveValue(sp.Scope, sp.Key, "")
			if err == nil && len(v) > 0 {
				var s string
				if err := json.Unmarshal(v, &s); err == nil {
					switch s {
					case "off":
						return ModeOff
					case "auto_threshold":
						return ModeAutoThreshold
					case "on_4xx":
						return ModeOn4xx
					}
				}
			}
		}
	}
	return envMode()
}

// CompressionReason is the canonical value written to
// request_logs.compression_reason (v7 §3.1 schema).
type CompressionReason string

const (
	ReasonNone             CompressionReason = ""
	ReasonAutoThreshold    CompressionReason = "mode_1_auto_threshold"
	ReasonOn4xx            CompressionReason = "mode_2_on_4xx"
	ReasonWarmupSkipped    CompressionReason = "mode_warmup_skipped"
)

// CompressionStrategy is the canonical value written to
// request_logs.compression_strategy.
type CompressionStrategy string

const (
	StrategyNone              CompressionStrategy = ""
	StrategyMechanicalTrim    CompressionStrategy = "mechanical_trim"
	StrategyMemoraL1Inject    CompressionStrategy = "memora_l1_inject"
	StrategyLLMSummary        CompressionStrategy = "llm_summary"
	StrategyNoop              CompressionStrategy = "noop"
)

// Meta is the compression telemetry payload written to
// request_logs.compression_meta (JSONB column). v7 §3.2 schema.
// Only the fields the dispatcher actually fills are populated; downstream
// helpers (LLM summary call site) may add latency_ms / model_used.
type Meta struct {
	TokensBefore        *int     `json:"tokens_before,omitempty"`
	TokensAfter         *int     `json:"tokens_after,omitempty"`
	BytesBefore         int      `json:"bytes_before"`
	BytesAfter          int      `json:"bytes_after,omitempty"`
	ContextWindowUsed   *int     `json:"context_window_used,omitempty"`
	ThresholdBytes      int      `json:"threshold_bytes,omitempty"`
	DroppedMessages     *int     `json:"dropped_messages,omitempty"`
	SummaryChars        *int     `json:"summary_chars,omitempty"`
	ModelUsed           string   `json:"model_used,omitempty"`
	LatencyMs           int      `json:"latency_ms,omitempty"`
	MemoraFactsUsed     *int     `json:"memora_facts_used,omitempty"`
	WarmupSkipped       bool     `json:"warmup_skipped,omitempty"`
	FirstUserRetained   bool     `json:"first_user_retained"`
	SystemRetained      bool     `json:"system_retained"`
	ReasonDetail        string   `json:"reason_detail,omitempty"`
}

// Marshal serializes Meta to JSON bytes suitable for
// request_logs.compression_meta (JSONB column).
func (m Meta) Marshal() []byte {
	b, _ := json.Marshal(m)
	return b
}

// Compressor is the public dispatcher. Build via NewCompressor() at
// executor init time. Holds the env-derived mode + estimator so the hot
// path doesn't re-read os.Getenv.
type Compressor struct {
	mode Mode
	est  *Estimator
}

// NewCompressor builds a Compressor with the current env config.
// Cheap to construct (no I/O); can be built per-request if needed.
func NewCompressor() *Compressor {
	return &Compressor{
		mode: LoadMode(),
		est:  NewEstimator(),
	}
}

// Mode returns the active mode (read-only).
func (c *Compressor) Mode() Mode {
	if c == nil {
		return ModeOff
	}
	return c.mode
}

// Estimator returns the underlying estimator (read-only).
func (c *Compressor) Estimator() *Estimator {
	if c == nil {
		return nil
	}
	return c.est
}

// ShouldCompressPreRequest is the mode=1 (auto_threshold) pre-request gate.
// Returns true when the body exceeds the dynamic threshold AND mode is
// ModeAutoThreshold. Returns false otherwise (including ModeOff and
// ModeOn4xx - the latter is invoked AFTER the 4xx, not before).
//
// This is the call that goes into executor_chat.go's pre-request trim
// path (line ~144) and executor_anthropic.go's prepareAnthropicRequestBody.
func (c *Compressor) ShouldCompressPreRequest(body []byte, contextWindow int) bool {
	if c == nil || c.mode != ModeAutoThreshold {
		return false
	}
	return c.est.NeedsCompression(body, contextWindow)
}

// Compress runs the compression flow for the given body. It is the
// single entry point that:
//   1. Decides whether compression should fire (per mode + heuristics)
//   2. For mode=1: gates on body size vs context window
//   3. Returns the rewritten body (or original if no-op) plus the
//      telemetry envelope (reason, strategy, meta) the caller writes
//      to request_logs.compression_*.
//
// v7 §3.4 says the dispatcher orchestrates three tiers
// (mechanical → memora L1 → llm summary). T8 lays the scaffolding for
// that orchestration; the actual tier-by-tier helpers stay in
// routing/context_summarize.go until T12 refactors them into this
// package. Until then Compress() implements only the mechanical trim
// path (the cheapest tier) and reports the rest as noop with a
// reason_detail pointing the caller at the post-error recovery flow.
//
// Returns:
//   newBody        — original body if no compression, otherwise the
//                     rebuilt body (mechanical trim today)
//   reason         — ReasonNone | ReasonAutoThreshold | ReasonOn4xx | ReasonWarmupSkipped
//   strategy       — StrategyNone | StrategyMechanicalTrim | StrategyNoop | ...
//   meta           — telemetry payload (JSONB-ready)
//   didCompress    — true iff the body was rewritten
func (c *Compressor) Compress(body []byte, contextWindow int) (newBody []byte, reason CompressionReason, strategy CompressionStrategy, meta Meta, didCompress bool) {
	meta = Meta{
		BytesBefore:       len(body),
		FirstUserRetained: false,
		SystemRetained:    false,
	}
	if body == nil {
		meta.ReasonDetail = "empty body"
		return body, ReasonNone, StrategyNoop, meta, false
	}

	// ModeOff: never compress.
	if c == nil || c.mode == ModeOff {
		meta.ReasonDetail = "mode=off"
		return body, ReasonNone, StrategyNoop, meta, false
	}

	// ModeOn4xx: caller MUST NOT pre-request-compress; the post-error
	// recovery in executor_chat.go / executor_anthropic.go calls
	// CompressAfter4xx (below) instead.
	if c.mode == ModeOn4xx {
		meta.ReasonDetail = "mode=on_4xx (caller must use CompressAfter4xx)"
		return body, ReasonNone, StrategyNoop, meta, false
	}

	// ModeAutoThreshold: gate on body size vs dynamic threshold.
	if !c.ShouldCompressPreRequest(body, contextWindow) {
		meta.ReasonDetail = "body under threshold; no compression needed"
		if contextWindow > 0 {
			meta.ThresholdBytes = c.est.ThresholdBytes(contextWindow)
			meta.ContextWindowUsed = &contextWindow
		}
		return body, ReasonNone, StrategyNoop, meta, false
	}

	// === Tier 1 (mechanical trim) ===
	// This is the cheapest path. transform.CompressMessagesIfNeeded /
	// CompressAnthropicMessagesIfNeeded are the existing in-place
	// sliding-window trims. They preserve system messages (existing
	// A-track behaviour) and tool-round integrity (per the v7 §6
	// tool_use ↔ tool_result atomic drop rule in transform/ctx_compress.go).
	//
	// v7 B-track (first user preservation) is NOT yet implemented in the
	// mechanical tier — that's a T11 enhancement to transform/ctx_compress.go.
	// Until then, transform's sliding window can drop the first user.
	// The dispatcher reports this in ReasonDetail so callers know.
	trimmed := compressMechanical(body, contextWindow)
	if len(trimmed) >= len(body) {
		// Mechanical couldn't make room. Mark as noop; caller should
		// fall through to memora L1 / LLM summary via the post-error path.
		meta.ReasonDetail = "mechanical trim had no effect; needs memora or LLM fallback"
		meta.ThresholdBytes = c.est.ThresholdBytes(contextWindow)
		meta.ContextWindowUsed = &contextWindow
		return body, ReasonAutoThreshold, StrategyNoop, meta, false
	}

	// Mechanical succeeded.
	before := len(body)
	after := len(trimmed)
	tokensBefore := before * 10 / 35 // chars/3.5 → ×10/35 → approx tokens
	tokensAfter := after * 10 / 35
	dropped := countDroppedMessages(body, trimmed)
	meta.BytesAfter = after
	meta.TokensBefore = &tokensBefore
	meta.TokensAfter = &tokensAfter
	meta.DroppedMessages = &dropped
	meta.ContextWindowUsed = &contextWindow
	meta.ThresholdBytes = c.est.ThresholdBytes(contextWindow)
	meta.ReasonDetail = fmt.Sprintf("body %d > threshold %d (window=%d × 0.8 × 3.5)",
		before, meta.ThresholdBytes, contextWindow)
	return trimmed, ReasonAutoThreshold, StrategyMechanicalTrim, meta, true
}

// CompressAfter4xx is the mode=2 (on_4xx) entry point. Called by
// executor_chat.go / executor_anthropic.go AFTER the upstream returns
// context_length_exceeded. Differs from Compress() in that mode is
// always treated as forced (we KNOW compression is needed because the
// upstream already rejected the body).
//
// Today (T8) the dispatcher delegates to mechanical trim only. The full
// three-tier recovery (mechanical → memora L1 → LLM summary) lives in
// routing/context_summarize.go and will be migrated in T12.
//
// Returns the same envelope as Compress().
func (c *Compressor) CompressAfter4xx(body []byte, contextWindow int) (newBody []byte, reason CompressionReason, strategy CompressionStrategy, meta Meta, didCompress bool) {
	meta = Meta{
		BytesBefore:       len(body),
		FirstUserRetained: false,
		SystemRetained:    false,
	}
	if body == nil {
		return body, ReasonNone, StrategyNoop, meta, false
	}
	if contextWindow <= 0 {
		meta.ReasonDetail = "unknown context window; cannot pick compression strategy"
		return body, ReasonOn4xx, StrategyNoop, meta, false
	}

	trimmed := compressMechanical(body, contextWindow)
	if len(trimmed) >= len(body) {
		meta.ReasonDetail = "4xx recovery: mechanical trim had no effect"
		meta.ContextWindowUsed = &contextWindow
		return body, ReasonOn4xx, StrategyNoop, meta, false
	}
	before := len(body)
	after := len(trimmed)
	meta.BytesAfter = after
	meta.TokensBefore = ptrInt(before * 10 / 35)
	meta.TokensAfter = ptrInt(after * 10 / 35)
	meta.DroppedMessages = ptrInt(countDroppedMessages(body, trimmed))
	meta.ContextWindowUsed = &contextWindow
	meta.ThresholdBytes = (contextWindow * 8 / 10) * 35 / 10
	meta.ReasonDetail = fmt.Sprintf("4xx recovery: body %d > window %d capacity", before, contextWindow)
	return trimmed, ReasonOn4xx, StrategyMechanicalTrim, meta, true
}

// compressMechanical is the in-place sliding-window trim. Thin wrapper
// around transform.CompressMessagesIfNeeded so the dispatcher doesn't
// need to import transform directly. The mechanical tier already
// preserves system messages (existing behaviour); tool-round integrity
// is preserved by transform/ctx_compress.go's dropExtent logic.
func compressMechanical(body []byte, contextWindow int) []byte {
	if contextWindow <= 0 {
		return body
	}
	return CompressMessagesIfNeededBody(body, contextWindow)
}

// CompressMessagesIfNeededBody is a package-level alias for
// transform.CompressMessagesIfNeeded so compressor doesn't need to
// import transform at the call site. Avoids a circular dep while still
// routing through the canonical trim implementation.
func CompressMessagesIfNeededBody(body []byte, contextWindow int) []byte {
	// Local import would be cleaner but we want to keep this file
	// dependency-free for the test that exercises Meta alone.
	return trimMessagesBody(body, contextWindow)
}

// trimMessagesBody is the actual delegator. Lives in its own file
// (compressor_trim.go) so the transform import doesn't pollute this
// file's diff for readers focused on the dispatcher logic.

// countDroppedMessages returns the count of messages dropped by a trim,
// computed by parsing both bodies and diffing the messages array.
func countDroppedMessages(before, after []byte) int {
	var b, a struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(before, &b)
	_ = json.Unmarshal(after, &a)
	if len(b.Messages) == 0 || len(a.Messages) == 0 {
		return 0
	}
	diff := len(b.Messages) - len(a.Messages)
	if diff < 0 {
		return 0
	}
	return diff
}

func ptrInt(v int) *int { return &v }
