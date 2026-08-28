package streaming

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const emptyDeltaFrame = "data: {\"id\":\"empty\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{}}]}\n\n"
const earlyEmptyContentDeltaFrame = "data: {\"id\":\"content\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"

func withEarlyEmptyThreshold(t *testing.T, threshold int) {
	t.Helper()
	previous := streamConfigStore.Load()
	streamConfigStore.Store(config.NewStore(&config.Config{
		EnableEmptyStreamGate:       true,
		EmptyStreamEarlyEmptyChunks: threshold,
	}))
	t.Cleanup(func() { streamConfigStore.Store(previous) })
	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "")
}

func runEmptyGateForTest(t *testing.T, startingLine, remaining string) ([]string, *StreamOutcome, string) {
	t.Helper()
	body := io.NopCloser(strings.NewReader(remaining))
	recorder := httptest.NewRecorder()
	lastSend := time.Now()
	chunkCount := 0
	lines, outcome := runEmptyStreamGate(
		context.Background(),
		bufio.NewReader(body),
		body,
		recorder,
		recorder,
		nil,
		nil,
		nil,
		"gpt-test",
		new(string),
		startingLine,
		time.Second,
		&lastSend,
		&chunkCount,
		nil,
	)

	return lines, outcome, recorder.Body.String()
}

func TestRunEmptyStreamGateVendorFieldsAreSanitized(t *testing.T) {
	withEarlyEmptyThreshold(t, 0)
	starting := `data: {"id":"start","choices":[{"delta":{"content":"hello"}}],"zhipu_request_id":"private"}` + "\n"
	remaining := `data: {"id":"next","choices":[{"delta":{"content":"world"}}],"web_search_results":[{"title":"private"}]}` + "\n"
	body := io.NopCloser(strings.NewReader(remaining))
	recorder := httptest.NewRecorder()
	lastSend := time.Now()
	chunkCount := 0
	lines, outcome := runEmptyStreamGateWithVendor(context.Background(), bufio.NewReader(body), body, recorder, recorder, nil, nil, nil, "gpt-test", new(string), starting, time.Second, &lastSend, &chunkCount, nil, "zhipu", StripZhipuFieldsBody)
	if outcome != nil {
		t.Fatalf("unexpected gate outcome: %+v", outcome)
	}
	joined := strings.Join(lines, "")
	if strings.Contains(joined, "zhipu_request_id") || strings.Contains(joined, "web_search_results") {
		t.Fatalf("vendor-private fields leaked from gate: %s", joined)
	}
}

func TestRunEmptyStreamGateEarlyEmptyDetection(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)

	_, outcome, wire := runEmptyGateForTest(t, emptyDeltaFrame, emptyDeltaFrame+emptyDeltaFrame)
	if outcome == nil {
		t.Fatal("expected early-empty outcome")
	}
	if outcome.Reason != "early_empty_detection" || outcome.Kind != errorsx.KindEmptyResponse {
		t.Fatalf("outcome = %+v, want early empty KindEmptyResponse", outcome)
	}
	if !outcome.Resumable || outcome.ChunkCount != 0 {
		t.Fatalf("outcome must be resumable with zero client chunks: %+v", outcome)
	}
	if wire != "" {
		t.Fatalf("early-empty buffer leaked to wire: %q", wire)
	}
}

func TestRunEmptyStreamGateContentAfterEmptyDeltasFlushes(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)

	lines, outcome, wire := runEmptyGateForTest(t, emptyDeltaFrame, emptyDeltaFrame+earlyEmptyContentDeltaFrame)
	if outcome != nil {
		t.Fatalf("content after two empty deltas should pass gate, got %+v", outcome)
	}
	if got := strings.Join(lines, ""); !strings.Contains(got, "content") || !strings.Contains(got, "ok") {
		t.Fatalf("flushed lines = %q, want all buffered content", got)
	}
	if wire != "" {
		t.Fatalf("gate must return frames to caller, not write directly: %q", wire)
	}
}

func TestRunEmptyStreamGateControlFramesDoNotTriggerEarlyEmpty(t *testing.T) {
	withEarlyEmptyThreshold(t, 2)
	usage := "data: {\"id\":\"usage\",\"usage\":{\"prompt_tokens\":1}}\n\n"
	comment := ": keep-alive\n\n"
	malformed := "data: not-json\n\n"
	missingChoices := "data: {\"id\":\"metadata\",\"model\":\"gpt-test\"}\n\n"

	lines, outcome, _ := runEmptyGateForTest(t, emptyDeltaFrame, usage+comment+malformed+missingChoices+earlyEmptyContentDeltaFrame)
	if outcome != nil {
		t.Fatalf("control frames must not trigger early-empty detection, got %+v", outcome)
	}
	if got := strings.Join(lines, ""); !strings.Contains(got, "ok") {
		t.Fatalf("expected content to release buffered frames, got %q", got)
	}
}

func TestRunEmptyStreamGateEarlyEmptyCanBeDisabled(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	t.Setenv("LLM_GATEWAY_EMPTY_STREAM_EARLY_EMPTY_CHUNKS", "0")

	_, outcome, wire := runEmptyGateForTest(t, emptyDeltaFrame, emptyDeltaFrame+emptyDeltaFrame+"data: [DONE]\n\n")
	if outcome == nil || outcome.Reason != "empty_stream_no_content" {
		t.Fatalf("disabled early detection should retain DONE-based empty handling, got %+v", outcome)
	}
	if wire != "" {
		t.Fatalf("empty stream buffer leaked to wire: %q", wire)
	}
}

func TestIsEmptySemanticDelta(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "valid empty delta", payload: `{"choices":[{"delta":{}}]}`, want: true},
		{name: "role-only delta", payload: `{"choices":[{"delta":{"role":"assistant"}}]}`, want: true},
		{name: "finish-only delta", payload: `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, want: true},
		{name: "content", payload: `{"choices":[{"delta":{"content":"ok"}}]}`, want: false},
		{name: "usage", payload: `{"usage":{"prompt_tokens":1}}`, want: false},
		{name: "no choices", payload: `{"id":"metadata"}`, want: false},
		{name: "empty choices", payload: `{"choices":[]}`, want: false},
		{name: "malformed", payload: `not-json`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptySemanticDelta(tc.payload); got != tc.want {
				t.Fatalf("isEmptySemanticDelta(%s) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}
