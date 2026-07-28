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
		ProviderResponseID: ec.ProviderResponseID,
		SystemFingerprint:  ec.SystemFingerprint,
		UsageSource:        ec.UsageSource,
		IsStream:           ec.IsStream,
	}
	// Extract model + finish_reason + token counts from the response
	// body so the Detector can do its checks without re-parsing
	// upstream.
	if len(ec.ResponseBody) > 0 {
		c.RespModel = ExtractRespModelFromBody(ec.ResponseBody)
		c.FinishReason = ExtractFinishReasonFromBody(ec.ResponseBody)
		pt, ct, tot, it, ot := ExtractTokenCountsFromBody(ec.ResponseBody)
		if pt != nil || ct != nil || tot != nil || it != nil || ot != nil {
			c.PromptTokens = pt
			c.CompletionTokens = ct
			c.TotalTokens = tot
			c.InputTokens = it
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
