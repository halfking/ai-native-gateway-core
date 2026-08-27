// Package requestfact defines the versioned request fact and archive wire contract.
package requestfact

import (
	"encoding/json"
	"time"
)

const (
	EnvelopeVersionV1        = 1
	PayloadVersionV1         = 1
	CodecVersionV1           = 1
	ProjectionEventVersionV1 = 1

	CurrentEnvelopeVersion        = EnvelopeVersionV1
	CurrentPayloadVersion         = PayloadVersionV1
	CurrentCodecVersion           = CodecVersionV1
	CurrentProjectionEventVersion = ProjectionEventVersionV1
)

type ArchiveState string

const (
	ArchiveStateActive          ArchiveState = "active"
	ArchiveStateTerminalPending ArchiveState = "terminal_pending"
	ArchiveStateCleanupPending  ArchiveState = "cleanup_pending"
)

type WarningCode string

const (
	WarningOptionalFieldOmitted WarningCode = "optional_field_omitted"
	WarningLossReported         WarningCode = "loss_reported"
	WarningDerivedFieldMissing  WarningCode = "derived_field_missing"
)

// ConversionWarning records an allowed omission or lossy projection. Core facts
// must instead fail validation or encoding.
type ConversionWarning struct {
	Code      WarningCode `json:"code"`
	Field     string      `json:"field"`
	Detail    string      `json:"detail,omitempty"`
	Retryable bool        `json:"retryable"`
}

type Identity struct {
	TenantID        string `json:"tenant_id"`
	RequestID       string `json:"request_id"`
	SessionID       string `json:"session_id,omitempty"`
	TurnID          string `json:"turn_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	ParentRequestID string `json:"parent_request_id,omitempty"`
}

type Lifecycle struct {
	Status      string    `json:"status"`
	Success     *bool     `json:"success,omitempty"`
	ErrorKind   string    `json:"error_kind,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	DeadlineAt  time.Time `json:"deadline_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

type Routing struct {
	ClientProtocol   string `json:"client_protocol"`
	ClientModel      string `json:"client_model"`
	OutboundProtocol string `json:"outbound_protocol,omitempty"`
	OutboundModel    string `json:"outbound_model,omitempty"`
	ProviderID       string `json:"provider_id,omitempty"`
	CredentialID     string `json:"credential_id,omitempty"`
	CanonicalModel   string `json:"canonical_model,omitempty"`
	ConversionPath   string `json:"conversion_path,omitempty"`
}

// ContentDocument keeps a raw JSON protocol or IR document. Callers must provide
// valid JSON; the codec never substitutes an empty document after failure.
type ContentDocument struct {
	RawBody     json.RawMessage `json:"raw_body,omitempty"`
	CanonicalIR json.RawMessage `json:"canonical_ir,omitempty"`
	Protocol    string          `json:"protocol,omitempty"`
	Extensions  json.RawMessage `json:"extensions,omitempty"`
}

type ResponseContent struct {
	RawBody       json.RawMessage `json:"raw_body,omitempty"`
	CanonicalIR   json.RawMessage `json:"canonical_ir,omitempty"`
	ClientIR      json.RawMessage `json:"client_ir,omitempty"`
	StreamSummary json.RawMessage `json:"stream_summary,omitempty"`
	StreamChunks  json.RawMessage `json:"stream_chunks,omitempty"`
	Extensions    json.RawMessage `json:"extensions,omitempty"`
}

type Usage struct {
	PromptTokens     int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens int64   `json:"completion_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	CachedTokens     int64   `json:"cached_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	Cost             float64 `json:"cost,omitempty"`
	Currency         string  `json:"currency,omitempty"`
}

type Timeline struct {
	T0            time.Time `json:"t0,omitempty"`
	T1            time.Time `json:"t1,omitempty"`
	T2            time.Time `json:"t2,omitempty"`
	T3            time.Time `json:"t3,omitempty"`
	T4            time.Time `json:"t4,omitempty"`
	T5            time.Time `json:"t5,omitempty"`
	T6            time.Time `json:"t6,omitempty"`
	T7            time.Time `json:"t7,omitempty"`
	T8            time.Time `json:"t8,omitempty"`
	T9            time.Time `json:"t9,omitempty"`
	TTFTMillis    int64     `json:"ttft_millis,omitempty"`
	LatencyMillis int64     `json:"latency_millis,omitempty"`
}

type Integrity struct {
	RequestBodySHA256  string `json:"request_body_sha256,omitempty"`
	OutboundBodySHA256 string `json:"outbound_body_sha256,omitempty"`
	ResponseBodySHA256 string `json:"response_body_sha256,omitempty"`
}

// CanonicalRequestFact is the once-built terminal request fact. It is a
// versioned domain document, not a request_logs or session_v2 table model.
type CanonicalRequestFact struct {
	Identity    Identity            `json:"identity"`
	Lifecycle   Lifecycle           `json:"lifecycle"`
	Routing     Routing             `json:"routing"`
	Request     ContentDocument     `json:"request"`
	Upstream    ContentDocument     `json:"upstream,omitempty"`
	Response    ResponseContent     `json:"response,omitempty"`
	Usage       Usage               `json:"usage,omitempty"`
	Timeline    Timeline            `json:"timeline,omitempty"`
	Attachments json.RawMessage     `json:"attachments,omitempty"`
	Extensions  json.RawMessage     `json:"extensions,omitempty"`
	Warnings    []ConversionWarning `json:"warnings,omitempty"`
	Integrity   Integrity           `json:"integrity,omitempty"`
}

type ArchiveMetadata struct {
	State       ArchiveState `json:"state"`
	Attempts    int          `json:"attempts,omitempty"`
	LastError   string       `json:"last_error,omitempty"`
	PersistedAt time.Time    `json:"persisted_at,omitempty"`
}

// RequestArchiveEnvelope is the on-disk contract for a request fact. Future
// archive storage owns atomic I/O and cleanup; this package owns the codec.
type RequestArchiveEnvelope struct {
	EnvelopeVersion        int                  `json:"envelope_version"`
	PayloadVersion         int                  `json:"payload_version"`
	CodecVersion           int                  `json:"codec_version"`
	ProjectionEventVersion int                  `json:"projection_event_version"`
	CapturedAt             time.Time            `json:"captured_at"`
	Archive                ArchiveMetadata      `json:"archive"`
	Payload                CanonicalRequestFact `json:"payload"`
	PayloadSHA256          string               `json:"payload_sha256"`
	Extensions             json.RawMessage      `json:"extensions,omitempty"`
}
