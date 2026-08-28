package streaming

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

// TestStreamNativeResponsesSSE_InteropTopology verifies the gateway's
// compatible Responses event surface forwards a complete, SDK-style event
// topology verbatim and in order:
//   - message item lifecycle (output_item.added → content_part.added →
//     output_text.delta* → output_text.done → output_item.done)
//   - function-call item lifecycle (output_item.added[function_call] →
//     function_call_arguments.delta* → output_item.done)
//   - reasoning terminal (reasoning_summary_part.added →
//     reasoning_summary_part.done → reasoning_item.done[completed])
//   - audio event contract (audio.delta → audio.done →
//     audio_transcript.delta → audio_transcript.done)
//   - a large content delta (overflow handling) forwarded without truncation
//   - terminates cleanly on response.completed
//
// This is the contract a consuming Responses SDK client relies on.
//
// NOTE: interop against the OFFICIAL OpenAI Responses SDK (a live
// round-trip through /v1/responses built with the SDK client) is UNKNOWN in
// this sandbox — the SDK is not a dependency (absent from go.mod) and no
// provider keys or network are available. This test exercises the gateway's
// own compatible event serializer, which is the surface an SDK would consume.
func TestStreamNativeResponsesSSE_InteropTopology(t *testing.T) {
	// ~10KB content delta to exercise overflow handling without truncation.
	longText := strings.Repeat("世界", 5000)

	body := "" +
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
		// message item lifecycle
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
		"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"item_id\":\"msg_1\",\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"content_index\":0,\"delta\":\"Hello\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"content_index\":0,\"delta\":\"" + longText + "\"}\n\n" +
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"item_id\":\"msg_1\",\"content_index\":0,\"text\":\"Hello" + longText + "\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello" + longText + "\"}]}}\n\n" +
		// function-call item lifecycle
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"name\":\"get_weather\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"Beijing\\\"}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"Beijing\\\"}\"}}\n\n" +
		// reasoning terminal
		"event: response.reasoning_summary_part.added\ndata: {\"type\":\"response.reasoning_summary_part.added\",\"item_id\":\"rs_1\",\"summary_index\":0,\"part\":{\"type\":\"summary_text\",\"text\":\"\"}}\n\n" +
		"event: response.reasoning_summary_part.done\ndata: {\"type\":\"response.reasoning_summary_part.done\",\"item_id\":\"rs_1\",\"summary_index\":0,\"part\":{\"type\":\"summary_text\",\"text\":\"thinking...\"}}\n\n" +
		"event: response.reasoning_item.done\ndata: {\"type\":\"response.reasoning_item.done\",\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"thinking...\"}]}}\n\n" +
		// audio event contract
		"event: response.audio.delta\ndata: {\"type\":\"response.audio.delta\",\"item_id\":\"msg_1\",\"content_index\":1,\"delta\":\"AUDIO_BYTES\"}\n\n" +
		"event: response.audio.done\ndata: {\"type\":\"response.audio.done\",\"item_id\":\"msg_1\",\"content_index\":1}\n\n" +
		"event: response.audio_transcript.delta\ndata: {\"type\":\"response.audio_transcript.delta\",\"item_id\":\"msg_1\",\"content_index\":1,\"delta\":\"spoken\"}\n\n" +
		"event: response.audio_transcript.done\ndata: {\"type\":\"response.audio_transcript.done\",\"item_id\":\"msg_1\",\"content_index\":1}\n\n" +
		// terminal
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n"

	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	rec := httptest.NewRecorder()
	capture := audit.NewStreamCapture()

	outcome := StreamNativeResponsesSSE(context.Background(), rec, resp, "req-interop", capture)
	if outcome.Interrupted {
		t.Fatalf("outcome.Interrupted=true reason=%q kind=%v, want completed stream", outcome.Reason, outcome.Kind)
	}
	if outcome.ChunkCount == 0 {
		t.Fatal("expected semantic chunk count > 0 (content/tool-call frames)")
	}

	// Verbatim passthrough: the compatible Responses surface must not alter
	// or drop any event a consuming SDK client expects.
	if got := rec.Body.String(); got != body {
		t.Fatalf("wire changed or events dropped:\n got  %q\n want %q", got, body)
	}
}

// TestStreamNativeResponsesSSE_FunctionCallOnlyLifecycle isolates the
// function-call item lifecycle (the most failure-prone Responses topology):
// a lone function_call item must forward through output_item.added →
// function_call_arguments.delta* → output_item.done and terminate on
// response.completed without injecting message scaffold.
func TestStreamNativeResponsesSSE_FunctionCallOnlyLifecycle(t *testing.T) {
	body := "" +
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_2\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"status\":\"in_progress\",\"name\":\"get_time\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_2\",\"delta\":\"{\\\"tz\\\":\\\"Asia/Shanghai\\\"}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"get_time\",\"arguments\":\"{\\\"tz\\\":\\\"Asia/Shanghai\\\"}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\"}}\n\n"

	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	rec := httptest.NewRecorder()

	outcome := StreamNativeResponsesSSE(context.Background(), rec, resp, "req-fc", audit.NewStreamCapture())
	if outcome.Interrupted {
		t.Fatalf("outcome.Interrupted=true reason=%q, want completed", outcome.Reason)
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("function-call lifecycle not forwarded verbatim:\n got  %q\n want %q", got, body)
	}
}
