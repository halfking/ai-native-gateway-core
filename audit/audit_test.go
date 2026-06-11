package audit

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventBuilder_Basic(t *testing.T) {
	e := NewEvent().
		ClientModel("gpt-4o").
		OutboundModel("gpt-4o-2024-08-06").
		Provider(1).
		Credential(5).
		Success(true).
		Latency(150 * time.Millisecond).
		Tokens(100, 50).
		Build()

	if e.ClientModel != "gpt-4o" {
		t.Fatalf("expected gpt-4o, got %s", e.ClientModel)
	}
	if !e.Success {
		t.Fatal("expected success")
	}
	if e.LatencyMs != 150 {
		t.Fatalf("expected 150ms, got %d", e.LatencyMs)
	}
	if e.PromptTokens != 100 || e.CompletionToken != 50 {
		t.Fatalf("expected 100/50, got %d/%d", e.PromptTokens, e.CompletionToken)
	}
	if e.RequestID == "" {
		t.Fatal("expected non-empty request_id")
	}
}

func TestEventBuilder_Failure(t *testing.T) {
	e := NewEvent().
		ClientModel("test").
		Success(false).
		ErrorKind("upstream_down").
		FailureStage("upstream_call").
		Build()

	if e.Success {
		t.Fatal("expected failure")
	}
	if e.ErrorKind != "upstream_down" {
		t.Fatalf("expected upstream_down, got %s", e.ErrorKind)
	}
	if e.FailureStage != "upstream_call" {
		t.Fatalf("expected upstream_call, got %s", e.FailureStage)
	}
}

func TestStreamCapture(t *testing.T) {
	sc := NewStreamCapture()
	sc.RecordChunk([]byte("hello"))
	sc.RecordChunk([]byte("world"))
	sc.RecordDone()

	count, ttfb, done, interrupted, checksum := sc.Snapshot()
	if count != 2 {
		t.Fatalf("expected 2 chunks, got %d", count)
	}
	if ttfb < 0 {
		t.Fatal("expected non-negative ttfb")
	}
	if !done {
		t.Fatal("expected done")
	}
	if interrupted {
		t.Fatal("expected not interrupted")
	}
	if len(checksum) != 64 {
		t.Fatalf("expected 64-char checksum, got %d", len(checksum))
	}
}

func TestStreamCapture_Interrupted(t *testing.T) {
	sc := NewStreamCapture()
	sc.RecordChunk([]byte("data"))
	sc.MarkInterrupted()

	_, _, _, interrupted, _ := sc.Snapshot()
	if !interrupted {
		t.Fatal("expected interrupted")
	}
}

func TestEventBuilder_StreamMetrics(t *testing.T) {
	sc := NewStreamCapture()
	sc.RecordChunk([]byte("chunk1"))
	sc.RecordChunk([]byte("chunk2"))
	sc.RecordDone()

	e := NewEvent().
		ClientModel("test").
		Stream(true).
		StreamMetrics(sc).
		Build()

	if e.StreamChunkCount != 2 {
		t.Fatalf("expected 2 chunks, got %d", e.StreamChunkCount)
	}
	if !e.StreamDone {
		t.Fatal("expected stream_done")
	}
}

func TestEventBuilder_StreamMetricsNil(t *testing.T) {
	e := NewEvent().StreamMetrics(nil).Build()
	if e.StreamChunkCount != 0 {
		t.Fatal("expected 0 chunks for nil capture")
	}
}

func TestStreamCapture_ObservePayload(t *testing.T) {
	sc := NewStreamCapture()
	sc.ObservePayload(`{"delta":"hel"}`, "", false)
	sc.ObservePayload(`{"delta":"lo"}`, "stop", false)
	sc.ObservePayload("[DONE]", "", true)

	m := sc.SummaryAsMap()
	if m["stream_chunk_count"].(int) != 2 {
		t.Errorf("expected 2 chunks, got %v", m["stream_chunk_count"])
	}
	if m["stream_done_received"].(bool) != true {
		t.Error("expected done_received=true")
	}
	if m["stream_interrupted"].(bool) != false {
		t.Error("expected interrupted=false")
	}
	if m["failure_detail_code"].(string) != "stop" {
		t.Errorf("expected finish_reason=stop, got %v", m["failure_detail_code"])
	}
	if _, ok := m["stream_first_chunk_ms"]; !ok {
		t.Error("expected first_chunk_ms to be set")
	}
	if _, ok := m["response_checksum"]; !ok {
		t.Error("expected checksum to be set")
	}
}

func TestStreamCapture_ObserveUsage(t *testing.T) {
	sc := NewStreamCapture()
	pt := 100
	ct := 50
	cr := 80
	cw := 20
	sc.ObserveUsage(&pt, &ct, &cr, &cw)

	m := sc.SummaryAsMap()
	if m["prompt_tokens"].(int) != 100 {
		t.Errorf("expected prompt_tokens=100, got %v", m["prompt_tokens"])
	}
	if m["completion_tokens"].(int) != 50 {
		t.Errorf("expected completion_tokens=50, got %v", m["completion_tokens"])
	}
	if m["cache_read_tokens"].(int) != 80 {
		t.Errorf("expected cache_read_tokens=80, got %v", m["cache_read_tokens"])
	}
	if m["cache_write_tokens"].(int) != 20 {
		t.Errorf("expected cache_write_tokens=20, got %v", m["cache_write_tokens"])
	}
}

func TestStreamCapture_MarkInterruptedWithReason(t *testing.T) {
	sc := NewStreamCapture()
	sc.ObservePayload("data", "", false)
	sc.MarkInterruptedWithReason("stream_timeout")

	m := sc.SummaryAsMap()
	if m["stream_interrupted"].(bool) != true {
		t.Error("expected interrupted=true")
	}
	if m["failure_detail_code"].(string) != "stream_timeout" {
		t.Errorf("expected reason=stream_timeout, got %v", m["failure_detail_code"])
	}
}

func TestStreamCapture_PreviewTruncation(t *testing.T) {
	sc := NewStreamCapture()
	longPayload := ""
	for i := 0; i < 300; i++ {
		longPayload += "x"
	}
	for i := 0; i < 10; i++ {
		sc.ObservePayload(longPayload, "", false)
	}
	m := sc.SummaryAsMap()
	preview := m["response_preview"].(string)
	if len(preview) > 2048 {
		t.Errorf("preview should be capped at 2048, got %d", len(preview))
	}
}

func TestLogSink(t *testing.T) {
	sink := &LogSink{}
	e := NewEvent().ClientModel("test").Success(true).Build()
	sink.Emit(context.Background(), e)
}

func TestJSONSink(t *testing.T) {
	sink := NewJSONSink(5)

	for i := 0; i < 10; i++ {
		sink.Emit(context.Background(), NewEvent().ClientModel("test").Build())
	}

	if sink.Count() != 5 {
		t.Fatalf("expected 5 (capped), got %d", sink.Count())
	}

	recent := sink.Recent(3)
	if len(recent) != 3 {
		t.Fatalf("expected 3 recent, got %d", len(recent))
	}

	data := sink.RecentJSON(2)
	if !strings.HasPrefix(string(data), "[") {
		t.Fatalf("expected JSON array, got %s", string(data[:20]))
	}

	var parsed []Event
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed events, got %d", len(parsed))
	}
}

func TestMultiSink(t *testing.T) {
	s1 := NewJSONSink(100)
	s2 := NewJSONSink(100)
	multi := NewMultiSink(s1, s2)

	e := NewEvent().ClientModel("multi-test").Success(true).Build()
	multi.Emit(context.Background(), e)

	if s1.Count() != 1 || s2.Count() != 1 {
		t.Fatalf("expected 1/1, got %d/%d", s1.Count(), s2.Count())
	}
}

func TestMultiSink_Concurrent(t *testing.T) {
	sink := NewJSONSink(10000)
	multi := NewMultiSink(sink)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			multi.Emit(context.Background(), NewEvent().ClientModel("concurrent").Build())
		}()
	}
	wg.Wait()

	if sink.Count() != 100 {
		t.Fatalf("expected 100, got %d", sink.Count())
	}
}

func TestComputeRequestChecksum(t *testing.T) {
	cs1 := ComputeRequestChecksum("gpt-4o", []byte(`{"messages":[]}`))
	cs2 := ComputeRequestChecksum("gpt-4o", []byte(`{"messages":[]}`))
	cs3 := ComputeRequestChecksum("gpt-4o", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))

	if cs1 != cs2 {
		t.Fatal("same input should produce same checksum")
	}
	if cs1 == cs3 {
		t.Fatal("different input should produce different checksum")
	}
	if len(cs1) != 64 {
		t.Fatalf("expected 64-char hex, got %d", len(cs1))
	}
}

func TestEventBuilder_RequestChecksum(t *testing.T) {
	e := NewEvent().RequestChecksum([]byte("test body")).Build()
	if len(e.RequestChecksum) != 64 {
		t.Fatalf("expected 64-char checksum, got %d chars", len(e.RequestChecksum))
	}
}

func TestEventBuilder_AllFields(t *testing.T) {
	e := NewEvent().
		RequestID("req-123").
		TenantID("tenant-1").
		ApplicationID(2).
		APIKeyID(5).
		ClientModel("claude-3.5").
		OutboundModel("claude-3-5-sonnet").
		ResolutionPath("canonical").
		CanonicalName("claude-3.5").
		IdentityHash("abc123").
		ClientProfile("cursor").
		Stream(true).
		Provider(3).
		Credential(7).
		Success(true).
		Latency(200*time.Millisecond).
		Tokens(50, 25).
		Cost(0.003).
		TransformRule("rule_1").
		Build()

	if e.RequestID != "req-123" {
		t.Fatal("request_id mismatch")
	}
	if e.TenantID != "tenant-1" {
		t.Fatal("tenant_id mismatch")
	}
	if e.ApplicationID != 2 {
		t.Fatal("application_id mismatch")
	}
	if e.APIKeyID != 5 {
		t.Fatal("api_key_id mismatch")
	}
	if e.CostUSD != 0.003 {
		t.Fatal("cost mismatch")
	}
	if e.TransformRule != "rule_1" {
		t.Fatal("transform_rule mismatch")
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.ClientModel != "claude-3.5" {
		t.Fatal("round-trip client_model mismatch")
	}
}

func TestExtractDeltaText(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "content delta",
			payload: `{"choices":[{"index":0,"delta":{"content":"hello","role":"assistant"}}]}`,
			want:    "hello",
		},
		{
			name:    "role only (no content)",
			payload: `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			want:    "",
		},
		{
			name:    "empty content",
			payload: `{"choices":[{"index":0,"delta":{"content":"","role":"assistant"}}]}`,
			want:    "",
		},
		{
			name:    "not data line",
			payload: `not json`,
			want:    "",
		},
		{
			name:    "no choices",
			payload: `{"model":"x"}`,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractDeltaText(tc.payload); got != tc.want {
				t.Errorf("extractDeltaText(%q) = %q, want %q", tc.payload, got, tc.want)
			}
		})
	}
}

func TestExtractDeltaToolText(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "single tool call with name and args",
			payload: `{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"BJ\"}"}}]}}]}`,
			want:    "[tool:get_weather] {\"city\":\"BJ\"}",
		},
		{
			name:    "tool call with args only (subsequent chunk)",
			payload: `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"foo\""}}]}}]}`,
			want:    "\"foo\"",
		},
		{
			name:    "no tool calls",
			payload: `{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			want:    "",
		},
		{
			name:    "empty tool_calls array",
			payload: `{"choices":[{"index":0,"delta":{"tool_calls":[]}}]}`,
			want:    "",
		},
		{
			name:    "tool call without function name",
			payload: `{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"x","function":{"arguments":"{}"}}]}}]}`,
			want:    "{}",
		},
		{
			name:    "invalid json",
			payload: `{`,
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractDeltaToolText(tc.payload); got != tc.want {
				t.Errorf("extractDeltaToolText(%q) = %q, want %q", tc.payload, got, tc.want)
			}
		})
	}
}

// TestStreamCapture_ToolCalls ensures that function-calling responses (which
// have delta.tool_calls but no delta.content) still produce a non-empty
// stream_text_content. This is the regression case for minimax-m3 sessions.
func TestStreamCapture_ToolCalls(t *testing.T) {
	sc := NewStreamCapture()
	// Chunk 1: tool call start (name + first args)
	sc.ObservePayload(
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":"}}]}}]}`,
		"", false)
	// Chunk 2: tool call args continuation
	sc.ObservePayload(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\""}}]}}]}`,
		"", false)
	// Chunk 3: done
	sc.ObservePayload("[DONE]", "", true)

	m := sc.SummaryAsMap()
	text, ok := m["stream_text_content"].(string)
	if !ok || text == "" {
		t.Fatalf("expected non-empty stream_text_content for tool_calls, got %v", m["stream_text_content"])
	}
	if !strings.Contains(text, "[tool:terminal]") {
		t.Errorf("expected tool name in textContent, got %q", text)
	}
	if !strings.Contains(text, "ls") {
		t.Errorf("expected args in textContent, got %q", text)
	}
	if m["stream_done_received"].(bool) != true {
		t.Error("expected done_received=true")
	}
}

// TestStreamCapture_LongStreamContent verifies that textContent can grow
// beyond 8192 bytes (the previous limit), up to 65536 bytes. This handles
// long streaming responses that previously got truncated.
func TestStreamCapture_LongStreamContent(t *testing.T) {
	sc := NewStreamCapture()
	chunk := strings.Repeat("a", 500)
	for i := 0; i < 200; i++ {
		sc.ObservePayload(
			`{"choices":[{"index":0,"delta":{"content":"`+chunk+`"}}]}`,
			"", false)
	}
	m := sc.SummaryAsMap()
	text := m["stream_text_content"].(string)
	if len(text) < 65536 {
		t.Errorf("expected textContent to reach 65536 bytes, got %d", len(text))
	}
}
