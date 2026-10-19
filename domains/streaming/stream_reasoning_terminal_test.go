package streaming

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func reasoningTerminalBody(count int, answer, done bool) string {
	body := strings.Repeat(reasoningProgressFrame, count)
	if answer {
		body += earlyEmptyContentDeltaFrame
	}
	body += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"
	if done {
		body += "data: [DONE]\n\n"
	}
	return body
}

func TestReasoningChatTerminalMatrix(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	for _, count := range []int{8, 25, 30, 49, 50, 60} {
		for _, answer := range []bool{false, true} {
			for _, done := range []bool{false, true} {
				for _, pending := range []bool{false, true} {
					t.Run(fmt.Sprintf("chunks=%d/answer=%t/done=%t/pending=%t", count, answer, done, pending), func(t *testing.T) {
						rec := httptest.NewRecorder()
						sw := NewSerializedStreamWriter(rec)
						gate := NewAttemptCommitGate(t.Context(), ProtocolOpenAIChat, sw, GateOptions{
							Mode: GateModeBuffered, HoldbackWindow: 10 * time.Second, HoldbackMaxChunks: 50,
						})
						gw := NewGateWriterWithResponse(gate, rec)
						resp := &http.Response{Body: io.NopCloser(strings.NewReader(reasoningTerminalBody(count, answer, done))), Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)}
						var pc *pendingCapturer
						if pending {
							pc = NewPendingCapturer(64 * 1024)
						}
						capture := audit.NewStreamCapture()
						out := StreamChatWithPendingCapture(context.Background(), gw, resp, "minimax-m3", "minimax-m3", NewNormalizer(), capture, false, nil, pc)
						if pc != nil {
							_, state, ok := pc.Snapshot()
							want := "failed"
							if answer {
								want = "completed"
							}
							if !ok || state.Status != want {
								t.Fatalf("pending capture status=%+v want=%s", state, want)
							}
						}
						if answer {
							if out.Interrupted {
								t.Fatalf("reasoning then answer must succeed: %+v", out)
							}
							if err := gw.Finish(); err != nil {
								t.Fatal(err)
							}
							if !strings.Contains(rec.Body.String(), `"content":"ok"`) || strings.Contains(rec.Body.String(), `"error":`) || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
								t.Fatalf("answer terminal lost: %q", rec.Body.String())
							}
							return
						}
						if !out.Interrupted || ((done || count <= 50) && (out.Kind != errorsx.KindEmptyResponse || out.Reason != "openai_empty_response")) {
							t.Fatalf("reasoning without an answer returned success: %+v", out)
						}
						_, _, auditDone, interrupted, _ := capture.Snapshot()
						if auditDone || !interrupted {
							t.Fatalf("empty turn audited as completed: done=%v interrupted=%v", auditDone, interrupted)
						}
						if count <= 50 {
							if !out.Resumable || out.ChunkCount != 0 || out.TerminalRendered || rec.Body.Len() != 0 {
								t.Fatalf("uncommitted empty attempt must remain revocable: out=%+v wire=%q", out, rec.Body.String())
							}
							if err := gate.Discard(); err != nil {
								t.Fatalf("empty attempt cannot be discarded: %v", err)
							}
							if err := gw.Finish(); !errors.Is(err, ErrAttemptDiscarded) || rec.Body.Len() != 0 {
								t.Fatalf("discarded terminal escaped on Finish: err=%v wire=%q", err, rec.Body.String())
							}
						} else if out.Resumable || !out.TerminalRendered || !gate.TerminalRendered() ||
							strings.Count(rec.Body.String(), `"error":`) != 1 || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
							t.Fatalf("committed empty attempt needs one error terminal: out=%+v wire=%q", out, rec.Body.String())
						}
						if count > 50 {
							(&SurvivalCoordinator{Terminal: func(TaskDecision, bool) { t.Fatal("coordinator added a second terminal") }}).renderTerminal(TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, gate, true)
						}
					})
				}
			}
		}
	}
}

func TestReasoningOnlyDisconnectedCaptureIsFailed(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	writer := &flusherAfterFirstDisconnectWriter{header: http.Header{}}
	gate := NewAttemptCommitGate(t.Context(), ProtocolOpenAIChat, NewSerializedStreamWriter(writer), GateOptions{Mode: GateModeBuffered, HoldbackWindow: 10 * time.Second, HoldbackMaxChunks: 50})
	gw := NewGateWriterWithResponse(gate, writer)
	pc := NewPendingCapturer(64 * 1024)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(reasoningTerminalBody(60, false, true))), Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)}
	out := StreamChatWithPendingCapture(context.Background(), gw, resp, "minimax-m3", "minimax-m3", NewNormalizer(), nil, false, nil, pc)
	_, state, ok := pc.Snapshot()
	if !out.Interrupted || out.Resumable || out.TerminalRendered || gate.TerminalRendered() || !ok || state.Status != "failed" || writer.writes != 2 {
		t.Fatalf("disconnected reasoning-only turn became a replayable success: out=%+v state=%+v writes=%d", out, state, writer.writes)
	}
}

type reasoningReadFailure struct{}

func (reasoningReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestHeldReasoningReadFailuresKeepZeroVisibleChunks(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	for _, count := range []int{25, 30, 49, 50} {
		for _, broken := range []bool{false, true} {
			t.Run(fmt.Sprintf("chunks=%d/broken=%t", count, broken), func(t *testing.T) {
				rec := httptest.NewRecorder()
				gate := NewAttemptCommitGate(t.Context(), ProtocolOpenAIChat, NewSerializedStreamWriter(rec), GateOptions{Mode: GateModeBuffered, HoldbackWindow: 10 * time.Second, HoldbackMaxChunks: 50})
				gw := NewGateWriterWithResponse(gate, rec)
				var body io.Reader = strings.NewReader(strings.Repeat(reasoningProgressFrame, count))
				if broken {
					body = io.MultiReader(body, reasoningReadFailure{})
				}
				resp := &http.Response{Body: io.NopCloser(body), Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)}
				out := StreamChatWithPendingCapture(t.Context(), gw, resp, "minimax-m3", "minimax-m3", NewNormalizer(), nil, false, nil, nil)
				if !out.Interrupted || !out.Resumable || out.ChunkCount != 0 || gate.Committed() || rec.Body.Len() != 0 {
					t.Fatalf("held frames inflated client-visible failure count: out=%+v committed=%v wire=%q", out, gate.Committed(), rec.Body.String())
				}
			})
		}
	}
}

type reasoningTimedReader struct {
	io.Reader
	advance func()
}

func (r reasoningTimedReader) Read(p []byte) (int, error) {
	r.advance()
	return r.Reader.Read(p)
}

func TestReasoningDoneAfterHoldbackTimeWithoutFinish(t *testing.T) {
	withEarlyEmptyThreshold(t, 3)
	for _, count := range []int{8, 60} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var now atomic.Int64
			now.Store(time.Now().UnixNano())
			initial := now.Load()
			rec := httptest.NewRecorder()
			gate := NewAttemptCommitGate(t.Context(), ProtocolOpenAIChat, NewSerializedStreamWriter(rec), GateOptions{
				Mode: GateModeBuffered, HoldbackWindow: 10 * time.Second, HoldbackMaxChunks: 50,
				Now: func() time.Time { return time.Unix(0, now.Load()) },
			})
			gw := NewGateWriterWithResponse(gate, rec)
			body := io.MultiReader(strings.NewReader(strings.Repeat(reasoningProgressFrame, count)),
				reasoningTimedReader{strings.NewReader(reasoningProgressFrame + "data: [DONE]\n\n"), func() { now.Store(initial + int64(11*time.Second)) }})
			resp := &http.Response{Body: io.NopCloser(body), Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)}
			out := StreamChatWithPendingCapture(t.Context(), gw, resp, "minimax-m3", "minimax-m3", NewNormalizer(), nil, false, nil, nil)
			if !gate.Committed() || !out.Interrupted || out.Kind != errorsx.KindEmptyResponse || out.Resumable || !gate.TerminalRendered() ||
				strings.Count(rec.Body.String(), `"error":`) != 1 || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
				t.Fatalf("time boundary without finish returned an empty success: out=%+v wire=%q", out, rec.Body.String())
			}
		})
	}
}
