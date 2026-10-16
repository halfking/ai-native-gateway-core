package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const (
	continuationRoleFrame = "data: {\"id\":\"cont\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n"
	continuationTextFrame = "data: {\"id\":\"cont\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one\"},\"finish_reason\":null}]}\n\n"
	continuationDoneFrame = "data: [DONE]\n\n"
	continuationErrorFrame = "data: {\"error\":{\"type\":\"provider_secret_type\",\"code\":\"provider_secret_code\",\"message\":\"supplier secret detail must not reach client\"}}\n\n"
)

type continuationCloseRecorder struct {
	io.Reader
	closed bool
}

func (r *continuationCloseRecorder) Close() error {
	r.closed = true
	return nil
}

func continuationResponse(ctx context.Context, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
		Request:    httptest.NewRequest(http.MethodPost, "http://upstream.test/v1/chat/completions", nil).WithContext(ctx),
	}
}

func countContinuationSentinel(wire string) int {
	return strings.Count(wire, "data: [DONE]")
}

func TestP03ContinuationInBandErrorStopsWithoutSplice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stream  string
		wantText string
	}{
		{
			name:     "first frame error remains resumable and never continues",
			stream:   continuationErrorFrame,
			wantText: "",
		},
		{
			name:     "committed text then error gets one safe terminal",
			stream:   continuationRoleFrame + continuationTextFrame + continuationErrorFrame,
			wantText: "one",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx := executors.WithStreamContinuation(context.Background(), func(string) (io.ReadCloser, error) {
				calls++
				return io.NopCloser(strings.NewReader(continuationDoneFrame)), nil
			})
			recorder := httptest.NewRecorder()
			capture := audit.NewStreamCapture()
			outcome := StreamChatWithPendingCapture(ctx, recorder, continuationResponse(ctx, io.NopCloser(strings.NewReader(tc.stream))), "model", "model", nil, capture, false, nil, nil)
			wire := recorder.Body.String()

			if calls != 0 {
				t.Fatalf("continuation calls = %d, want 0 after ChunkTypeError", calls)
			}
			if !outcome.Interrupted || outcome.Reason != "upstream_error" || outcome.Kind != errorsx.KindUpstreamDown {
				t.Fatalf("outcome = %+v, want safe upstream_error interruption", outcome)
			}
			if strings.Contains(wire, "supplier secret detail") || strings.Contains(wire, "provider_secret_type") || strings.Contains(wire, "provider_secret_code") {
				t.Fatalf("raw upstream error leaked: %q", wire)
			}
			if tc.wantText == "" {
				if wire != "" {
					t.Fatalf("uncommitted error must remain byte-silent for failover, got %q", wire)
				}
				if outcome.TerminalRendered {
					t.Fatal("uncommitted error must not claim a rendered terminal")
				}
				return
			}
			if !strings.Contains(wire, tc.wantText) {
				t.Fatalf("committed text missing from client wire: %q", wire)
			}
			if !strings.Contains(wire, `"code":"upstream_error"`) || !strings.Contains(wire, "upstream stream returned an error") {
				t.Fatalf("committed error must render the gateway-safe terminal: %q", wire)
			}
			if got := countContinuationSentinel(wire); got != 1 {
				t.Fatalf("terminal count = %d, want exactly one: %q", got, wire)
			}
			if !outcome.TerminalRendered {
				t.Fatal("committed error must latch TerminalRendered")
			}
		})
	}
}

func TestP03ContinuationSuppressesRoleAfterMetadataAndAccumulatesVisibleContentOnce(t *testing.T) {
	calls := 0
	ctx := executors.WithStreamContinuation(context.Background(), func(prefix string) (io.ReadCloser, error) {
		calls++
		if prefix != "one" {
			t.Fatalf("continuation prefix = %q, want only client-visible content once", prefix)
		}
		return io.NopCloser(strings.NewReader(
			"data: {\"id\":\"cont\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"+
				"data: {\"id\":\"cont\",\"choices\":[{\"index\":0,\"delta\":{},\"usage\":{\"prompt_tokens\":1}}]}\n\n"+
				"data: {\"id\":\"cont\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\" two\"}}]}\n\n"+
				continuationDoneFrame)), nil
	})

	recorder := httptest.NewRecorder()
	outcome := StreamChatWithPendingCapture(ctx, recorder, continuationResponse(ctx, io.NopCloser(strings.NewReader(continuationRoleFrame+continuationTextFrame))), "model", "model", nil, audit.NewStreamCapture(), false, nil, nil)
	wire := recorder.Body.String()
	if calls != 1 {
		t.Fatalf("continuation calls = %d, want 1", calls)
	}
	if outcome.Interrupted {
		t.Fatalf("continuation should complete after metadata frames: %+v", outcome)
	}
	if got := strings.Count(wire, `"role":"assistant"`); got != 1 {
		t.Fatalf("assistant role count = %d, want exactly first-leg role; wire=%q", got, wire)
	}
	if got := strings.Count(wire, `"content":"one"`); got != 1 {
		t.Fatalf("first-leg visible content was duplicated %d times: %q", got, wire)
	}
	if !strings.Contains(wire, `"content":" two"`) {
		t.Fatalf("continuation content missing: %q", wire)
	}
}

func TestP03ContinuationClosesEOFBodiesBeforeHook(t *testing.T) {
	firstBody := &continuationCloseRecorder{Reader: strings.NewReader(continuationRoleFrame + continuationTextFrame)}
	secondBody := &continuationCloseRecorder{Reader: strings.NewReader(continuationDoneFrame)}
	ctx := executors.WithStreamContinuation(context.Background(), func(string) (io.ReadCloser, error) {
		if !firstBody.closed {
			t.Fatal("EOF body must be closed before continuation hook runs")
		}
		return secondBody, nil
	})
	recorder := httptest.NewRecorder()
	outcome := StreamChatWithPendingCapture(ctx, recorder, continuationResponse(ctx, firstBody), "model", "model", nil, audit.NewStreamCapture(), false, nil, nil)
	if outcome.Interrupted {
		t.Fatalf("continuation should finish cleanly: %+v", outcome)
	}
	if !secondBody.closed {
		t.Fatal("continuation body must be closed when stream returns")
	}
}

func TestP03ContinuationRejectsNonzeroAndMultipleChoices(t *testing.T) {
	cases := []string{
		`data: {"id":"cont","choices":[{"index":1,"delta":{"content":"one"}}]}` + "\n\n",
		`data: {"id":"cont","choices":[{"index":0,"delta":{"content":"one"}},{"index":1,"delta":{"content":"two"}}]}` + "\n\n",
	}
	for _, stream := range cases {
		t.Run(stream, func(t *testing.T) {
			calls := 0
			ctx := executors.WithStreamContinuation(context.Background(), func(string) (io.ReadCloser, error) {
				calls++
				return io.NopCloser(strings.NewReader(continuationDoneFrame)), nil
			})
			recorder := httptest.NewRecorder()
			outcome := StreamChatWithPendingCapture(ctx, recorder, continuationResponse(ctx, io.NopCloser(strings.NewReader(stream))), "model", "model", nil, audit.NewStreamCapture(), false, nil, nil)
			if calls != 0 {
				t.Fatalf("continuation calls = %d, want 0 for ambiguous choice stream", calls)
			}
			if !outcome.Interrupted || outcome.Reason != "eof_without_done" {
				t.Fatalf("outcome = %+v, want original EOF terminal", outcome)
			}
		})
	}
}

func TestP03ContinuationUsageCaptureSumsLegsAndUsesLastValuePerLeg(t *testing.T) {
	first := continuationRoleFrame + continuationTextFrame +
		`data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}` + "\n\n"
	second := `data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":13,"total_tokens":21}}` + "\n\n" + continuationDoneFrame

	ctx := executors.WithStreamContinuation(context.Background(), func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(second)), nil
	})
	capture := audit.NewStreamCapture()
	recorder := httptest.NewRecorder()
	outcome := StreamChatWithPendingCapture(ctx, recorder, continuationResponse(ctx, io.NopCloser(strings.NewReader(first))), "model", "model", nil, capture, false, nil, nil)
	if outcome.Interrupted {
		t.Fatalf("continuation should complete: %+v", outcome)
	}
	summary := capture.SummaryAsMap()
	if got, want := summary["prompt_tokens"], 12; got != want {
		t.Fatalf("prompt_tokens = %v, want %d; summary=%v", got, want, summary)
	}
	if got, want := summary["completion_tokens"], 19; got != want {
		t.Fatalf("completion_tokens = %v, want %d; summary=%v", got, want, summary)
	}
}

func TestP03ContinuationPrefixByteLimitStopsSecondRequest(t *testing.T) {
	// Production cap is introduced with this repair; retain a local literal so
	// this overlay first proves the baseline does not yet expose the bound.
	prefix := strings.Repeat("x", 64*1024+1)
	stream := `data: {"id":"cont","choices":[{"index":0,"delta":{"content":"` + prefix + `"}}]}` + "\n\n"
	calls := 0
	ctx := executors.WithStreamContinuation(context.Background(), func(string) (io.ReadCloser, error) {
		calls++
		return nil, errors.New("must not be called")
	})
	recorder := httptest.NewRecorder()
	outcome := StreamChatWithPendingCapture(ctx, recorder, continuationResponse(ctx, io.NopCloser(strings.NewReader(stream))), "model", "model", nil, audit.NewStreamCapture(), false, nil, nil)
	if calls != 0 {
		t.Fatalf("continuation calls = %d, want 0 once prefix exceeds cap", calls)
	}
	if !outcome.Interrupted || outcome.Reason != "eof_without_done" {
		t.Fatalf("outcome = %+v, want original EOF terminal", outcome)
	}
}
