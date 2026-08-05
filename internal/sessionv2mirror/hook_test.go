package sessionv2mirror

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

func gwSessionPtr(s string) *string  { return &s }
func intPtr(v int) *int              { return &v }
func strPtr(s string) *string        { return &s }
func floatPtr(v float64) *float64    { return &v }
func timePtr(t time.Time) *time.Time { return &t }

func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}

// ────────────────────── No-op guards ──────────────────────

func TestPersistHook_NilWriterIsNoOp(t *testing.T) {
	hook := PersistHook(nil)
	// Must not panic
	hook(nil)
	hook(&telemetry.RequestLogEntry{RequestID: "x", GwSessionID: gwSessionPtr("sess_1")})
}

func TestPersistHook_NilEntryIsNoOp(t *testing.T) {
	hook := PersistHook(&v2.SessionWriterV2{})
	// Must not panic (entry is nil → early return before settings check)
	hook(nil)
}

func TestPersistHook_NilGwSessionIDIsNoOp(t *testing.T) {
	hook := PersistHook(&v2.SessionWriterV2{})
	// Must not panic — nil GwSessionID → early return
	hook(&telemetry.RequestLogEntry{RequestID: "x"})
}

func TestPersistHook_EmptyGwSessionIDIsNoOp(t *testing.T) {
	hook := PersistHook(&v2.SessionWriterV2{})
	// Must not panic — empty GwSessionID → early return
	hook(&telemetry.RequestLogEntry{RequestID: "x", GwSessionID: gwSessionPtr("")})
}

func TestPersistHook_SkipsInProgressEntry(t *testing.T) {
	called := 0
	writer := captureWriterV2{fn: func(*v2.ProcessedRequest) { called++ }}
	hook := PersistHook(writer)
	withShadowFlags(t, func() {
		hook(&telemetry.RequestLogEntry{
			RequestID:     "req-in-progress",
			GwSessionID:   gwSessionPtr("sess-in-progress"),
			Success:       false,
			RequestStatus: strPtr(telemetry.RequestStatusInProgress),
		})
	})
	if called != 0 {
		t.Fatalf("in-progress entry reached V2 writer %d times, want 0", called)
	}
}

func TestPersistHook_MirrorsTerminalSuccess(t *testing.T) {
	var got *v2.ProcessedRequest
	writer := captureWriterV2{fn: func(req *v2.ProcessedRequest) { got = req }}
	hook := PersistHook(writer)
	prompt, completion := 123, 45
	hookEntry := &telemetry.RequestLogEntry{
		RequestID:          "req-terminal-success",
		GwSessionID:        gwSessionPtr("sess-terminal-success"),
		Success:            true,
		RequestStatus:      strPtr(telemetry.RequestStatusSuccess),
		PromptTokens:       &prompt,
		CompletionTokens:   &completion,
		UpstreamStatusCode: intPtr(200),
	}
	withShadowFlags(t, func() { hook(hookEntry) })
	if got == nil {
		t.Fatal("terminal success entry did not reach V2 writer")
	}
	if !got.Success || got.StatusCode != 200 {
		t.Fatalf("V2 result = success=%v status=%d, want true/200", got.Success, got.StatusCode)
	}
	if got.PromptTokens != prompt || got.CompletionTokens != completion {
		t.Fatalf("V2 tokens = %d/%d, want %d/%d", got.PromptTokens, got.CompletionTokens, prompt, completion)
	}
}

func TestPersistHook_MirrorsTerminalFailure(t *testing.T) {
	var got *v2.ProcessedRequest
	writer := captureWriterV2{fn: func(req *v2.ProcessedRequest) { got = req }}
	hook := PersistHook(writer)
	hookEntry := &telemetry.RequestLogEntry{
		RequestID:     "req-terminal-failure",
		GwSessionID:   gwSessionPtr("sess-terminal-failure"),
		Success:       false,
		RequestStatus: strPtr(telemetry.RequestStatusFailure),
		ErrorKind:     strPtr("rate_limit"),
	}
	withShadowFlags(t, func() { hook(hookEntry) })
	if got == nil {
		t.Fatal("terminal failure entry did not reach V2 writer")
	}
	if got.Success {
		t.Fatalf("V2 result success = true, want false for failure")
	}
	if got.ErrorKind != "rate_limit" {
		t.Fatalf("V2 error_kind = %q, want rate_limit", got.ErrorKind)
	}
}

func TestPersistHook_MirrorsTerminalRateLimited(t *testing.T) {
	var got *v2.ProcessedRequest
	writer := captureWriterV2{fn: func(req *v2.ProcessedRequest) { got = req }}
	hook := PersistHook(writer)
	hookEntry := &telemetry.RequestLogEntry{
		RequestID:     "req-rate-limited",
		GwSessionID:   gwSessionPtr("sess-rate-limited"),
		Success:       false,
		RequestStatus: strPtr(telemetry.RequestStatusRateLimited),
		ErrorKind:     strPtr("gw_rpm_exceeded"),
	}
	withShadowFlags(t, func() { hook(hookEntry) })
	if got == nil {
		t.Fatal("rate-limited entry did not reach V2 writer")
	}
	if got.Success {
		t.Fatalf("V2 result success = true, want false for rate_limited")
	}
	if got.ErrorKind != "gw_rpm_exceeded" {
		t.Fatalf("V2 error_kind = %q, want gw_rpm_exceeded", got.ErrorKind)
	}
}

type captureWriterV2 struct {
	fn func(*v2.ProcessedRequest)
}

func (w captureWriterV2) Write(_ context.Context, req *v2.ProcessedRequest) error {
	if w.fn != nil {
		w.fn(req)
	}
	return nil
}

func TestEntryToProcessedRequest_NilEntry(t *testing.T) {
	req := entryToProcessedRequest(nil)
	if req != nil {
		t.Fatal("expected nil for nil entry")
	}
}

func TestEntryToProcessedRequest_NilGwSessionID(t *testing.T) {
	req := entryToProcessedRequest(&telemetry.RequestLogEntry{RequestID: "r1"})
	if req != nil {
		t.Fatal("expected nil when GwSessionID is nil")
	}
}

func TestEntryToProcessedRequest_CoreFields(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

	entry := &telemetry.RequestLogEntry{
		RequestID:          "req_abc123",
		GwSessionID:        gwSessionPtr("sess_v2_test_001"),
		TenantID:           "tenant_demo",
		ClientModel:        strPtr("gpt-4o-mini"),
		ProviderID:         intPtr(36),
		CredentialID:       intPtr(42),
		Success:            true,
		EventAt:            timePtr(now),
		LatencyMs:          intPtr(1500),
		ErrorKind:          strPtr(""),
		UpstreamStatusCode: intPtr(200),
	}

	req := entryToProcessedRequest(entry)
	if req == nil {
		t.Fatal("entryToProcessedRequest returned nil")
	}

	check(t, "SessionID", req.SessionID, "sess_v2_test_001")
	check(t, "TenantID", req.TenantID, "tenant_demo")
	check(t, "RequestID", req.RequestID, "req_abc123")
	check(t, "ClientModel", req.ClientModel, "gpt-4o-mini")
	check(t, "ErrorKind", req.ErrorKind, "")
	check(t, "CredentialID", req.CredentialID, "42")
	check(t, "ProviderID", req.ProviderID, "36")
	check(t, "StatusCode", req.StatusCode, 200)
	if !req.Success {
		t.Error("expected Success=true")
	}
	if !req.StartedAt.Equal(now) {
		t.Errorf("StartedAt: got %v, want %v", req.StartedAt, now)
	}
	expectedCompleted := now.Add(1500 * time.Millisecond)
	if !req.CompletedAt.Equal(expectedCompleted) {
		t.Errorf("CompletedAt: got %v, want %v", req.CompletedAt, expectedCompleted)
	}
}

func TestEntryToProcessedRequest_UsageFields(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		RequestID:        "req_usage",
		GwSessionID:      gwSessionPtr("sess_usage"),
		PromptTokens:     intPtr(100),
		CompletionTokens: intPtr(50),
		CacheReadTokens:  intPtr(10),
		CacheWriteTokens: intPtr(5),
		CostUSD:          floatPtr(0.0025),
		Success:          true,
	}

	req := entryToProcessedRequest(entry)
	check(t, "PromptTokens", req.PromptTokens, 100)
	check(t, "CompletionTokens", req.CompletionTokens, 50)
	check(t, "CacheReadTokens", req.CacheReadTokens, 10)
	check(t, "CacheWriteTokens", req.CacheWriteTokens, 5)
	check(t, "CostUSD", req.CostUSD, 0.0025)
}

func TestEntryToProcessedRequest_CompressionFields(t *testing.T) {
	strategy := "auto_threshold"
	reason := "token_limit"

	entry := &telemetry.RequestLogEntry{
		RequestID:           "req_comp",
		GwSessionID:         gwSessionPtr("sess_comp"),
		CompressionStrategy: &strategy,
		CompressionReason:   &reason,
		Success:             true,
	}

	req := entryToProcessedRequest(entry)
	if !req.CompressionApplied {
		t.Error("expected CompressionApplied=true")
	}
	check(t, "CompressionStrategy", req.CompressionStrategy, "auto_threshold")
	if req.CompressionMeta == nil {
		t.Fatal("expected CompressionMeta non-nil")
	}
	if r, ok := req.CompressionMeta["reason"]; !ok || r != "token_limit" {
		t.Errorf("CompressionMeta[reason] = %v, want token_limit", r)
	}
}

func TestEntryToProcessedRequest_CompressionSkipped(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		RequestID:   "req_no_comp",
		GwSessionID: gwSessionPtr("sess_no_comp"),
		Success:     true,
	}
	req := entryToProcessedRequest(entry)
	if req.CompressionApplied {
		t.Error("expected CompressionApplied=false when no compression strategy")
	}
	if req.CompressionMeta != nil {
		t.Error("expected CompressionMeta=nil when no compression reason")
	}
}

func TestEntryToProcessedRequest_RequestBodyParsing(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi there"}]}`

	entry := &telemetry.RequestLogEntry{
		RequestID:   "req_body",
		GwSessionID: gwSessionPtr("sess_body"),
		RequestBody: &body,
		Success:     true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.RequestBody) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.RequestBody))
	}
	check(t, "RequestBody[0].Role", req.RequestBody[0].Role, "user")
	check(t, "RequestBody[0].Content", req.RequestBody[0].Content, "hello")
	check(t, "RequestBody[1].Role", req.RequestBody[1].Role, "assistant")
	check(t, "RequestBody[1].Content", req.RequestBody[1].Content, "hi there")
}

func TestEntryToProcessedRequest_ResponseBodyParsing(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"I am fine"}}]}`

	entry := &telemetry.RequestLogEntry{
		RequestID:    "req_resp",
		GwSessionID:  gwSessionPtr("sess_resp"),
		ResponseBody: &body,
		Success:      true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.ResponseBody) != 1 {
		t.Fatalf("expected 1 response message, got %d", len(req.ResponseBody))
	}
	check(t, "ResponseBody[0].Role", req.ResponseBody[0].Role, "assistant")
	if req.ResponseBody[0].Content != "I am fine" {
		t.Errorf("ResponseBody[0].Content = %q, want %q", req.ResponseBody[0].Content, "I am fine")
	}
}

func TestEntryToProcessedRequest_OutboundBodyParsing(t *testing.T) {
	outbound := jsonRaw(`[{"role":"user","content":"compressed hello"}]`)

	entry := &telemetry.RequestLogEntry{
		RequestID:        "req_out",
		GwSessionID:      gwSessionPtr("sess_out"),
		RequestBody:      strPtr(`{"messages":[{"role":"user","content":"original hello"}]}`),
		OutboundBody:     outbound,
		OutboundMsgCount: intPtr(2),
		Success:          true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.OutboundBody) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(req.OutboundBody))
	}
	check(t, "OutboundBody[0].Content", req.OutboundBody[0].Content, "compressed hello")
	if len(req.LastOutboundBody) != 0 {
		t.Fatalf("expected LastOutboundBody to be loaded by SessionWriterV2, got %d messages", len(req.LastOutboundBody))
	}
}

// TestEntryToProcessedRequest_OutboundBodyFullObject asserts the mirror
// correctly extracts messages from the FULL upstream request object that the
// session compressor produces (spliceBodyMessages returns the whole
// {"model":...,"messages":[...]} body, NOT a bare array). The 2026-08-05 fix:
// before this, parseMessagesJSON unmarshalled into a bare []msgProbe, which
// silently failed on an object and left OutboundBody empty → session_bodies.
// outbound_body persisted NULL for every session request.
func TestEntryToProcessedRequest_OutboundBodyFullObject(t *testing.T) {
	// This is what compression.SessionCompressor.OutboundBody actually is:
	// the whole request payload with model/tools/messages, not just messages.
	outbound := jsonRaw(`{
		"model":"glm-5.1",
		"tools":[{"type":"function","function":{"name":"noop"}}],
		"messages":[
			{"role":"system","content":"you are helpful"},
			{"role":"user","content":"compressed hello"}
		]
	}`)

	entry := &telemetry.RequestLogEntry{
		RequestID:           "req_out_obj",
		GwSessionID:         gwSessionPtr("sess_out_obj"),
		OutboundBody:        outbound,
		CompressionStrategy: strPtr("mechanical_trim"),
		Success:             true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.OutboundBody) != 2 {
		t.Fatalf("expected 2 outbound messages from full request object, got %d", len(req.OutboundBody))
	}
	check(t, "OutboundBody[0].Role", req.OutboundBody[0].Role, "system")
	check(t, "OutboundBody[1].Role", req.OutboundBody[1].Role, "user")
	check(t, "OutboundBody[1].Content", req.OutboundBody[1].Content, "compressed hello")
}

// TestEntryToProcessedRequest_OutboundBodyBareArray keeps the legacy bare-array
// shape working so the fix is a superset, not a replacement.
func TestEntryToProcessedRequest_OutboundBodyBareArray(t *testing.T) {
	outbound := jsonRaw(`[{"role":"user","content":"bare array hello"}]`)

	entry := &telemetry.RequestLogEntry{
		RequestID:    "req_out_bare",
		GwSessionID:  gwSessionPtr("sess_out_bare"),
		OutboundBody: outbound,
		Success:      true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.OutboundBody) != 1 {
		t.Fatalf("expected 1 outbound message from bare array, got %d", len(req.OutboundBody))
	}
	check(t, "OutboundBody[0].Content", req.OutboundBody[0].Content, "bare array hello")
}

// TestEntryToProcessedRequest_SubmitModeHeader asserts the X-Gw-Submit-Mode
// header carried on the telemetry entry is propagated into the V2
// ProcessedRequest so the SubmitModeDetector's P0 (explicit-header) path can
// produce an authoritative "delta" instead of an LCS-inferred verdict.
func TestEntryToProcessedRequest_SubmitModeHeader(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		RequestID:        "req_submit_mode",
		GwSessionID:      gwSessionPtr("sess_submit_mode"),
		SubmitModeHeader: strPtr("delta"),
		Success:          true,
	}

	req := entryToProcessedRequest(entry)
	check(t, "SubmitModeHeader", req.SubmitModeHeader, "delta")
}

// TestEntryToProcessedRequest_SubmitModeHeaderEmpty asserts a nil header leaves
// the field empty (detector falls through to inference), never panics.
func TestEntryToProcessedRequest_SubmitModeHeaderEmpty(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		RequestID:   "req_no_submit_mode",
		GwSessionID: gwSessionPtr("sess_no_submit_mode"),
		Success:     true,
	}

	req := entryToProcessedRequest(entry)
	check(t, "SubmitModeHeader", req.SubmitModeHeader, "")
}

func TestEntryToProcessedRequest_UsesEventTime(t *testing.T) {
	eventAt := time.Date(2026, 8, 3, 23, 59, 58, 0, time.UTC)
	entry := &telemetry.RequestLogEntry{
		RequestID:   "req_event_time",
		GwSessionID: gwSessionPtr("sess_event_time"),
		EventAt:     &eventAt,
		Success:     true,
	}

	req := entryToProcessedRequest(entry)
	if !req.Timestamp.Equal(eventAt) {
		t.Fatalf("Timestamp = %v, want event time %v", req.Timestamp, eventAt)
	}
	if !req.StartedAt.Equal(eventAt) || !req.CompletedAt.Equal(eventAt) {
		t.Fatalf("started/completed = %v/%v, want %v", req.StartedAt, req.CompletedAt, eventAt)
	}
}

func TestEntryToProcessedRequest_AttachmentsParsing(t *testing.T) {
	attachments := jsonRaw(`[
		{
			"name":"image.png",
			"object_key":"uploads/abc.png",
			"mime_type":"image/png",
			"size_bytes":1024,
			"sha256":"abc123",
			"source_protocol":"http/1.1",
			"declared_mime":"image/png",
			"sniffed_mime":"image/png",
			"provider_file_id":"file_001"
		}
	]`)

	entry := &telemetry.RequestLogEntry{
		RequestID:   "req_att",
		GwSessionID: gwSessionPtr("sess_att"),
		Attachments: attachments,
		Success:     true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(req.Attachments))
	}
	check(t, "Attachments[0].Name", req.Attachments[0].Name, "image.png")
	check(t, "Attachments[0].MIMEType", req.Attachments[0].MIMEType, "image/png")
	check(t, "Attachments[0].SizeBytes", req.Attachments[0].SizeBytes, int64(1024))
	if len(req.MultimodalTypes) != 1 || req.MultimodalTypes[0] != "image" {
		t.Errorf("expected MultimodalTypes=[\"image\"], got %v", req.MultimodalTypes)
	}
}

func TestEntryToProcessedRequest_ResponseBodyWithToolCalls(t *testing.T) {
	body := `{
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"beijing\"}"}}]
			}
		}]
	}`

	entry := &telemetry.RequestLogEntry{
		RequestID:    "req_tool",
		GwSessionID:  gwSessionPtr("sess_tool"),
		ResponseBody: &body,
		Success:      true,
	}

	req := entryToProcessedRequest(entry)
	if len(req.ResponseBody) != 1 {
		t.Fatalf("expected 1 response message, got %d", len(req.ResponseBody))
	}
	if req.ResponseBody[0].Content != "" {
		t.Errorf("expected empty content, got %q", req.ResponseBody[0].Content)
	}
	// The tool_calls field should be populated for the assistant message
	if len(req.ResponseBody[0].ToolCalls) == 0 {
		t.Error("expected ToolCalls to be populated")
	}
}

// ────────────────────── Helpers ──────────────────────

func check[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}
