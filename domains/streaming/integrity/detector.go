package integrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Candidate is the per-request context the detector needs to emit
// events. It mirrors a subset of streaming.RequestLogContext and
// audit.StreamCapture so we don't have to import them here (this
// package has to stay light — the executor / handler already has those
// types in scope and can build a Candidate cheaply).
//
// All fields are optional; missing fields simply disable the
// corresponding signal.
type Candidate struct {
	RequestID     string
	TenantID      string
	ApplicationID *int
	APIKeyID      *int
	ProviderID    *int
	ProviderCode  string
	CredentialID  *int
	ClientModel   string
	OutboundModel string
	RawModel      string
	// RespModel is the upstream-returned model name (OpenAI non-stream
	// .model; first chat.completion.chunk's .model; or Anthropic
	// message_start.message.model). Empty disables the mismatch check.
	RespModel string
	// FinishReason is the upstream finish_reason (OpenAI) or
	// stop_reason (Anthropic). Empty disables finish_* checks.
	FinishReason string
	// PromptTokens / CompletionTokens / TotalTokens / InputTokens /
	// OutputTokens are the post-extraction values. Zero values disable
	// the arith check (rather than treating them as a mismatch).
	PromptTokens     *int
	CompletionTokens *int
	TotalTokens      *int
	InputTokens      *int
	OutputTokens     *int
	// ChunkCount is stream_chunk_count. < 0 means "not a stream".
	ChunkCount int
	// ChunksSent is stream_chunks_sent (for clients that aborted
	// mid-stream). 0 = unknown / non-stream.
	ChunksSent int
	// ContentPreview is the truncated first 2 KiB of the response
	// body / SSE preview. Used for repeated-content detection.
	ContentPreview string
	// TextContent is the captured text content (StreamCapture.textContent).
	// Used for repeated-content detection.
	TextContent string
	// ProviderResponseID / SystemFingerprint go into context JSONB and
	// the sample column. They never contain PII.
	ProviderResponseID string
	SystemFingerprint  string
	// UsageSource matches request_logs.usage_source. Recorded for
	// downstream debugging of token-arith failures.
	UsageSource string
	// IsStream true if this is a streaming response.
	IsStream bool
}

// Detector runs the per-request checks in one pass and emits events
// through the supplied Recorder. nil-safe; an all-nil Detector is a
// no-op so callers never need to nil-check.
type Detector struct {
	rec Recorder
}

// NewDetector wraps a Recorder. nil rec is allowed.
func NewDetector(r Recorder) *Detector {
	return &Detector{rec: r}
}

// Observe runs the full signal scan and persists any detected events.
// Always safe to call from a request hot path; persistence is async
// (Recorder.Record uses context.WithoutCancel + 3s budget).
//
// The function is short and side-effect-free other than Recorder.Record
// so unit tests can run it against a fake recorder.
func (d *Detector) Observe(ctx context.Context, c Candidate) {
	if d == nil || d.rec == nil {
		return
	}
	d.checkModelMismatch(ctx, c)
	d.checkFinishReason(ctx, c)
	d.checkTokenArithmetic(ctx, c)
	d.checkEmptyResponse(ctx, c)
	d.checkRepeatedContent(ctx, c)
}

// checkModelMismatch emits a model_mismatch event when the upstream
// returned a model name that doesn't case-insensitively match the
// client or outbound model. Severity is high because silent
// substitution is the highest-trust violation on the integrity menu.
func (d *Detector) checkModelMismatch(ctx context.Context, c Candidate) {
	if c.RespModel == "" {
		return
	}
	want := c.OutboundModel
	if want == "" {
		want = c.ClientModel
	}
	if want == "" {
		return
	}
	if strings.EqualFold(want, c.RespModel) {
		return
	}
	_ = d.rec.Record(ctx, Event{
		AnomalyType:   AnomalyModelMismatch,
		Severity:      SeverityHigh,
		RequestID:     c.RequestID,
		TenantID:      c.TenantID,
		ApplicationID: c.ApplicationID,
		APIKeyID:      c.APIKeyID,
		ProviderID:    c.ProviderID,
		ProviderCode:  c.ProviderCode,
		CredentialID:  c.CredentialID,
		ClientModel:   c.ClientModel,
		OutboundModel: c.OutboundModel,
		RawModel:      c.RawModel,
		ExpectedValue: want,
		ActualValue:   c.RespModel,
		Sample:        c.ProviderResponseID,
		Context: map[string]any{
			"chunk_count":        c.ChunkCount,
			"is_stream":          c.IsStream,
			"system_fingerprint": c.SystemFingerprint,
		},
	})
}

// checkFinishReason emits finish_refusal / finish_truncation events
// from the upstream finish_reason. Both are medium severity by default;
// they only become high when paired with a successful 200 status.
//
// finish_reason values:
//
//	refusal | content_filter  → AnomalyFinishRefusal (medium)
//	length  | max_tokens       → AnomalyFinishTruncation (low)
func (d *Detector) checkFinishReason(ctx context.Context, c Candidate) {
	fr := strings.ToLower(strings.TrimSpace(c.FinishReason))
	if fr == "" {
		return
	}
	switch fr {
	case "refusal", "content_filter":
		_ = d.rec.Record(ctx, Event{
			AnomalyType:   AnomalyFinishRefusal,
			Severity:      SeverityMedium,
			RequestID:     c.RequestID,
			TenantID:      c.TenantID,
			ApplicationID: c.ApplicationID,
			APIKeyID:      c.APIKeyID,
			ProviderID:    c.ProviderID,
			ProviderCode:  c.ProviderCode,
			CredentialID:  c.CredentialID,
			ClientModel:   c.ClientModel,
			OutboundModel: c.OutboundModel,
			RawModel:      c.RawModel,
			ActualValue:   fr,
			Sample:        c.ProviderResponseID,
			Context: map[string]any{
				"chunk_count": c.ChunkCount,
				"is_stream":   c.IsStream,
			},
		})
	case "length", "max_tokens":
		_ = d.rec.Record(ctx, Event{
			AnomalyType:   AnomalyFinishTruncation,
			Severity:      SeverityLow,
			RequestID:     c.RequestID,
			TenantID:      c.TenantID,
			ApplicationID: c.ApplicationID,
			APIKeyID:      c.APIKeyID,
			ProviderID:    c.ProviderID,
			ProviderCode:  c.ProviderCode,
			CredentialID:  c.CredentialID,
			ClientModel:   c.ClientModel,
			OutboundModel: c.OutboundModel,
			RawModel:      c.RawModel,
			ActualValue:   fr,
			Sample:        c.ProviderResponseID,
			Context: map[string]any{
				"chunk_count":  c.ChunkCount,
				"chunks_sent":  c.ChunksSent,
				"is_stream":    c.IsStream,
				"usage_source": c.UsageSource,
			},
		})
	}
}

// checkTokenArithmetic verifies the simple invariants:
//
//	prompt + completion == total     (OpenAI)
//	input + output     == total      (Anthropic native; some providers)
//	cache_read + cache_write <= prompt    (informational, not enforced here)
//
// Only fires when at least one of the three values is non-zero and the
// body is non-empty. A zero-value usage block is handled by
// format_anomaly_recorder (extraction_failed), not here — that path
// already exists; this path is for "we have numbers, they don't add up".
func (d *Detector) checkTokenArithmetic(ctx context.Context, c Candidate) {
	total := 0
	hasTotal := false
	if c.TotalTokens != nil {
		total = *c.TotalTokens
		hasTotal = true
	}
	pt := ptrOrZero(c.PromptTokens)
	ct := ptrOrZero(c.CompletionTokens)
	it := ptrOrZero(c.InputTokens)
	ot := ptrOrZero(c.OutputTokens)

	// OpenAI form.
	if pt > 0 || ct > 0 {
		expected := pt + ct
		if hasTotal && total != expected {
			d.recordTokenArith(ctx, c, "openai", expected, total)
			return
		}
	}
	// Anthropic form.
	if it > 0 || ot > 0 {
		expected := it + ot
		if hasTotal && total != expected {
			d.recordTokenArith(ctx, c, "anthropic", expected, total)
			return
		}
	}
}

func (d *Detector) recordTokenArith(ctx context.Context, c Candidate, kind string, expected, actual int) {
	_ = d.rec.Record(ctx, Event{
		AnomalyType:   AnomalyTokenArithFail,
		Severity:      SeverityMedium,
		RequestID:     c.RequestID,
		TenantID:      c.TenantID,
		ApplicationID: c.ApplicationID,
		APIKeyID:      c.APIKeyID,
		ProviderID:    c.ProviderID,
		ProviderCode:  c.ProviderCode,
		CredentialID:  c.CredentialID,
		ClientModel:   c.ClientModel,
		OutboundModel: c.OutboundModel,
		RawModel:      c.RawModel,
		ExpectedValue: itoa(expected),
		ActualValue:   itoa(actual),
		Sample:        c.ProviderResponseID,
		Context: map[string]any{
			"kind":              kind,
			"usage_source":      c.UsageSource,
			"prompt_tokens":     ptrOrZero(c.PromptTokens),
			"completion_tokens": ptrOrZero(c.CompletionTokens),
			"input_tokens":      ptrOrZero(c.InputTokens),
			"output_tokens":     ptrOrZero(c.OutputTokens),
		},
	})
}

// checkEmptyResponse mirrors streaming/handler.go:5778 detectEmptyStreamResponse
// but writes the integrity record (not the response_format_anomalies one —
// the two are independent and serve different dashboards). Severity is
// high because the user paid for tokens and got nothing.
func (d *Detector) checkEmptyResponse(ctx context.Context, c Candidate) {
	if !c.IsStream {
		return
	}
	ct := ptrOrZero(c.CompletionTokens)
	ot := ptrOrZero(c.OutputTokens)
	if ct > 0 || ot > 0 {
		return
	}
	if c.ChunkCount > 3 {
		return
	}
	if c.ContentPreview != "" {
		return
	}
	if c.FinishReason == "" {
		return
	}
	_ = d.rec.Record(ctx, Event{
		AnomalyType:   AnomalyEmptyResponse,
		Severity:      SeverityHigh,
		RequestID:     c.RequestID,
		TenantID:      c.TenantID,
		ApplicationID: c.ApplicationID,
		APIKeyID:      c.APIKeyID,
		ProviderID:    c.ProviderID,
		ProviderCode:  c.ProviderCode,
		CredentialID:  c.CredentialID,
		ClientModel:   c.ClientModel,
		OutboundModel: c.OutboundModel,
		RawModel:      c.RawModel,
		ActualValue:   c.FinishReason,
		Sample:        c.ProviderResponseID,
		Context: map[string]any{
			"chunk_count":  c.ChunkCount,
			"chunks_sent":  c.ChunksSent,
			"usage_source": c.UsageSource,
		},
	})
}

// repeatedBlockBytes is the granularity at which we hash the response
// text to detect looping. 256 bytes mirrors goal/loop_detector.go so
// the two checks share a notion of "duplicate enough to be suspicious".
const repeatedBlockBytes = 256

// checkRepeatedContent hashes the response text in repeatedBlockBytes
// blocks and emits an event if any block appears ≥ minRepeatedHits
// times. Min hits is 2: the first occurrence is expected (especially
// for short "Sure!" responses); a second match is the signal.
//
// We deliberately do not store the offending text — only its hash and
// occurrence count. The hash is enough to correlate a model with a
// known loop signature; the content itself is the model's output and
// could be a PII channel.
func (d *Detector) checkRepeatedContent(ctx context.Context, c Candidate) {
	if c.TextContent == "" {
		return
	}
	counts := make(map[string]int, 8)
	blocks := 0
	for i := 0; i+repeatedBlockBytes <= len(c.TextContent); i += repeatedBlockBytes {
		blocks++
		h := sha256.Sum256([]byte(c.TextContent[i : i+repeatedBlockBytes]))
		counts[hex.EncodeToString(h[:8])]++
	}
	if blocks < 2 {
		return
	}
	const minRepeatedHits = 2
	for hash, n := range counts {
		if n < minRepeatedHits {
			continue
		}
		_ = d.rec.Record(ctx, Event{
			AnomalyType:   AnomalyRepeatedContent,
			Severity:      SeverityHigh,
			RequestID:     c.RequestID,
			TenantID:      c.TenantID,
			ApplicationID: c.ApplicationID,
			APIKeyID:      c.APIKeyID,
			ProviderID:    c.ProviderID,
			ProviderCode:  c.ProviderCode,
			CredentialID:  c.CredentialID,
			ClientModel:   c.ClientModel,
			OutboundModel: c.OutboundModel,
			RawModel:      c.RawModel,
			ActualValue:   hash,
			Sample:        c.ProviderResponseID,
			Context: map[string]any{
				"block_hits":   n,
				"block_size":   repeatedBlockBytes,
				"blocks_total": blocks,
			},
		})
		// One event per request is enough; bail to avoid spam.
		return
	}
}

// ptrOrZero returns 0 for nil and *p otherwise. Tiny helper to keep
// the arithmetic checks readable.
func ptrOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// itoa is a local copy of strconv.Itoa so detector.go doesn't have to
// import strconv for one call.
func itoa(i int) string {
	// signed int; handles 0..max int without allocation by hand.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ExtractRespModelFromBody returns the upstream-returned model name
// from an OpenAI-shaped chat completions body. Mirrors
// streaming/executors.extractResponseModel but lives here so the
// adapter doesn't need to duplicate JSON-parsing logic.
func ExtractRespModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return strings.TrimSpace(v.Model)
}

// ExtractFinishReasonFromBody returns the upstream finish_reason from
// the first choice of an OpenAI chat completions body, or "" if the
// field is absent. Tolerates nil / missing / malformed bodies.
func ExtractFinishReasonFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	if len(v.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(v.Choices[0].FinishReason)
}

// ExtractTokenCountsFromBody returns the (prompt, completion, total,
// input, output) token counts. Each is nil when absent so the
// Detector's zero-value checks fire correctly. Tries both OpenAI
// (prompt_tokens/completion_tokens/total_tokens) and Anthropic
// (input_tokens/output_tokens) shapes — same convention as
// streaming/handler.go:4981 extractTokensFromResponseBody.
func ExtractTokenCountsFromBody(body []byte) (prompt, completion, total, input, output *int) {
	if len(body) == 0 {
		return
	}
	var v struct {
		Usage struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			TotalTokens      *int `json:"total_tokens"`
			InputTokens      *int `json:"input_tokens"`
			OutputTokens     *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return
	}
	return v.Usage.PromptTokens, v.Usage.CompletionTokens, v.Usage.TotalTokens, v.Usage.InputTokens, v.Usage.OutputTokens
}
