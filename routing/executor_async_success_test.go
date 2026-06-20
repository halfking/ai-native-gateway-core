// 2026-06-20: tests for async-retry success writeback.
package routing

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/telemetry"
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
