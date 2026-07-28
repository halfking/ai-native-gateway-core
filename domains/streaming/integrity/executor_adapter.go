package integrity

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// ExecutorAdapter bridges executors.IntegrityDetector (which speaks
// executors.IntegrityCandidate) with the integrity package's Detector
// (which speaks integrity.Candidate). The mapping is intentionally
// straight field copies; any transform (e.g. parsing token counts out
// of ResponseBody) happens inside the Detector itself.
//
// This file is the only place in the integrity package that depends on
// the executors package; the reverse direction (executors → integrity)
// is what we want and is unidirectional.
type ExecutorAdapter struct {
	d *Detector
}

// NewExecutorAdapter wraps a Detector into the executors.IntegrityDetector
// interface. The Detector's nil-safety applies — passing a nil Detector
// is allowed and Observe is a no-op.
func NewExecutorAdapter(d *Detector) *ExecutorAdapter {
	return &ExecutorAdapter{d: d}
}

// Observe implements executors.IntegrityDetector. It builds an
// integrity.Candidate from the executor's view and delegates.
func (a *ExecutorAdapter) Observe(ctx context.Context, ec executors.IntegrityCandidate) {
	if a == nil || a.d == nil {
		return
	}
	c := Candidate{
		RequestID:          ec.RequestID,
		TenantID:           ec.TenantID,
		ApplicationID:      ec.ApplicationID,
		APIKeyID:           ec.APIKeyID,
		ProviderID:         ec.ProviderID,
		ProviderCode:       ec.ProviderCode,
		CredentialID:       ec.CredentialID,
		ClientModel:        ec.ClientModel,
		OutboundModel:      ec.OutboundModel,
		RawModel:           ec.RawModel,
		RespModel:          ec.RespModel,
		FinishReason:       ec.FinishReason,
		PromptTokens:       ec.PromptTokens,
		CompletionTokens:   ec.CompletionTokens,
		TotalTokens:        ec.TotalTokens,
		InputTokens:        ec.InputTokens,
		OutputTokens:       ec.OutputTokens,
		ChunkCount:         ec.ChunkCount,
		ChunksSent:         ec.ChunksSent,
		ContentPreview:     ec.ContentPreview,
		TextContent:        ec.TextContent,
		ProviderResponseID: ec.ProviderResponseID,
		SystemFingerprint:  ec.SystemFingerprint,
		UsageSource:        ec.UsageSource,
		IsStream:           ec.IsStream,
	}
	// Preserve values already extracted by the executor or stream capture.
	// Parse the response body only to fill gaps; this avoids overwriting
	// valid stream metadata with empty fallback values.
	if len(ec.ResponseBody) > 0 {
		if c.RespModel == "" {
			c.RespModel = ExtractRespModelFromBody(ec.ResponseBody)
		}
		if c.FinishReason == "" {
			c.FinishReason = ExtractFinishReasonFromBody(ec.ResponseBody)
		}
		pt, ct, tot, it, ot := ExtractTokenCountsFromBody(ec.ResponseBody)
		if c.PromptTokens == nil {
			c.PromptTokens = pt
		}
		if c.CompletionTokens == nil {
			c.CompletionTokens = ct
		}
		if c.TotalTokens == nil {
			c.TotalTokens = tot
		}
		if c.InputTokens == nil {
			c.InputTokens = it
		}
		if c.OutputTokens == nil {
			c.OutputTokens = ot
		}
	}
	// The streaming path fills ChunkCount/TextContent/ContentPreview
	// directly on the Candidate; the executor path doesn't have
	// stream counters, so we leave them at zero values (which the
	// Detector interprets as "not a stream").
	a.d.Observe(ctx, c)
}

// Compile-time assertion: ExecutorAdapter satisfies the executor-side
// interface.
var _ executors.IntegrityDetector = (*ExecutorAdapter)(nil)
