package bg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
)

func responseValidationWorker(t *testing.T, server *httptest.Server, protocol string) *NodeProbeWorker {
	t.Helper()
	w := NewNodeProbeWorker(nil, nil, nil, "test-key", server.URL+"/v1", nil, 0)
	w.auditDB = nil // avoid a typed nil pool inside the optional audit interface
	w.client, w.probeClient = server.Client(), server.Client()
	w.resolveDirectTargetFn = func(context.Context, int, string) (string, string, string, string, int, error) {
		return "test-key", "test-model", server.URL, protocol, 7, nil
	}
	return w
}

func TestNodeProbeSyncReuseObservationDoesNotUseOldHealthyCache(t *testing.T) {
	for _, observation := range []bool{true, false} {
		t.Run(fmt.Sprintf("observation-%v", observation), func(t *testing.T) {
			w := NewNodeProbeWorker(nil, nil, nil, "", "", nil, 0)
			state := newFakeStateProvider()
			state.setAvailable(1, "test", true)
			w.stateProvider = state
			w.inFlight["1|test"] = struct{}{}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			done := make(chan bool, 1)
			go func() {
				done <- w.ProbeSync(ctx, []credentialstate.NoCandidatesCandidate{{CredentialID: 1, RawModel: "test"}}, "tenant-a", "reuse")
			}()
			deadline := time.Now().Add(time.Second)
			for {
				w.syncWaitersMu.Lock()
				attached := len(w.syncWaiters["1|test"]) > 0
				w.syncWaitersMu.Unlock()
				if attached {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("waiter did not attach")
				}
				time.Sleep(time.Millisecond)
			}
			if observation {
				w.markProbeObservation(1, "test")
			}
			w.finishProbe("1|test")
			if got := <-done; got == observation {
				t.Fatalf("recovery=%v observation=%v", got, observation)
			}
		})
	}
}

func TestNodeProbeSyncInconclusiveDoesNotWriteHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	w := responseValidationWorker(t, srv, "openai-completions")
	sink, observer := &recordingNodeProbeStateSink{}, &probeStateObserver{}
	w.stateSink, w.stateObserver = sink, observer
	var sideEffects int
	w.invalidateCandidateCache = func(int) { sideEffects++ }
	w.recordCircuitSuccess = func(int, int) { sideEffects++ }
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if w.ProbeSync(ctx, []credentialstate.NoCandidatesCandidate{{CredentialID: 1, RawModel: "test"}}, "tenant-a", "test") {
		t.Fatal("inconclusive probe reported recovery")
	}
	if sink.calls != 0 || sideEffects != 0 || len(observer.states) != 0 {
		t.Fatalf("health writes: sink=%d hooks=%d observed=%d", sink.calls, sideEffects, len(observer.states))
	}
}

func TestNodeProbeConfirmInconclusiveIsNotConfirmedFailure(t *testing.T) {
	for _, firstInconclusive := range []bool{true, false} {
		w := NewNodeProbeWorker(nil, nil, nil, "", "", nil, 0)
		calls := 0
		w.probeConfirmRound = func(context.Context, int, string) nodeProbeRoundResult {
			calls++
			return nodeProbeRoundResult{observationOnly: firstInconclusive || calls == 2, errCode: "probe_response_inconclusive"}
		}
		if w.ProbeConfirm(t.Context(), 1, "test") {
			t.Fatal("unverified response convicted the node")
		}
		if firstInconclusive && calls != 1 {
			t.Fatal("an uncertain first ping cannot justify another confirmation ping")
		}
	}
}

func TestProbeServiceInconclusiveDoesNotApplyOrRetry(t *testing.T) {
	service := newTestProbeService(nodeProbeRoundResult{observationOnly: true, errCode: "probe_response_inconclusive"}, gatewayProbeResult{},
		func(context.Context, probeOutcome) { t.Error("inconclusive outcome applied health") })
	service.gatewayRoundFn = func(context.Context, int, string) gatewayProbeResult {
		t.Error("inconclusive direct probe started gateway round")
		return gatewayProbeResult{}
	}
	sink := &recordingNodeProbeStateSink{}
	service.worker.stateSink = sink
	result, err := service.Run(t.Context(), probeServiceTask(4))
	if err != nil || result.Status != ProbeQueueSuccess || result.ReasonCode != "probe_response_inconclusive" || result.NextRunAt != nil || sink.calls != 0 {
		t.Fatalf("result=%+v err=%v sink=%d", result, err, sink.calls)
	}
	service.applyOutcome(t.Context(), probeOutcome{direct: nodeProbeRoundResult{observationOnly: true}})
	if !probeResultToRound("test", &ProbeResult{ObservationOnly: true, Status: ProbeStatusSkipped}).observationOnly {
		t.Fatal("executor observation verdict lost during conversion")
	}
}

func TestNodeProbeRunOneInconclusivePreservesHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	w := responseValidationWorker(t, srv, "openai-completions")
	w.loadStateFn = func(context.Context, int, string) (nodeProbeStateRow, error) {
		return nodeProbeStateRow{ConsecutiveFailures: 3}, nil
	}
	sink, observer := &recordingNodeProbeStateSink{}, &probeStateObserver{}
	w.stateSink, w.stateObserver = sink, observer
	w.invalidateCandidateCache = func(int) { t.Error("invalidated candidates") }
	w.recordCircuitSuccess = func(int, int) { t.Error("closed circuit") }
	w.modelQualityTrigger = func(int, string, int) { t.Error("advanced failure ladder") }
	if err := w.runOne(t.Context(), 1, "test", "request_failure", nodeProbeTrigger{tenantID: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	if sink.calls != 0 || len(observer.states) != 0 {
		t.Fatal("unverified observation wrote node authority")
	}
}

func TestNodeProbeResponsesFallbackValidatesChat(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          int
		body            string
		ok, observation bool
	}{
		{"empty", 200, `{}`, false, true},
		{"error-envelope", 200, `{"error":{"message":"busy"}}`, false, true},
		{"text", 200, `{"choices":[{"message":{"content":"pong"}}]}`, true, false},
		{"long-text", 200, `{"choices":[{"message":{"content":"` + strings.Repeat("p", 2048) + `"}}]}`, true, false},
		{"audio", 200, `{"choices":[{"message":{"audio":{"data":"YWJj"}}}]}`, true, false},
		{"auth", 401, `{"error":{"message":"invalid key"}}`, false, false},
		{"model", 404, `{"error":{"message":"model not found"}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/responses") {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"message":"provider does not support the Responses API"}}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			w := responseValidationWorker(t, srv, "openai-responses")
			r := w.probeDirect(t.Context(), 1, "test")
			if r.ok != tc.ok || r.observationOnly != tc.observation || !r.chatFallback || r.supportsResponses == nil || *r.supportsResponses {
				t.Fatalf("fallback=%+v", r)
			}
			if !tc.ok && !tc.observation && r.httpStatus != tc.status {
				t.Fatalf("fallback HTTP evidence lost: %+v", r)
			}
		})
	}
}

func TestNodeProbeUnreadableBodyPreservesHTTPFailure(t *testing.T) {
	for _, protocol := range []string{"openai-completions", "openai-responses", "anthropic-messages"} {
		for _, status := range []int{200, 401, 403, 404, 503} {
			for _, broken := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%d/broken-%v", protocol, status, broken), func(t *testing.T) {
					client := &http.Client{Transport: credentialProbeTestTransport(func(*http.Request) (*http.Response, error) {
						prefix := map[string]string{
							"openai-completions": `{"choices":[{"message":{"content":"pong"}}]}`,
							"openai-responses":   `{"output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`,
							"anthropic-messages": `{"type":"message","content":[{"type":"text","text":"pong"}]}`,
						}[protocol]
						var body io.Reader = strings.NewReader(prefix + strings.Repeat(" ", credentialProbeBodyLimit+1))
						if broken {
							body = io.MultiReader(strings.NewReader(prefix), credentialProbeBrokenReader{})
						}
						return &http.Response{StatusCode: status, Body: io.NopCloser(body), Header: make(http.Header)}, nil
					})}
					w := NewNodeProbeWorker(nil, nil, nil, "test-key", "https://provider.invalid", nil, 0)
					w.client, w.probeClient = client, client
					w.resolveDirectTargetFn = func(context.Context, int, string) (string, string, string, string, int, error) {
						return "test-key", "test-model", "https://provider.invalid", protocol, 7, nil
					}
					for _, r := range []nodeProbeRoundResult{w.probeDirect(t.Context(), 1, "test"), w.probeGateway(t.Context(), 1, "test")} {
						if r.ok || r.observationOnly != (status == 200) || r.httpStatus != status {
							t.Fatalf("HTTP evidence=%+v", r)
						}
						if status != 200 && r.errCode != fmt.Sprintf("http_%d", status) {
							t.Fatalf("code=%s", r.errCode)
						}
					}
				})
			}
		}
	}
}

func TestNodeProbeResponseDiagnosticsAreSanitized(t *testing.T) {
	secret := "sk-testabcdefghijklmnopqrstuvwxyzz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Bearer `+secret+`"}}]}`)
	}))
	defer srv.Close()
	w := responseValidationWorker(t, srv, "openai-completions")
	for _, r := range []nodeProbeRoundResult{w.probeDirect(t.Context(), 1, "test"), w.probeGateway(t.Context(), 1, "test")} {
		if !r.ok || strings.Contains(r.responseBody+r.errDetail, secret) || len(r.responseBody) > 512 {
			t.Fatalf("diagnostics=%+v", r)
		}
	}
}

func TestNodeProbeNativeProtocolEvidenceDoesNotRestoreHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`)
	}))
	defer srv.Close()
	w := responseValidationWorker(t, srv, "openai-responses")
	sink := &recordingResponsesCapabilitySink{}
	w.SetResponsesCapabilitySink(sink)
	r := w.probeDirect(t.Context(), 1, "reasoning-model")
	if r.ok || !r.observationOnly || r.supportsResponses == nil || !*r.supportsResponses || len(sink.records) != 1 || !sink.records[0].supported {
		t.Fatalf("protocol evidence must survive without recovery: %+v records=%+v", r, sink.records)
	}
}

func TestNodeProbeRequiresSemanticResponse(t *testing.T) {
	positive := map[string]string{
		"openai-completions": `{"choices":[{"message":{"content":"pong"}}]}`,
		"anthropic-messages": `{"type":"message","content":[{"type":"text","text":"pong"}]}`,
		"openai-responses":   `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`,
	}
	negative := []string{"", "<html>busy</html>", `{}`, `{"error":{"message":"busy"}}`,
		`{"choices":[{"message":{"reasoning_content":"thinking"},"finish_reason":"length"}]}`,
		`{"content":[{"type":"thinking","thinking":"thinking"}],"stop_reason":"max_tokens"}`,
		`{"status":"failed","output":[{"type":"message","content":[{"type":"output_text","text":"error prose"}]}]}`,
		`{"choices":[{"message":{"audio":{}}}]}`,
		`{"choices":[{"message":{"audio":{"data":"  ","transcript":" "}}}]}`,
		`{"error":{"message":"busy"},"choices":[{"message":{"audio":{"data":"YWJj"}}}]}`,
	}
	for protocol, valid := range positive {
		cases := append(append([]string{}, negative...), valid, strings.Replace(valid, "pong", strings.Repeat("p", 2048), 1))
		for i, body := range cases {
			t.Run(fmt.Sprintf("%s/%d", protocol, i), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
				defer srv.Close()
				worker := responseValidationWorker(t, srv, protocol)
				r := worker.probeDirect(t.Context(), 1, "test")
				want := i >= len(negative)
				if r.ok != want || (!want && r.errCode != "probe_response_inconclusive") {
					t.Fatalf("direct ok=%v want=%v code=%s", r.ok, want, r.errCode)
				}
				if protocol == "openai-completions" {
					gw := worker.probeGateway(t.Context(), 1, "test")
					if gw.ok != want || (!want && gw.errCode != "probe_response_inconclusive") {
						t.Fatalf("gateway=%+v", gw)
					}
				}
			})
		}
	}
}

func TestNodeProbeAcceptsAudioOutput(t *testing.T) {
	for _, audio := range []string{`{"data":"YWJj"}`, `{"transcript":"hello"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"choices":[{"message":{"audio":`+audio+`}}]}`)
		}))
		worker := responseValidationWorker(t, srv, "openai-completions")
		if !worker.probeDirect(t.Context(), 1, "tts-model").ok || !worker.probeGateway(t.Context(), 1, "tts-model").ok {
			t.Fatal("audio payload must remain positive evidence")
		}
		srv.Close()
	}
}
