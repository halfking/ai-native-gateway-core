package executors

import (
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// AuditContext is the runtime correlation handle for a single
// request. The handler builds the base fields once after parsing the
// body and headers; the executor refreshes the per-attempt fields
// every time a candidate credential is selected. Raw log emissions
// and anomaly reports read this context to populate their
// correlation envelopes.
//
// 2026-07-28 §5.1 / §5.7 — replaces the legacy envelopeFromParams
// helper which only populated GWSessionID, GWTaskID (incorrectly
// bound to the model name), TenantID, APIKeyID, ApplicationID.
type AuditContext struct {
	// Base fields populated by the handler after body parse.
	RequestID       string
	ClientRequestID string
	GWSessionID     string
	GWTaskID        string // resolved X-Gw-Task-Id or session.TaskID; NEVER the model name
	ParentRequestID string
	TenantID        string
	ApplicationID   string
	APIKeyID        int
	TraceID         string
	SpanID          string
	// Protocol is set by streaming bridges when the per-attempt
	// envelope is refreshed, so the wire-level raw log entry carries
	// the protocol of the upstream the executor actually chose.
	Protocol string

	// Per-attempt fields refreshed by the executor when each
	// candidate credential is selected.
	ProviderID       int
	CredentialID     int
	AttemptNo        int
	UpstreamEndpoint string

	// ChunkIndex is incremented atomically by streaming bridges so
	// each upstream frame can be tagged with a sequence number
	// without a lock.
	ChunkIndex atomic.Int64
}

// AuditContextFromRequest builds the base AuditContext from the
// resolved request, session, body, and key info. Headers
// X-Request-Id, X-Gw-Task-Id, X-Trace-Id, X-Span-Id,
// X-Parent-Request-Id are preferred when present; session fields
// are the fallback. The body parameter is reserved for future
// derivation (e.g. extracting model name as a fallback for
// GWTaskID) and is currently unused.
func AuditContextFromRequest(r *http.Request, sn *session.Session, body []byte, ki *authentication.KeyInfo) *AuditContext {
	_ = body // reserved; see comment above
	ctx := &AuditContext{}
	if r != nil {
		ctx.ClientRequestID = r.Header.Get("X-Request-Id")
		ctx.GWTaskID = r.Header.Get("X-Gw-Task-Id")
		ctx.TraceID = r.Header.Get("X-Trace-Id")
		ctx.SpanID = r.Header.Get("X-Span-Id")
		ctx.ParentRequestID = r.Header.Get("X-Parent-Request-Id")
	}
	if sn != nil {
		if ctx.GWSessionID == "" {
			ctx.GWSessionID = sn.SessionID
		}
		if ctx.GWTaskID == "" {
			ctx.GWTaskID = sn.TaskID
		}
		if ctx.TenantID == "" {
			ctx.TenantID = sn.TenantID
		}
		if ctx.APIKeyID == 0 {
			ctx.APIKeyID = sn.APIKeyID
		}
	}
	if ki != nil {
		if ctx.TenantID == "" {
			ctx.TenantID = ki.TenantID
		}
		if ctx.APIKeyID == 0 {
			ctx.APIKeyID = ki.ID
		}
		if ki.ApplicationID != 0 {
			ctx.ApplicationID = strconv.Itoa(ki.ApplicationID)
		}
	}
	return ctx
}

// AuditContextFromAttempt returns a copy of `base` with per-attempt
// fields populated. The returned struct is safe for the caller to
// mutate (the per-attempt fields) without affecting base. The
// ChunkIndex atomic is NOT copied (zero-init in the new struct);
// callers must continue to use the base's counter or attach a fresh
// one. Returns nil when base is nil.
//
// We cannot do a shallow struct copy because AuditContext embeds
// sync/atomic.Int64 (vet: "assignment copies lock value"). Use
// field-by-field assignment instead.
func AuditContextFromAttempt(base *AuditContext, providerID, credentialID int, endpoint string, attemptNo int) *AuditContext {
	if base == nil {
		return nil
	}
	return &AuditContext{
		RequestID:        base.RequestID,
		ClientRequestID:  base.ClientRequestID,
		GWSessionID:      base.GWSessionID,
		GWTaskID:         base.GWTaskID,
		ParentRequestID:  base.ParentRequestID,
		TenantID:         base.TenantID,
		ApplicationID:    base.ApplicationID,
		APIKeyID:         base.APIKeyID,
		TraceID:          base.TraceID,
		SpanID:           base.SpanID,
		Protocol:         base.Protocol,
		ProviderID:       providerID,
		CredentialID:     credentialID,
		UpstreamEndpoint: endpoint,
		AttemptNo:        attemptNo,
		// ChunkIndex is intentionally not copied — see comment above.
	}
}

// RawCorrelationEnvelope returns a snapshot of the current fields in
// the wire-level DTO used by AsyncRawDataLogger. The returned value
// is a value copy safe to mutate.
func (c *AuditContext) RawCorrelationEnvelope() logging.RawCorrelationEnvelope {
	if c == nil {
		return logging.RawCorrelationEnvelope{}
	}
	return logging.RawCorrelationEnvelope{
		ClientRequestID:  c.ClientRequestID,
		GWSessionID:      c.GWSessionID,
		GWTaskID:         c.GWTaskID,
		ParentRequestID:  c.ParentRequestID,
		TenantID:         c.TenantID,
		ApplicationID:    c.ApplicationID,
		APIKeyID:         c.APIKeyID,
		ProviderID:       c.ProviderID,
		CredentialID:     c.CredentialID,
		AttemptNo:        c.AttemptNo,
		ChunkIndex:       int(c.ChunkIndex.Load()),
		UpstreamEndpoint: c.UpstreamEndpoint,
		TraceID:          c.TraceID,
		SpanID:           c.SpanID,
	}
}

// AnomalyReportEnvelope returns the wire-level envelope used by
// LockFreeAnomalyReporter (and any other anomaly transport).
func (c *AuditContext) AnomalyReportEnvelope() logging.AnomalyReportEnvelope {
	if c == nil {
		return logging.AnomalyReportEnvelope{}
	}
	return logging.AnomalyReportEnvelope{
		ClientRequestID: c.ClientRequestID,
		GWSessionID:     c.GWSessionID,
		ProviderID:      c.ProviderID,
		CredentialID:    c.CredentialID,
		TraceID:         c.TraceID,
	}
}

// GetRequestID returns the request_id or empty string for a nil
// receiver. Used by helpers that previously took requestID as a
// separate argument.
func (c *AuditContext) GetRequestID() string {
	if c == nil {
		return ""
	}
	return c.RequestID
}

// GetProtocol returns the protocol string carried on the context.
// Set explicitly by streaming bridges (default "openai-chat"
// matches the prior free-function behaviour).
func (c *AuditContext) GetProtocol() string {
	if c == nil || c.Protocol == "" {
		return "openai-chat"
	}
	return c.Protocol
}