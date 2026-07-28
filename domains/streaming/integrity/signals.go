// Package integrity records per-request and per-(cred,model) signals that
// detect upstream LLM integrity issues: silent model substitution (注水 /
// 假的替代), finish_reason=refusal or truncation, token-arithmetic failures,
// empty streams, repeated content, and 7-day system_fingerprint drift.
//
// Design constraints (see plan: 154 / 6h log + model quality detection):
//   - All writes are async and nil-safe; the request hot path MUST NOT block
//     on integrity detection. Recording takes a context.WithoutCancel + 3s
//     budget so a client disconnect can never leave a record half-written.
//   - The "sample" column carries only PII-safe metadata
//     (provider_response_id, system_fingerprint, finish_reason,
//     chunk_count, usage_source). NEVER the user prompt or model output.
//   - Detection runs in detector.go; persistence runs in recorder.go.
//     The two are wired together via the Recorder interface in handler/main
//     and never on the request hot path synchronously.
package integrity

// AnomalyType enumerates the per-(cred,model) integrity signals we surface.
// New values are append-only — the admin UI and SQL views group by this
// column, so renaming breaks dashboards. Add a new value and an admin
// filter, do not rename.
type AnomalyType string

const (
	// AnomalyModelMismatch: upstream returned a different model than the
	// client requested (silent substitution / 注水 / 假的替代). Detected for
	// OpenAI non-stream via response.model, OpenAI stream via the first
	// chat.completion.chunk's model field, and Anthropic via message_start
	// (existing side-channel in anthropic_passthrough_stream.go:195).
	AnomalyModelMismatch AnomalyType = "model_mismatch"

	// AnomalyFinishRefusal: finish_reason in {refusal, content_filter}.
	// The Anthropic → OpenAI translator already maps this to
	// finish_reason=content_filter; we tag it as an integrity event so
	// operators can filter "how many refusals today per (cred, model)?"
	AnomalyFinishRefusal AnomalyType = "finish_refusal"

	// AnomalyFinishTruncation: finish_reason in {length, max_tokens}.
	// Indicates the upstream hit its output cap and cut the response
	// off; the request was 200 OK from a wire perspective but
	// semantically incomplete.
	AnomalyFinishTruncation AnomalyType = "finish_truncation"

	// AnomalyTokenArithFail: usage arithmetic invariant violated, e.g.
	// total_tokens != prompt_tokens + completion_tokens (or
	// input_tokens + output_tokens for Anthropic) and the body is
	// non-empty. Distinct from AnomalyExtractionFailed in
	// response_format_anomalies: that one fires when the block is
	// missing entirely; this one fires when the block is present but
	// the numbers don't reconcile.
	AnomalyTokenArithFail AnomalyType = "token_arith_fail"

	// AnomalyEmptyResponse: success-status stream with 0 completion
	// tokens and ≤3 chunks and no content preview. Mirrors
	// detectEmptyStreamResponse in streaming/handler.go:5778 but
	// records as an integrity event so dashboard operators can
	// separate the symptom (NIM empty stream) from generic
	// "extraction failed" rows.
	AnomalyEmptyResponse AnomalyType = "empty_response"

	// AnomalyRepeatedContent: the model produced ≥2 identical 256-byte
	// blocks of text content within a single response — strong signal
	// of a stuck / looping upstream model or a mid-stream substitution.
	// Adapted from domains/hooks/goal/loop_detector.go:hashResponse;
	// promoted to a per-request integrity signal rather than a
	// goal-session-only check.
	AnomalyRepeatedContent AnomalyType = "repeated_content"

	// AnomalyFingerprintDrift: detected by the bg/integrity_fingerprint_drift
	// background worker. The dominant system_fingerprint for a
	// (cred, model) pair shifted > 3σ in the last hour vs the 7-day
	// baseline. Indicates a provider rolled a new model version or
	// started routing through a different upstream silently.
	AnomalyFingerprintDrift AnomalyType = "fingerprint_drift"
)

// Severity tiers (mirrors response_format_anomalies). "critical" is
// always recorded; everything else is subject to the sample ratio
// (default 0.1 = 10%).
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Event is the recorder input. Builder methods keep the call sites
// readable; the recorder itself is JSON-stored in model_integrity_events.
//
// Recorder implementations MUST treat this struct as read-only — copies
// may be retained across goroutines (Record passes through
// context.WithoutCancel).
type Event struct {
	AnomalyType   AnomalyType
	Severity      Severity
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
	// ExpectedValue / ActualValue are short machine-readable strings
	// that the admin UI can display in a diff column. They are
	// intended for things like "want=glm-5.2 got=glm-5.1" or
	// "want=length got=stop". Empty when the signal has no diff.
	ExpectedValue string
	ActualValue   string
	// Sample is the PII-safe payload (see package doc). Length is
	// capped by TruncateForSample to keep the table small.
	Sample string
	// Context is a structured payload — JSON-encoded by the recorder
	// into the context JSONB column. Optional.
	Context map[string]any
}
