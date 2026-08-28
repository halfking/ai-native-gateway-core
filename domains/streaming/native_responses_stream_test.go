package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestNativeResponsesEventReaderPreservesRawMultilineCRLF(t *testing.T) {
	raw := "comment\r\n:event-heartbeat\r\n\r\nevent: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"hi\"}\r\n\r\n"
	reader := NewNativeResponsesEventReader(strings.NewReader(raw), 1024)
	event, err := reader.ReadEvent(context.Background(), time.Second, nil)
	if err != nil {
		t.Fatalf("ReadEvent() error = %v", err)
	}
	if event.Name != "" || event.Class != FrameClassKeepalive || !bytes.Contains(event.Raw, []byte(":event-heartbeat")) {
		t.Fatalf("keepalive event = %#v, want preserved comment frame", event)
	}
	event, err = reader.ReadEvent(context.Background(), time.Second, nil)
	if err != nil {
		t.Fatalf("ReadEvent() second error = %v", err)
	}
	if event.Name != "response.output_text.delta" || event.Class != FrameClassContent {
		t.Fatalf("event = %#v, want content delta", event)
	}
	if string(event.Raw) != "event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"hi\"}\r\n\r\n" {
		t.Fatalf("raw frame changed: %q", event.Raw)
	}
}

func TestNativeResponsesEventReaderRejectsMalformedJSON(t *testing.T) {
	reader := NewNativeResponsesEventReader(strings.NewReader("event: response.created\ndata: {bad}\n\n"), 1024)
	if _, err := reader.ReadEvent(context.Background(), time.Second, nil); err == nil {
		t.Fatal("ReadEvent() error = nil, want malformed JSON")
	}
}

func TestNativeResponsesEventReaderLineLimit(t *testing.T) {
	reader := NewNativeResponsesEventReader(strings.NewReader("event: response.created\ndata: {\"x\":\"123456789\"}\n\n"), 8)
	if _, err := reader.ReadEvent(context.Background(), time.Second, nil); err == nil {
		t.Fatal("ReadEvent() error = nil, want line-too-long")
	}
}

func TestStreamNativeResponsesSSEPreservesLifecycleAndNoSyntheticDone(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_1\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()
	outcome := StreamNativeResponsesSSE(context.Background(), rec, resp, "req-native", capture)
	if outcome.Interrupted || outcome.ChunkCount != 1 {
		t.Fatalf("outcome = %#v, want completed one semantic event", outcome)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("wire changed or synthetic events added:\n got %q\nwant %q", got, body)
	}
}

func TestObserveNativeResponsesEventCapturesNativeSemantics(t *testing.T) {
	capture := audit.NewStreamCapture()
	for _, event := range []NativeResponsesEvent{
		{Name: "response.output_text.delta", Payload: map[string]json.RawMessage{"delta": json.RawMessage(`"hello"`)}},
		{Name: "response.reasoning_text.delta", Payload: map[string]json.RawMessage{"delta": json.RawMessage(`"think"`)}},
		{Name: "response.function_call_arguments.delta", Payload: map[string]json.RawMessage{"delta": json.RawMessage(`"{}"`)}},
		{Name: "response.completed", Payload: map[string]json.RawMessage{"response": json.RawMessage(`{"model":"gpt-native","usage":{"input_tokens":7,"output_tokens":2}}`)}},
	} {
		observeNativeResponsesEvent(capture, event)
	}
	if got := capture.TextContentSnapshot(); !strings.Contains(got, "hello") || !strings.Contains(got, "think") {
		t.Fatalf("captured text = %q, want native text and reasoning", got)
	}
	m := capture.SummaryAsMap()
	if m["prompt_tokens"] != 7 || m["completion_tokens"] != 2 || m["resp_model"] != "gpt-native" {
		t.Fatalf("native capture summary = %#v", m)
	}
}

func TestStreamNativeResponsesSSEFailedTerminalIsNotResumable(t *testing.T) {
	body := "event: response.failed\ndata: {\"type\":\"response.failed\",\"error\":{\"code\":\"upstream\"}}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	outcome := StreamNativeResponsesSSE(context.Background(), httptest.NewRecorder(), resp, "req-failed", audit.NewStreamCapture())
	if !outcome.Interrupted || outcome.Resumable || outcome.Reason != "native_response_failed" {
		t.Fatalf("outcome = %#v, want non-resumable failed terminal", outcome)
	}
}

func TestStreamNativeResponsesSSEPostSemanticEOFIsNotResumable(t *testing.T) {
	body := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	outcome := StreamNativeResponsesSSE(context.Background(), httptest.NewRecorder(), resp, "req-post", audit.NewStreamCapture())
	if !outcome.Interrupted || outcome.Resumable || outcome.Reason != "native_responses_read_error" {
		t.Fatalf("outcome = %#v, want non-resumable post-semantic EOF", outcome)
	}
}

func TestStreamNativeResponsesSSEPreSemanticEOFIsResumable(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	outcome := StreamNativeResponsesSSE(context.Background(), httptest.NewRecorder(), resp, "req-pre", audit.NewStreamCapture())
	if !outcome.Interrupted || !outcome.Resumable || outcome.Kind != errorsx.KindUpstreamDown {
		t.Fatalf("outcome = %#v, want resumable pre-semantic EOF", outcome)
	}
}
