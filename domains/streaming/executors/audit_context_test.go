package executors

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestAuditContextFromRequest_FullEnvelope covers the base-context
// builder (2026-07-28 §5.1). Headers take priority over session
// fields; session fields fill in the gaps; key info fills in the
// remaining tenant/application/api_key values.
func TestAuditContextFromRequest_FullEnvelope(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Request-Id", "server-rid-1")
	r.Header.Set("X-Gw-Client-Request-Id", "client-rid-1")
	r.Header.Set("X-Gw-Task-Id", "task-1")
	r.Header.Set("X-Trace-Id", "trace-1")
	r.Header.Set("X-Span-Id", "span-1")
	r.Header.Set("X-Parent-Request-Id", "parent-1")

	sn := &session.Session{SessionID: "sess-1", TaskID: "session-task", TenantID: "tenant-a", APIKeyID: 7}
	ki := &authentication.KeyInfo{ID: 7, TenantID: "tenant-a", ApplicationID: 11}

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	ctx := AuditContextFromRequest(r, sn, body, ki)

	if ctx.RequestID != "server-rid-1" {
		t.Errorf("request_id=%s", ctx.RequestID)
	}
	if ctx.ClientRequestID != "client-rid-1" {
		t.Errorf("client_request_id=%s", ctx.ClientRequestID)
	}
	if ctx.GWSessionID != "sess-1" {
		t.Errorf("gw_session_id=%s", ctx.GWSessionID)
	}
	if ctx.GWTaskID != "task-1" {
		t.Errorf("gw_task_id=%s (header takes priority over session.TaskID)", ctx.GWTaskID)
	}
	if ctx.ParentRequestID != "parent-1" {
		t.Errorf("parent_request_id=%s", ctx.ParentRequestID)
	}
	if ctx.TenantID != "tenant-a" {
		t.Errorf("tenant_id=%s", ctx.TenantID)
	}
	if ctx.ApplicationID != "11" {
		t.Errorf("application_id=%s", ctx.ApplicationID)
	}
	if ctx.APIKeyID != 7 {
		t.Errorf("api_key_id=%d", ctx.APIKeyID)
	}
	if ctx.TraceID != "trace-1" {
		t.Errorf("trace_id=%s", ctx.TraceID)
	}
	if ctx.SpanID != "span-1" {
		t.Errorf("span_id=%s", ctx.SpanID)
	}
}

// TestAuditContextFromRequest_SessionFallback covers the path where
// X-Gw-Task-Id is not in headers; session.TaskID must take over.
func TestAuditContextFromRequest_SessionFallback(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	sn := &session.Session{SessionID: "sess-fb", TaskID: "session-task-fb"}
	ctx := AuditContextFromRequest(r, sn, nil, nil)
	if ctx.GWSessionID != "sess-fb" {
		t.Errorf("gw_session_id=%s", ctx.GWSessionID)
	}
	if ctx.GWTaskID != "session-task-fb" {
		t.Errorf("gw_task_id=%s (session.TaskID must fill when header absent)", ctx.GWTaskID)
	}
}

func TestAuditContextFromRequest_SanitizesClientCorrelationIDs(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Trace-Id", "hydrate-trace")
	r.Header.Set("X-Span-Id", "invalid span")
	r.Header.Set("X-Parent-Request-Id", strings.Repeat("x", maxCorrelationIDLen+1))

	ctx := AuditContextFromRequest(r, nil, nil, nil)
	if ctx.TraceID != "hydrate-trace" {
		t.Fatalf("trace_id = %q, want hydrate-trace", ctx.TraceID)
	}
	if ctx.SpanID != "" {
		t.Fatalf("invalid span_id must be dropped, got %q", ctx.SpanID)
	}
	if ctx.ParentRequestID != "" {
		t.Fatalf("overlong parent_request_id must be dropped, got %q", ctx.ParentRequestID)
	}
}

// TestAuditContextFromParams_PerAttempt covers the per-attempt
// shim (2026-07-28 §5.1). Base fields must survive the copy; only
// per-attempt fields change.
func TestAuditContextFromAttempt(t *testing.T) {
	base := &AuditContext{RequestID: "req-1", TenantID: "t", APIKeyID: 9}
	ctx := AuditContextFromAttempt(base, 18, 42, "https://upstream/api", 2)
	if ctx.ProviderID != 18 || ctx.CredentialID != 42 {
		t.Errorf("provider/cred=%d/%d", ctx.ProviderID, ctx.CredentialID)
	}
	if ctx.UpstreamEndpoint != "https://upstream/api" {
		t.Errorf("endpoint=%s", ctx.UpstreamEndpoint)
	}
	if ctx.AttemptNo != 2 {
		t.Errorf("attempt_no=%d", ctx.AttemptNo)
	}
	if ctx.RequestID != "req-1" {
		t.Errorf("request_id=%s (must inherit)", ctx.RequestID)
	}
	if ctx.APIKeyID != 9 {
		t.Errorf("api_key_id=%d (must inherit)", ctx.APIKeyID)
	}

	// Mutating the per-attempt copy must not affect base.
	ctx.ProviderID = 99
	if base.ProviderID != 0 {
		t.Errorf("base mutated: provider_id=%d", base.ProviderID)
	}

	// Nil base is a no-op.
	if AuditContextFromAttempt(nil, 1, 2, "x", 3) != nil {
		t.Error("nil base must return nil")
	}
}

// TestAuditContext_RawAndAnomalyEnvelopes covers the snapshot
// helpers (2026-07-28 §5.1 / §5.7). Both envelopes must carry every
// field set on the AuditContext.
func TestAuditContext_RawAndAnomalyEnvelopes(t *testing.T) {
	ctx := &AuditContext{
		RequestID:        "r1",
		ClientRequestID:  "cr1",
		GWSessionID:      "s1",
		GWTaskID:         "task",
		TenantID:         "t",
		APIKeyID:         9,
		ProviderID:       18,
		CredentialID:     42,
		AttemptNo:        2,
		UpstreamEndpoint: "https://u",
		TraceID:          "trace",
		SpanID:           "span",
	}
	raw := ctx.RawCorrelationEnvelope()
	if raw.ClientRequestID != "cr1" || raw.ProviderID != 18 || raw.AttemptNo != 2 {
		t.Errorf("raw envelope mismatch: %+v", raw)
	}
	if raw.GWTaskID != "task" {
		t.Errorf("raw gw_task_id=%q (must NOT be model name)", raw.GWTaskID)
	}
	anom := ctx.AnomalyReportEnvelope()
	if anom.ClientRequestID != "cr1" || anom.ProviderID != 18 || anom.CredentialID != 42 || anom.GWSessionID != "s1" || anom.TraceID != "trace" {
		t.Errorf("anom envelope mismatch: %+v", anom)
	}
}

// TestAuditContext_NilReceiver covers the defensive guarantee for
// helpers that may hold an optional *AuditContext.
func TestAuditContext_NilReceiver(t *testing.T) {
	var c *AuditContext
	if raw := c.RawCorrelationEnvelope(); raw.GWSessionID != "" || raw.ClientRequestID != "" {
		t.Errorf("nil RawCorrelationEnvelope must be empty: %+v", raw)
	}
	if anom := c.AnomalyReportEnvelope(); anom.GWSessionID != "" || anom.ClientRequestID != "" {
		t.Errorf("nil AnomalyReportEnvelope must be empty: %+v", anom)
	}
	if id := c.GetRequestID(); id != "" {
		t.Errorf("nil GetRequestID=%q", id)
	}
	if proto := c.GetProtocol(); proto != "openai-chat" {
		t.Errorf("nil GetProtocol=%q (must default to openai-chat)", proto)
	}
}

// TestAuditContext_ChunkIndexAtomic verifies the streaming chunk
// counter increments without races.
func TestExecuteOpenAI_DoesNotForwardInboundTraceHeaders(t *testing.T) {
	var upstreamHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"chatcmpl-1","model":"gpt-test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()), credential.NewManager(), credential.NewLimiter(),
		pool.NewPoolManager(nil), nil, func(chunk []byte, _ bool) []byte { return chunk }, nil, nil,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Request-Id", "server-request-1")
	req.Header.Set("X-Trace-Id", "untrusted-trace")
	req.Header.Set("X-Span-Id", "untrusted-span")
	cand := provider.Candidate{
		ProviderID: 1, CredentialID: 1, BaseURL: upstream.URL,
		Protocol: "openai-completions", RawModel: "gpt-test", APIKey: "key",
	}

	_, err := exec.executeOpenAI(&ExecParams{
		W: httptest.NewRecorder(), R: req, RequestID: "server-request-1",
		BodyBytes:      []byte(`{"model":"gpt-test","messages":[]}`),
		ClientProtocol: "openai-completions", ClientModel: "gpt-test",
		TraceID: "untrusted-trace", SpanID: "untrusted-span",
		Audit:    &AuditContext{TraceID: "untrusted-trace", SpanID: "untrusted-span"},
		ClientID: identity.ClientIdentity{IdentityHash: "test"},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if got := upstreamHeaders.Get("X-Request-Id"); got != "server-request-1" {
		t.Fatalf("provider request ID = %q", got)
	}
	if got := upstreamHeaders.Get("X-Trace-Id"); got != "" {
		t.Fatalf("untrusted X-Trace-Id forwarded to provider: %q", got)
	}
	if got := upstreamHeaders.Get("X-Span-Id"); got != "" {
		t.Fatalf("untrusted X-Span-Id forwarded to provider: %q", got)
	}
}

func TestAuditContext_ChunkIndexAtomic(t *testing.T) {
	ctx := &AuditContext{}
	ctx.ChunkIndex.Add(1)
	ctx.ChunkIndex.Add(1)
	ctx.ChunkIndex.Add(1)
	if got := int(ctx.ChunkIndex.Load()); got != 3 {
		t.Errorf("chunk_index=%d want 3", got)
	}
	raw := ctx.RawCorrelationEnvelope()
	if raw.ChunkIndex != 3 {
		t.Errorf("raw envelope chunk_index=%d want 3", raw.ChunkIndex)
	}
}
