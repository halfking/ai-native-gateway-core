package streaming

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SR-W1 Phase 0B (doc 18 §17): with the gate enabled in immediate mode the
// bridges must produce byte-identical client output — including synthesized
// terminators on error paths — compared to the gate-disabled legacy path.
// This is the safety proof before deferred commit semantics ship.

type bridgeCase struct {
	name    string
	serve   func(w http.ResponseWriter, r *http.Request)
	invoke  func(w http.ResponseWriter, resp *http.Response) StreamOutcome
	wantAny []string // sanity assertions on the legacy output
}

func upstreamPost(t *testing.T, serve func(w http.ResponseWriter, r *http.Request)) *http.Response {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(serve))
	t.Cleanup(upstream.Close)
	resp, err := http.Post(upstream.URL, "application/json", strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

func runBridge(t *testing.T, bc bridgeCase) string {
	t.Helper()
	resp := upstreamPost(t, bc.serve)
	defer func() { _ = resp.Body.Close() }()
	rec := httptest.NewRecorder()
	bc.invoke(rec, resp)
	return rec.Body.String()
}

func bridgeCases() []bridgeCase {
	return []bridgeCase{
		{
			name: "openai passthrough normal stream",
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamChatWithPendingCapture(w, resp, "gpt-4o", "gpt-4o", nil, nil, false, nil, nil)
			},
			wantAny: []string{"Hi", "[DONE]"},
		},
		{
			name: "openai passthrough EOF without DONE",
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
				// hard EOF: no [DONE]
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamChatWithPendingCapture(w, resp, "gpt-4o", "gpt-4o", nil, nil, false, nil, nil)
			},
			wantAny: []string{"Hi", "[DONE]"},
		},
		{
			name: "responses passthrough normal stream",
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamResponsesSSE(w, resp, "gpt-4o", "gpt-4o", "test-req-id-123456789012345678", nil)
			},
			wantAny: []string{"response.output_text.delta", "response.completed"},
		},
		{
			name: "openai to anthropic normal stream",
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamOpenAIToAnthropicSSE(w, resp, "gpt-4o", "gpt-4o", "test-req-id-123456789012345678", nil, nil)
			},
			wantAny: []string{"message_start", "content_block_delta", "message_stop"},
		},
		{
			name: "anthropic to openai normal stream",
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3}}}\n\n")
				fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
				fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n")
				fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
				fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToOpenAI(w, resp, "claude", "claude", "test-req-id-123456789012345678", nil, nil)
			},
			wantAny: []string{"choices", "[DONE]"},
		},
		{
			name: "anthropic to responses normal stream",
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3}}}\n\n")
				fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
				fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n")
				fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
				fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToResponses(w, resp, "claude", "claude", "test-req-id-123456789012345678", nil, nil)
			},
			wantAny: []string{"response.output_text.delta", "response.completed"},
		},
	}
}

func TestBridgeWireBytesIdenticalWithImmediateGate(t *testing.T) {
	for _, bc := range bridgeCases() {
		t.Run(bc.name, func(t *testing.T) {
			restore := setAttemptGateForTest(false, AttemptCommitFirstSemantic)
			legacy := runBridge(t, bc)
			restore()

			for _, want := range bc.wantAny {
				if !strings.Contains(legacy, want) {
					t.Fatalf("legacy output missing %q:\n%s", want, legacy)
				}
			}

			restore = setAttemptGateForTest(true, AttemptCommitImmediate)
			gated := runBridge(t, bc)
			restore()

			if legacy != gated {
				t.Fatalf("immediate-gate output differs from legacy:\n--- legacy ---\n%q\n--- gated ---\n%q", legacy, gated)
			}
		})
	}
}

// Deferred semantics at the bridge level: an upstream EOF without a proper
// terminator — while the gate holds only uncommitted metadata — must NOT
// synthesize a client-side terminator. The bridge returns a structured
// outcome and the (W2) coordinator decides discard/retry/final render.
func TestBridgeDeferredModeKeepsMetadataAttemptDroppable(t *testing.T) {
	restore := setAttemptGateForTest(true, AttemptCommitFirstSemantic)
	defer restore()

	t.Run("openai passthrough EOF without DONE and no content", func(t *testing.T) {
		out := runBridge(t, bridgeCase{
			serve: func(w http.ResponseWriter, r *http.Request) {
				// role-only chunk (attempt metadata), then hard EOF
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamChatWithPendingCapture(w, resp, "gpt-4o", "gpt-4o", nil, nil, false, nil, nil)
			},
		})
		if strings.Contains(out, "[DONE]") {
			t.Fatalf("deferred mode must not synthesize [DONE] on uncommitted attempt, got:\n%s", out)
		}
		if strings.Contains(out, "role") {
			t.Fatalf("deferred mode must buffer attempt metadata, got:\n%s", out)
		}
	})

	t.Run("responses passthrough EOF without DONE and no content", func(t *testing.T) {
		out := runBridge(t, bridgeCase{
			serve: func(w http.ResponseWriter, r *http.Request) {
				// role-only chunk (attempt metadata), then hard EOF
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamResponsesSSE(w, resp, "gpt-4o", "gpt-4o", "test-req-id-123456789012345678", nil)
			},
		})
		if strings.Contains(out, "response.completed") {
			t.Fatalf("deferred mode must not fabricate a completed response on uncommitted attempt, got:\n%s", out)
		}
	})

	t.Run("anthropic to openai EOF without stop and no content", func(t *testing.T) {
		out := runBridge(t, bridgeCase{
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":3}}}\n\n")
				// hard EOF: no message_delta/message_stop
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamAnthropicSSEToOpenAI(w, resp, "claude", "claude", "test-req-id-123456789012345678", nil, nil)
			},
		})
		if strings.Contains(out, "[DONE]") {
			t.Fatalf("deferred mode must not synthesize [DONE] on uncommitted attempt, got:\n%s", out)
		}
	})
}

// Once real content has been committed, the legacy terminal rendering must
// keep working under the deferred policy — the client saw content and needs a
// well-formed ending even if the upstream then dies.
func TestBridgeDeferredModeStillFinishesAfterContentCommitted(t *testing.T) {
	restore := setAttemptGateForTest(true, AttemptCommitFirstSemantic)
	defer restore()

	t.Run("openai passthrough EOF after content", func(t *testing.T) {
		out := runBridge(t, bridgeCase{
			serve: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
				// hard EOF after content
			},
			invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
				return StreamChatWithPendingCapture(w, resp, "gpt-4o", "gpt-4o", nil, nil, false, nil, nil)
			},
		})
		if !strings.Contains(out, "Hi") || !strings.Contains(out, "[DONE]") {
			t.Fatalf("committed attempt must still receive content + synthesized terminator, got:\n%s", out)
		}
	})

	t.Run("responses passthrough normal stream matches legacy bytes", func(t *testing.T) {
		restoreOff := setAttemptGateForTest(true, AttemptCommitFirstSemantic)
		deferred := runBridge(t, bridgeCases()[2])
		restoreOff()
		restoreOff = setAttemptGateForTest(false, AttemptCommitFirstSemantic)
		legacy := runBridge(t, bridgeCases()[2])
		restoreOff()
		if deferred != legacy {
			t.Fatalf("deferred output differs from legacy on a normal stream:\n%q\nvs\n%q", deferred, legacy)
		}
	})
}

// Normal completed streams must be byte-identical under the deferred policy
// too: metadata buffers, then the first content frame commits in order.
func TestBridgeDeferredModeNormalStreamByteIdentical(t *testing.T) {
	for _, bc := range bridgeCases() {
		t.Run(bc.name, func(t *testing.T) {
			restore := setAttemptGateForTest(false, AttemptCommitFirstSemantic)
			legacy := runBridge(t, bc)
			restore()

			restore = setAttemptGateForTest(true, AttemptCommitFirstSemantic)
			deferred := runBridge(t, bc)
			restore()

			if legacy != deferred {
				t.Fatalf("deferred output differs from legacy:\n--- legacy ---\n%q\n--- deferred ---\n%q", legacy, deferred)
			}
		})
	}
}

// doc 18 §16.1 #9: first-byte timeout must not write a terminal frame while
// the gate holds an uncommitted attempt — the bridge returns a structured
// outcome and the coordinator owns the final rendering. Under the immediate
// (Phase 0B) policy and with the gate disabled the legacy error frame still
// reaches the wire.
func TestBridgeFirstByteTimeoutNoTerminalFrameBeforeCommit(t *testing.T) {
	t.Setenv("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", "1")

	bridge := bridgeCase{
		serve: func(w http.ResponseWriter, r *http.Request) {
			// send headers, then stall the body past the 1s first-byte timeout
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		},
		invoke: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamChatWithPendingCapture(w, resp, "gpt-4o", "gpt-4o", nil, nil, false, nil, nil)
		},
	}

	restore := setAttemptGateForTest(true, AttemptCommitFirstSemantic)
	deferredOut, deferredOutcome := func() (string, StreamOutcome) {
		resp := upstreamPost(t, bridge.serve)
		defer func() { _ = resp.Body.Close() }()
		rec := httptest.NewRecorder()
		oc := bridge.invoke(rec, resp)
		return rec.Body.String(), oc
	}()
	restore()

	if !deferredOutcome.Interrupted || deferredOutcome.Reason != "first_byte_timeout" {
		t.Fatalf("outcome = %+v, want interrupted first_byte_timeout", deferredOutcome)
	}
	if strings.Contains(deferredOut, "first_byte_timeout") || strings.Contains(deferredOut, "error") {
		t.Fatalf("deferred mode must not write a terminal error frame on uncommitted attempt, got:\n%s", deferredOut)
	}
}
