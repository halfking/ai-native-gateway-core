package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestBuildClientDisconnectProbeEntry_Cancel covers 问题4: when the client
// cancels (context.Canceled) the handler must synthesize a "probe-" prefixed
// RequestLogEntry carrying the selected credential_id, so it lands in
// request_logs_hot and the live-stream swim lane under the right provider.
//
// 2026-07-24: 验证修复后的版本记录了完整的请求信息（request_body, request_preview）。
func TestBuildClientDisconnectProbeEntry_Cancel(t *testing.T) {
	credID := 11
	provID := 18
	apiKeyID := 42
	requestBody := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"Hello"}],"temperature":0.7,"max_tokens":100}`)

	logCtx := &RequestLogContext{
		ClientModel:   "glm-5.2",
		OutboundModel: "glm-5.2",
		CredentialID:  &credID,
		ProviderID:    &provID,
		KeyInfo:       &authentication.KeyInfo{TenantID: "tenant-1", ID: apiKeyID},
		Body:          requestBody,
		StartTime:     time.Now().Add(-100 * time.Millisecond),
		EndUser:       "user@example.com",
	}

	// Build a request whose context is already canceled (client went away).
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(ctx)
	cancel()

	entry, ok := buildClientDisconnectProbeEntry("req-abc", r, logCtx)
	if !ok {
		t.Fatal("expected probe entry to be built on a canceled context")
	}
	if !strings.HasPrefix(entry.RequestID, "probe-client_cancel-cred11-") {
		t.Errorf("RequestID=%q must be probe-client_cancel-cred11-<ts>", entry.RequestID)
	}
	if entry.ErrorKind == nil || *entry.ErrorKind != "client_cancel" {
		t.Errorf("ErrorKind must be client_cancel, got %v", entry.ErrorKind)
	}
	if entry.FailureStage == nil || *entry.FailureStage != "probe" {
		t.Errorf("FailureStage must be probe, got %v", entry.FailureStage)
	}
	if entry.RequestStatus == nil || *entry.RequestStatus != telemetry.RequestStatusFailure {
		t.Errorf("RequestStatus must be failure, got %v", entry.RequestStatus)
	}
	if entry.CredentialID == nil || *entry.CredentialID != credID {
		t.Errorf("CredentialID must be carried for provider reverse-lookup, got %v", entry.CredentialID)
	}
	if entry.TenantID != "tenant-1" {
		t.Errorf("TenantID must propagate, got %q", entry.TenantID)
	}
	if entry.ClientRequestID == nil || *entry.ClientRequestID != "req-abc" {
		t.Errorf("ClientRequestID must link back to original request_id, got %v", entry.ClientRequestID)
	}
	// 2026-07-24: 验证新增字段 - 请求体和预览信息
	if entry.RequestBody == nil || !strings.Contains(*entry.RequestBody, "glm-5.2") {
		t.Errorf("RequestBody must be recorded, got %v", entry.RequestBody)
	}
	if entry.RequestPreview == nil || !strings.Contains(*entry.RequestPreview, "temperature") {
		t.Errorf("RequestPreview must contain model params, got %v", entry.RequestPreview)
	}
	if entry.APIKeyID == nil || *entry.APIKeyID != apiKeyID {
		t.Errorf("APIKeyID must be recorded, got %v", entry.APIKeyID)
	}
	if entry.EndUserID == nil || *entry.EndUserID != "user@example.com" {
		t.Errorf("EndUserID must be recorded, got %v", entry.EndUserID)
	}
	if entry.LatencyMs == nil || *entry.LatencyMs < 50 {
		t.Errorf("LatencyMs must be recorded and >= 50ms, got %v", entry.LatencyMs)
	}
}

// TestBuildClientDisconnectProbeEntry_Timeout verifies the deadline-exceeded
// path produces error_kind="probe_timeout".
func TestBuildClientDisconnectProbeEntry_Timeout(t *testing.T) {
	logCtx := &RequestLogContext{ClientModel: "glm-5.2"}

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx, cancel := context.WithTimeout(r.Context(), 0) // already expired
	r = r.WithContext(ctx)
	defer cancel()
	// Force the deadline-exceeded error to surface.
	<-ctx.Done()

	entry, ok := buildClientDisconnectProbeEntry("req-xyz", r, logCtx)
	if !ok {
		t.Fatal("expected probe entry to be built on a timed-out context")
	}
	if entry.ErrorKind == nil || *entry.ErrorKind != "probe_timeout" {
		t.Errorf("ErrorKind must be probe_timeout for DeadlineExceeded, got %v", entry.ErrorKind)
	}
	if !strings.HasPrefix(entry.RequestID, "probe-probe_timeout-nocred-") {
		t.Errorf("RequestID=%q must reflect no-credential + timeout", entry.RequestID)
	}
	// credential_id nil when none selected — still records the client-side event.
	if entry.CredentialID != nil {
		t.Errorf("CredentialID should be nil when none selected, got %d", *entry.CredentialID)
	}
}

// TestBuildClientDisconnectProbeEntry_FirstByteTimeout verifies that when
// StreamCapture records first_byte_timeout (upstream timeout), even if the
// context error is Canceled (not DeadlineExceeded), the probe is still
// classified as probe_timeout (supplier timeout) rather than client_cancel.
//
// 2026-08-09: 修复问题 —— 上游首字节超时导致的客户端取消应归类为供应商超时，
// 触发凭据降级，而非客户端主动取消（不应降级）。
func TestBuildClientDisconnectProbeEntry_FirstByteTimeout(t *testing.T) {
	// 模拟场景：上游首字节超时 → StreamCapture 记录 first_byte_timeout →
	// 但父 context 取消导致 r.Context().Err() 返回 context.Canceled。
	credID := 19
	provID := 9271
	logCtx := &RequestLogContext{
		ClientModel:  "gpt-5.6-luna",
		CredentialID: &credID,
		ProviderID:   &provID,
	}

	// 创建一个 StreamCapture 并标记为 first_byte_timeout 中断
	capture := &audit.StreamCapture{}
	capture.MarkInterruptedWithReason("first_byte_timeout")
	logCtx.StreamCapture = capture

	// 构造一个已取消的 context（模拟父 context 取消）
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx, cancel := context.WithCancel(r.Context())
	cancel() // 立即取消 → Err() 返回 context.Canceled
	r = r.WithContext(ctx)

	entry, ok := buildClientDisconnectProbeEntry("req-abc", r, logCtx)
	if !ok {
		t.Fatal("expected probe entry to be built on a canceled context with first_byte_timeout")
	}

	// 核心断言：即使 context.Err() 是 Canceled，因为 StreamCapture 记录了
	// first_byte_timeout，error_kind 必须是 probe_timeout（供应商超时）
	if entry.ErrorKind == nil || *entry.ErrorKind != "probe_timeout" {
		t.Errorf("ErrorKind must be probe_timeout for first_byte_timeout, got %v", entry.ErrorKind)
	}

	// RequestID 前缀应为 probe-probe_timeout-cred19-...
	if !strings.HasPrefix(entry.RequestID, "probe-probe_timeout-cred19-") {
		t.Errorf("RequestID=%q must be probe-probe_timeout-cred19-<ts>", entry.RequestID)
	}

	// 供应商凭据信息应被记录（用于降级）
	if entry.CredentialID == nil || *entry.CredentialID != credID {
		t.Errorf("CredentialID must be %d, got %v", credID, entry.CredentialID)
	}
	if entry.ProviderID == nil || *entry.ProviderID != provID {
		t.Errorf("ProviderID must be %d, got %v", provID, entry.ProviderID)
	}
}

// TestBuildClientDisconnectProbeEntry_NonTimeoutReason: when the capture
// records a client-side interruption reason (e.g. client_disconnected), the
// probe must stay "client_cancel" even on a canceled context — only supplier
// timeout reasons are reclassified as probe_timeout.
//
// 2026-08-09 审计: 边界保证 —— StreamCapture reason 非超时类时不误伤。
func TestBuildClientDisconnectProbeEntry_NonTimeoutReason(t *testing.T) {
	credID := 19
	logCtx := &RequestLogContext{
		ClientModel:  "gpt-5.6-luna",
		CredentialID: &credID,
	}

	capture := &audit.StreamCapture{}
	capture.MarkInterruptedWithReason("client_disconnected")
	logCtx.StreamCapture = capture

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)

	entry, ok := buildClientDisconnectProbeEntry("req-def", r, logCtx)
	if !ok {
		t.Fatal("expected probe entry to be built on a canceled context")
	}
	// 客户端主动断连（非超时）→ 必须保持 client_cancel
	if entry.ErrorKind == nil || *entry.ErrorKind != "client_cancel" {
		t.Errorf("ErrorKind must stay client_cancel for client_disconnected, got %v", entry.ErrorKind)
	}
}

// TestBuildClientDisconnectProbeEntry_NoErrorReturnsFalse: when the context
// is still alive there is nothing to probe.
func TestBuildClientDisconnectProbeEntry_NoErrorReturnsFalse(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	if _, ok := buildClientDisconnectProbeEntry("req-1", r, &RequestLogContext{}); ok {
		t.Fatal("must not build a probe entry when context has no error")
	}
}

// TestBuildClientDisconnectProbeEntry_NilRequest: defensive — a nil request
// must not panic.
func TestBuildClientDisconnectProbeEntry_NilRequest(t *testing.T) {
	if _, ok := buildClientDisconnectProbeEntry("req-1", nil, &RequestLogContext{}); ok {
		t.Fatal("must not build a probe entry for a nil request")
	}
}

// TestBuildClientDisconnectProbeEntry_LargeBody covers 2026-07-25 fix:
// request body logging was raised from 64KB → 512KB → 2MB to support
// modern LLM context windows (Claude 200K, Gemini 2M, GPT-4 128K).
// This test asserts the 2MB threshold holds (body within limit is recorded
// verbatim) and that oversize bodies are truncated with the new marker
// that includes the original byte count so analysts can tell how much
// was cut.
func TestBuildClientDisconnectProbeEntry_LargeBody(t *testing.T) {
	credID := 11
	provID := 18

	// ── Case A: 1.5 MB body (under 2 MB limit) ── preserved verbatim.
	// 1.5MB 覆盖 Claude 200K + GPT-4 128K 的典型请求体大小。
	bodyUnder := make([]byte, 0, 1536*1024)
	bodyUnder = append(bodyUnder, []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"`)...)
	for i := 0; i < 1536*1024-100; i++ {
		bodyUnder = append(bodyUnder, 'a')
	}
	bodyUnder = append(bodyUnder, []byte(`"}],"temperature":0.7}`)...)

	logCtxA := &RequestLogContext{
		ClientModel:   "glm-5.2",
		OutboundModel: "glm-5.2",
		CredentialID:  &credID,
		ProviderID:    &provID,
		KeyInfo:       &authentication.KeyInfo{TenantID: "tenant-1", ID: 42},
		Body:          bodyUnder,
		StartTime:     time.Now(),
	}

	rA := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctxA, cancelA := context.WithCancel(rA.Context())
	rA = rA.WithContext(ctxA)
	cancelA()

	entryA, ok := buildClientDisconnectProbeEntry("req-large-A", rA, logCtxA)
	if !ok {
		t.Fatal("expected probe entry to be built on canceled context (under-limit body)")
	}
	if entryA.RequestBody == nil {
		t.Fatal("RequestBody must be recorded for under-limit body")
	}
	if strings.Contains(*entryA.RequestBody, "...[truncated") {
		t.Errorf("1.5MB body must NOT be truncated, got len=%d", len(*entryA.RequestBody))
	}
	if len(*entryA.RequestBody) != len(bodyUnder) {
		t.Errorf("1.5MB body length must match original: got %d want %d",
			len(*entryA.RequestBody), len(bodyUnder))
	}

	// ── Case B: 3 MB body (over 2 MB limit) ── truncated with original byte marker.
	// 3MB 模拟 Gemini 1.5 Pro 2M token 上下文的极端请求。
	bodyOver := make([]byte, 0, 3*1024*1024)
	bodyOver = append(bodyOver, []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"`)...)
	for i := 0; i < 3*1024*1024-100; i++ {
		bodyOver = append(bodyOver, 'b')
	}
	bodyOver = append(bodyOver, []byte(`"}]}`)...)

	logCtxB := &RequestLogContext{
		ClientModel:   "glm-5.2",
		OutboundModel: "glm-5.2",
		CredentialID:  &credID,
		ProviderID:    &provID,
		KeyInfo:       &authentication.KeyInfo{TenantID: "tenant-1", ID: 42},
		Body:          bodyOver,
		StartTime:     time.Now(),
	}

	rB := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctxB, cancelB := context.WithCancel(rB.Context())
	rB = rB.WithContext(ctxB)
	cancelB()

	entryB, ok := buildClientDisconnectProbeEntry("req-large-B", rB, logCtxB)
	if !ok {
		t.Fatal("expected probe entry to be built on canceled context (over-limit body)")
	}
	if entryB.RequestBody == nil {
		t.Fatal("RequestBody must be recorded for over-limit body")
	}
	if !strings.Contains(*entryB.RequestBody, "...[truncated,original=") {
		t.Errorf("over-limit body must carry truncation marker, got prefix: %q",
			(*entryB.RequestBody)[:min(80, len(*entryB.RequestBody))])
	}
	// 标记里应该包含原始字节数（实际由前面拼装决定，使用 len(bodyOver) 精确比较）
	expectedOriginal := len(bodyOver)
	markerFragment := "original=" + strconv.Itoa(expectedOriginal) + "bytes"
	if !strings.Contains(*entryB.RequestBody, markerFragment) {
		t.Errorf("truncation marker must contain original byte count %q, got: %q",
			markerFragment,
			(*entryB.RequestBody)[len(*entryB.RequestBody)-80:])
	}
	// 截断后主体不超过 2MB + 标记长度
	maxAllowed := 2*1024*1024 + 80
	if len(*entryB.RequestBody) > maxAllowed {
		t.Errorf("truncated body must be within 2MB+marker, got len=%d", len(*entryB.RequestBody))
	}
}

// min is a tiny helper for the truncation marker assertions above.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure errors.Is is wired (guards against future import removals).
var _ = errors.Is
