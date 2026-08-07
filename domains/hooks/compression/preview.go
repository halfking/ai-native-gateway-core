// Package compression - preview.go (docs/omni-ref3 C7)
//
// Preview is the read-only, side-effect-free counterpart of
// SessionCompressor.Prepare. It answers "what would compression do to this
// body?" for the admin preview endpoint (POST /api/admin/compression/preview)
// without touching any shared state.
//
// Why NOT reuse Prepare:
//
//	Prepare is a mutating hot-path call. Running it for a preview would
//	  1. write the derived state back into the session cache (poisoning the
//	     real session for the next live request),
//	  2. issue a REAL LLM summary call (seconds of latency + upstream quota
//	     spend, triggered by an admin clicking a button),
//	  3. populate the C3 result memo with a preview-derived entry,
//	  4. emit lossiness / trigger metrics that would skew dashboards with
//	     non-production events.
//
//	Preview therefore composes only the deterministic, pure primitives
//	(StripThinkingBlocksOnly / PruneOldMediaBlocks / ShouldTriggerWindow /
//	StripToolInfo / mechanicalTrim / prefix.Stabilize). It passes state=nil
//	so the IDLE trigger and the mutual-exclusion guard — both of which need
//	persisted session state — are inert by construction.
//
// LLM-summary honesty:
//
//	The LLM-summary branch CANNOT be previewed without spending money, so
//	Preview does not attempt it. When the window fires, Preview reports the
//	mechanical-trim outcome (the deterministic fallback Prepare uses when
//	the summary fails or the breaker is open) and sets SummarySkipped=true
//	plus WouldTryLLMSummary=true. Callers must render that distinction:
//	the live path may shrink FURTHER than the preview shows. Preview is a
//	lower bound on savings, never an upper bound.

package compression

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/cache/prefix"
)

// PreviewOptions are the knobs an admin can vary to compare outcomes.
// The zero value is valid: Mode defaults to the live resolved mode and
// Protocol defaults to "openai".
type PreviewOptions struct {
	// Protocol is "openai" or "anthropic-messages". Selects the rebuilder
	// and trim implementation. Empty → "openai".
	Protocol string

	// ContextWindow is the target model's window in tokens. 0 disables the
	// TOKEN trigger and makes mechanicalTrim a no-op, exactly as in Prepare.
	ContextWindow int

	// Mode overrides the compression mode. nil → use the live resolved mode
	// (LoadMode()), so a preview with no override reflects current config.
	Mode *Mode

	// PruneMedia enables the A3 inline image/audio pruning step, keeping the
	// most recent KeepMedia blocks. Off by default because Prepare does not
	// run it on the main path yet.
	PruneMedia bool

	// KeepMedia is the number of most-recent media blocks to retain when
	// PruneMedia is true. <=0 falls back to PruneOldMediaBlocks' default.
	KeepMedia int
}

// PreviewStage records one deterministic transform's effect, so the admin UI
// can show WHERE the bytes went rather than only a final total.
type PreviewStage struct {
	Name        string `json:"name"`
	Applied     bool   `json:"applied"`
	BytesBefore int    `json:"bytes_before"`
	BytesAfter  int    `json:"bytes_after"`

	// Detail carries the StripResult counters for strip/prune stages.
	// Nil for stages that do not strip (e.g. mechanical_trim).
	Detail *StripResult `json:"detail,omitempty"`
}

// PreviewResult is the full preview report.
type PreviewResult struct {
	Mode     string `json:"mode"`
	Protocol string `json:"protocol"`

	BytesBefore  int `json:"bytes_before"`
	BytesAfter   int `json:"bytes_after"`
	TokensBefore int `json:"tokens_before"`
	TokensAfter  int `json:"tokens_after"`
	MsgsBefore   int `json:"msgs_before"`
	MsgsAfter    int `json:"msgs_after"`

	// BytesSaved / TokensSaved are clamped at 0: a transform that grew the
	// body reports 0 saved rather than a negative number.
	BytesSaved  int     `json:"bytes_saved"`
	TokensSaved int     `json:"tokens_saved"`
	BytesRatio  float64 `json:"bytes_ratio"`

	// WindowTriggered is the trigger reason, "" when the body is under budget.
	WindowTriggered string `json:"window_triggered"`

	// Strategy is the strategy Preview could deterministically demonstrate:
	// "" (no rewrite), "strip_only", or "mechanical_trim".
	Strategy string `json:"strategy"`

	// Lossiness classifies the previewed outcome via the SAME function the
	// live path uses, so preview and production labels never diverge.
	Lossiness string `json:"lossiness"`

	// WouldTryLLMSummary is true when the live path would attempt an LLM
	// summary at this point (window fired, not degraded).
	WouldTryLLMSummary bool `json:"would_try_llm_summary"`

	// SummarySkipped is always true when WouldTryLLMSummary is true: it flags
	// that the reported numbers EXCLUDE the LLM-summary saving. The live path
	// may shrink further.
	SummarySkipped bool `json:"summary_skipped"`

	// CompressedPrefixHash is the C8/D7 stable-prefix hash of the previewed
	// output, so an admin can verify prefix stability across two previews.
	CompressedPrefixHash string `json:"compressed_prefix_hash,omitempty"`

	// Stages is the ordered per-transform breakdown.
	Stages []PreviewStage `json:"stages"`

	// TokensThreshold is the byte threshold the TOKEN trigger compared
	// against (0 when ContextWindow was unknown).
	TokensThreshold int `json:"tokens_threshold"`
}

// Preview reports what compression would do to body, without side effects.
//
// It never calls an LLM, never reads or writes the session cache, never
// touches the C3 memo, and emits no metrics. Safe to call repeatedly from an
// admin endpoint. Returns nil for an empty body.
func Preview(body []byte, opts PreviewOptions) *PreviewResult {
	if len(body) == 0 {
		return nil
	}

	protocol := opts.Protocol
	if protocol == "" {
		protocol = "openai"
	}
	mode := LoadMode()
	if opts.Mode != nil {
		mode = *opts.Mode
	}

	res := &PreviewResult{
		Mode:         mode.String(),
		Protocol:     protocol,
		BytesBefore:  len(body),
		TokensBefore: estimateBodyTokens(body),
		MsgsBefore:   countMessages(body),
		Stages:       []PreviewStage{},
	}

	cur := body
	smart := mode == ModeSmart || mode == ModeAggressive

	// ── Stage 1: thinking strip (always safe, non-destructive) ────────────
	// Mirrors Prepare phase 4a. Only runs in smart/aggressive, same as live.
	if smart {
		stripped, sr := StripThinkingBlocksOnly(cur)
		stage := PreviewStage{
			Name:        "strip_thinking",
			Applied:     sr.DidStrip,
			BytesBefore: len(cur),
			BytesAfter:  len(stripped),
			Detail:      sr,
		}
		if sr.DidStrip {
			cur = stripped
		} else {
			stage.BytesAfter = len(cur)
		}
		res.Stages = append(res.Stages, stage)
	}

	// ── Stage 2: media prune (A3, opt-in) ─────────────────────────────────
	if opts.PruneMedia {
		pruned, sr := PruneOldMediaBlocks(cur, opts.KeepMedia)
		stage := PreviewStage{
			Name:        "prune_media",
			Applied:     sr.DidStrip,
			BytesBefore: len(cur),
			BytesAfter:  len(pruned),
			Detail:      sr,
		}
		if sr.DidStrip {
			cur = pruned
		} else {
			stage.BytesAfter = len(cur)
		}
		res.Stages = append(res.Stages, stage)
	}

	// ── Stage 3: window trigger evaluation ────────────────────────────────
	// state=nil and streamStarted=false: the IDLE trigger and the
	// recently-compressed degradation guard both require persisted state, so
	// they are inert here by construction. A preview therefore reports the
	// TOKEN/COUNT verdict only — which is the deterministic part.
	win := ShouldTriggerWindow(cur, nil, opts.ContextWindow, false, time.Now())
	res.WindowTriggered = win.Reason
	res.TokensThreshold = win.Threshold

	if win.ShouldTrigger && smart {
		// ── Stage 4: tool-round strip (destructive; live path gates it on
		// the window having fired, and so do we) ──────────────────────────
		stripped, sr := StripToolInfo(cur, protocol)
		stage := PreviewStage{
			Name:        "strip_tool_rounds",
			Applied:     sr.DidStrip,
			BytesBefore: len(cur),
			BytesAfter:  len(stripped),
			Detail:      sr,
		}
		if sr.DidStrip {
			cur = stripped
			res.Strategy = "strip_only"
		} else {
			stage.BytesAfter = len(cur)
		}
		res.Stages = append(res.Stages, stage)

		// The live path would now attempt an LLM summary unless the window
		// degraded it. We cannot run that here (cost), so flag it and show
		// the mechanical-trim fallback instead.
		res.WouldTryLLMSummary = !win.Degraded
		res.SummarySkipped = res.WouldTryLLMSummary

		// ── Stage 5: mechanical trim (deterministic fallback) ─────────────
		trimmed := mechanicalTrim(cur, opts.ContextWindow, protocol)
		trimStage := PreviewStage{
			Name:        "mechanical_trim",
			BytesBefore: len(cur),
			BytesAfter:  len(cur),
		}
		if len(trimmed) < len(cur) {
			cur = trimmed
			trimStage.Applied = true
			trimStage.BytesAfter = len(trimmed)
			res.Strategy = "mechanical_trim"
		}
		res.Stages = append(res.Stages, trimStage)
	}

	// ── Final accounting ──────────────────────────────────────────────────
	res.BytesAfter = len(cur)
	res.TokensAfter = estimateBodyTokens(cur)
	res.MsgsAfter = countMessages(cur)

	if res.BytesBefore > res.BytesAfter {
		res.BytesSaved = res.BytesBefore - res.BytesAfter
	}
	if res.TokensBefore > res.TokensAfter {
		res.TokensSaved = res.TokensBefore - res.TokensAfter
	}
	if res.BytesBefore > 0 {
		res.BytesRatio = float64(res.BytesAfter) / float64(res.BytesBefore)
	}

	// Reuse the live classifier so preview and production labels agree.
	// SummaryMarker is "" because Preview never injects one.
	res.Lossiness = classifyLossiness(res.Strategy, "")

	// C8/D7 stable prefix hash of the previewed output.
	if _, report, err := prefix.Stabilize(cur, prefix.Options{TailTurns: 1}); err == nil && report != nil {
		res.CompressedPrefixHash = report.PrefixHash
	}

	return res
}
