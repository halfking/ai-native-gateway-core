// 2026-06-20: tests for async-retry success writeback.
package executors

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"                      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/provider"
)

type mockRequestLogEmitter struct {
	mu      sync.Mutex
	enabled bool
	entries []*telemetry.RequestLogEntry
}

func (m *mockRequestLogEmitter) Enabled() bool { return m.enabled }

func (m *mockRequestLogEmitter) EmitRequestLogUpdate(e *telemetry.RequestLogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

var _ RequestLogEmitter = (*telemetry.Client)(nil)

func TestBuildAsyncSuccessEntry_HappyPath(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-2 * time.Second)
	result := &ExecuteResult{
		Candidate: provider.Candidate{
			CredentialID: 42,
			ProviderID:   7,
			Protocol:     "openai-completions",
		},
		ResponseBody: []byte(`{"choices":[{"message":{"content":"hi"}}]}`),
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{
		R:             req,
		ClientModel:   "gpt-4",
		OutboundModel: "gpt-4-turbo",
		ClientID:      identity.ClientIdentity{IdentityHash: "abc123"},
	}

	entry := e.buildAsyncSuccessEntry("req-123", "sess-456", startedAt, result, params)

	if entry.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want req-123", entry.RequestID)
	}
	if !entry.Success {
		t.Error("Success should be true")
	}
	if entry.RequestStatus == nil || *entry.RequestStatus != "success" {
		t.Errorf("RequestStatus = %v, want success", entry.RequestStatus)
	}
	if entry.ErrorKind == nil {
		t.Error("ErrorKind should be non-nil empty string")
	} else if *entry.ErrorKind != "" {
		t.Errorf("ErrorKind = %q, want empty string", *entry.ErrorKind)
	}
	if entry.LatencyMs == nil || *entry.LatencyMs < 2000 {
		t.Errorf("LatencyMs = %v, want >= 2000", entry.LatencyMs)
	}
	if entry.CredentialID == nil || *entry.CredentialID != 42 {
		t.Errorf("CredentialID = %v, want 42", entry.CredentialID)
	}
	if entry.ProviderID == nil || *entry.ProviderID != 7 {
		t.Errorf("ProviderID = %v, want 7", entry.ProviderID)
	}
	if entry.EgressProtocol == nil || *entry.EgressProtocol != "openai-completions" {
		t.Errorf("EgressProtocol = %v, want openai-completions", entry.EgressProtocol)
	}
	if entry.ResponsePreview == nil || *entry.ResponsePreview == "" {
		t.Error("ResponsePreview should be populated")
	}
}

func TestBuildAsyncSuccessEntry_LongBodyTruncated(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	longBody := make([]byte, 500)
	for i := range longBody {
		longBody[i] = 'x'
	}
	result := &ExecuteResult{
		Candidate:    provider.Candidate{CredentialID: 1, ProviderID: 2},
		ResponseBody: longBody,
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{R: req}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, result, params)
	if entry.ResponsePreview == nil {
		t.Fatal("ResponsePreview should be populated")
	}
	if len(*entry.ResponsePreview) != 203 {
		t.Errorf("ResponsePreview length = %d, want 203", len(*entry.ResponsePreview))
	}
}

func TestBuildAsyncSuccessEntry_NilResultTolerated(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{R: req, ClientModel: "gpt-4"}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, nil, params)
	if entry.CredentialID != nil {
		t.Error("CredentialID should be nil when result is nil")
	}
	if !entry.Success {
		t.Error("Success should still be true with nil result")
	}
}

func TestBuildAsyncSuccessEntry_ZeroIDSkipped(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	result := &ExecuteResult{
		Candidate: provider.Candidate{Protocol: "openai-completions"},
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{R: req}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, result, params)
	if entry.CredentialID != nil {
		t.Errorf("CredentialID = %v, want nil (0 should be skipped)", entry.CredentialID)
	}
	if entry.ProviderID != nil {
		t.Errorf("ProviderID = %v, want nil", entry.ProviderID)
	}
}

func TestBuildAsyncSuccessEntry_StreamingPathLeavesResponsePreviewNil(t *testing.T) {
	// For streaming responses, Execute() does NOT set ResponseBody
	// (the body is consumed by the StreamChat capturer). The
	// success writeback should still work — just without a preview.
	e := &Executor{}
	startedAt := time.Now().Add(-3 * time.Second)
	result := &ExecuteResult{
		Candidate: provider.Candidate{
			CredentialID: 99,
			ProviderID:   5,
			Protocol:     "openai-completions",
		},
		ResponseBody: nil,
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{
		R:             req,
		ClientModel:   "gpt-4",
		OutboundModel: "gpt-4-turbo",
		IsStream:      true,
	}

	entry := e.buildAsyncSuccessEntry("req-stream-1", "sess-stream-1", startedAt, result, params)

	if !entry.Success {
		t.Error("Success should be true even for streaming")
	}
	if entry.ResponsePreview != nil {
		t.Errorf("ResponsePreview = %v, want nil for streaming (body consumed by capturer)", entry.ResponsePreview)
	}
	if entry.CredentialID == nil || *entry.CredentialID != 99 {
		t.Errorf("CredentialID should be 99 even for streaming, got %v", entry.CredentialID)
	}
}

func TestBuildAsyncSuccessEntry_NilParamsMinimalEntry(t *testing.T) {
	// Defensive: even with nil params, the entry should be valid.
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	result := &ExecuteResult{
		Candidate: provider.Candidate{CredentialID: 1, ProviderID: 2},
	}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, result, nil)

	if entry.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", entry.RequestID)
	}
	if !entry.Success {
		t.Error("Success should be true even with nil params")
	}
	if entry.ClientModel != nil {
		t.Errorf("ClientModel = %v, want nil for nil params", entry.ClientModel)
	}
}

func TestRequestLogEmitter_DisabledShortCircuit(t *testing.T) {
	mock := &mockRequestLogEmitter{enabled: false}
	if mock.Enabled() {
		t.Fatal("Enabled() should return false")
	}
}

// PR-5 (2026-06-30): async-retry success path must populate
// client_request_id from X-Gw-Client-Request-Id header. Without this,
// retry storms cannot be correlated (audit P0-8).
func TestBuildAsyncSuccessEntry_PropagatesClientRequestID(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	result := &ExecuteResult{
		Candidate: provider.Candidate{CredentialID: 1, ProviderID: 2},
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Client-Request-Id", "client-req-abc-123")
	params := &ExecParams{
		R:             req,
		ClientModel:   "gpt-4",
		OutboundModel: "gpt-4-turbo",
	}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, result, params)

	if entry.ClientRequestID == nil {
		t.Fatal("ClientRequestID should be populated from X-Gw-Client-Request-Id header")
	}
	if *entry.ClientRequestID != "client-req-abc-123" {
		t.Errorf("ClientRequestID = %q, want %q", *entry.ClientRequestID, "client-req-abc-123")
	}
}

// PR-5: fallback to X-Client-Request-Id header for back-compat.
func TestBuildAsyncSuccessEntry_FallsBackToXClientRequestID(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	result := &ExecuteResult{Candidate: provider.Candidate{CredentialID: 1}}

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Client-Request-Id", "legacy-client-req-456")
	params := &ExecParams{R: req, ClientModel: "gpt-4"}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, result, params)

	if entry.ClientRequestID == nil {
		t.Fatal("ClientRequestID should fall back to X-Client-Request-Id")
	}
	if *entry.ClientRequestID != "legacy-client-req-456" {
		t.Errorf("ClientRequestID = %q, want legacy-client-req-456", *entry.ClientRequestID)
	}
}

// PR-5: nil ClientRequestID when no header is present (regression-safe).
func TestBuildAsyncSuccessEntry_NilWhenNoHeader(t *testing.T) {
	e := &Executor{}
	startedAt := time.Now().Add(-1 * time.Second)
	result := &ExecuteResult{Candidate: provider.Candidate{CredentialID: 1}}

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	params := &ExecParams{R: req, ClientModel: "gpt-4"}

	entry := e.buildAsyncSuccessEntry("req-1", "sess-1", startedAt, result, params)

	if entry.ClientRequestID != nil {
		t.Errorf("ClientRequestID should be nil when no header, got %q", *entry.ClientRequestID)
	}
}
