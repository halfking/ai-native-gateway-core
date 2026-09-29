package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// TestVapeurShape_ProductionStackSegmented runs the production-fidelity stack
// against a segmented (network-like) vapeur-shaped upstream, 10 iterations per
// shape — the production failure rate was ~50% on fast tiny streams.
func TestVapeurShape_ProductionStackSegmented(t *testing.T) {
	shapes := []struct {
		name   string
		events []string
	}{
		{"no_role_with_done", []string{vC, vF, vDn}},
		{"no_role_no_done", []string{vC, vF}},
		{"role_done", []string{vR, vC, vF, vDn}},
	}
	for _, shape := range shapes {
		for i := 0; i < 10; i++ {
			resp := vapeurUpstreamSegmented(t, shape.events, 5*time.Millisecond)
			body, oc := runVapeurShapeFull(t, resp)
			if !strings.Contains(body, "data: [DONE]") && shape.events[len(shape.events)-1] == vDn {
				t.Errorf("[%s iter%d] wire missing data: [DONE] (outcome=%+v)\nwire:\n%s", shape.name, i, oc, body)
			}
			if oc.Interrupted {
				t.Errorf("[%s iter%d] outcome unexpectedly interrupted: %+v", shape.name, i, oc)
			}
		}
	}
}

// vapeur_tail_truncation_repro_test.go — R-vapeur3 repro harness.
//
// Production evidence (2026-09-30, local 8782 build 2343, wire-captured via
// TCP recording proxy): chat streams proxied to vapeur (provider 36, cred 126)
// intermittently lose their tail on the wire — client saw content+finish but
// never `data: [DONE]` — while the audit recorded success=true with 4
// chunkCount. Static analysis of StreamChatWithPendingCaptureAndDiagnosticsWithVendor
// shows several candidate loss points (GateWriter.pending stranding, empty-stream
// gate line shapes, serialized writer layering). This harness drives the REAL
// bridge with production-identical writer wrapping (pre-stream keepalive
// StreamSession → bridge → wrapAttemptWriter gate) over four upstream frame
// shapes and asserts every data frame AND the [DONE] sentinel reach the wire.

func vapeurUpstream(t *testing.T, raw string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(raw))
	}))
	t.Cleanup(srv.Close)
	resp, err := (&http.Client{}).Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("upstream fetch: %v", err)
	}
	return resp
}

// vapeurUpstreamSegmented mimics real-network delivery: each SSE event is a
// separate Write + flush with a small delay, like distinct TCP segments.
func vapeurUpstreamSegmented(t *testing.T, events []string, delay time.Duration) *http.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(delay)
		}
	}))
	t.Cleanup(srv.Close)
	resp, err := (&http.Client{}).Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("upstream fetch: %v", err)
	}
	return resp
}

// runVapeurShapeFull mirrors the production writer stack: pre-stream keepalive
// session → interceptingStreamWriter (active chain object) → bridge (which adds
// monitor + attempt gate). capture and pc are non-nil as in the session path.
func runVapeurShapeFull(t *testing.T, resp *http.Response) (body string, outcome StreamOutcome) {
	t.Helper()
	resp.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	ctx := context.Background()
	psk, ok := startPreStreamKeepalive(ctx, rec, time.Second, "repro-full")
	if !ok {
		t.Fatal("keepalive not started")
	}
	defer psk.stop()
	w := psk.Writer()
	// Production: the handler wraps the session writer in the interceptor
	// writer whenever a chain object exists (inert chain mirrors the local
	// default where goal/audit/compliance hooks all pass through).
	w = newInterceptingStreamWriter(w, response.NewInterceptorChain(), ctx, response.StreamMeta{
		SessionID: "gw_repro", RequestID: "repro-full", TenantID: "default", ClientProtocol: "openai-chat",
	})
	capture := audit.NewStreamCapture()
	pc := NewPendingCapturer(0)
	outcome = StreamChatWithPendingCaptureAndDiagnosticsWithVendor(
		ctx, w, resp, "gpt-5.6-terra", "gpt-5.6-terra",
		NewNormalizer(), capture, false, nil, "vapeur", pc, nil)
	return rec.Body.String(), outcome
}

const (
	vC  = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"pong"}}]}` + "\n\n"
	vF  = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":4,"total_tokens":17}}` + "\n\n"
	vU  = `data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":4,"total_tokens":17}}` + "\n\n"
	vR  = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n"
	vDn = "data: [DONE]\n" // vapeur's real wire shape (raw-log proven 2026-09-30): NO trailing blank line before close
)

func runVapeurShape(t *testing.T, name, raw string) (body string, outcome StreamOutcome) {
	t.Helper()
	resp := vapeurUpstream(t, raw)
	rec := httptest.NewRecorder()
	ctx := context.Background()
	// Production writer wrapping: the chat handler starts the pre-stream
	// keepalive BEFORE the bridge runs; the bridge receives psk.Writer().
	psk, ok := startPreStreamKeepalive(ctx, rec, time.Second, "repro-"+name)
	if !ok {
		t.Fatal("keepalive not started")
	}
	defer psk.stop()
	outcome = StreamChatWithPendingCapture(ctx, psk.Writer(), resp, "gpt-5.6-terra", "gpt-5.6-terra",
		NewNormalizer(), nil, false, nil, nil)
	return rec.Body.String(), outcome
}

func assertTailComplete(t *testing.T, name, body string, outcome StreamOutcome, wantDataFrames int) {
	t.Helper()
	if n := strings.Count(body, "data: {"); n != wantDataFrames {
		t.Errorf("[%s] wire has %d data frames, want %d\nwire:\n%s", name, n, wantDataFrames, body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("[%s] wire missing data: [DONE] (outcome=%+v)\nwire:\n%s", name, outcome, body)
	}
	if outcome.Interrupted {
		t.Errorf("[%s] outcome unexpectedly interrupted: %+v", name, outcome)
	}
}

func TestVapeurShape_RoleContentFinishUsageDONE(t *testing.T) {
	body, oc := runVapeurShape(t, "full", vR+vC+vF+vU+vDn)
	assertTailComplete(t, "full", body, oc, 4)
}

func TestVapeurShape_ContentFinishUsageDONE(t *testing.T) {
	body, oc := runVapeurShape(t, "no_role", vC+vF+vU+vDn)
	assertTailComplete(t, "no_role", body, oc, 3)
}

func TestVapeurShape_ContentFinishUsageInlineDONE(t *testing.T) {
	body, oc := runVapeurShape(t, "usage_inline", vC+vF+vDn)
	assertTailComplete(t, "usage_inline", body, oc, 2)
}

func TestVapeurShape_ContentFinishNoDONE(t *testing.T) {
	body, oc := runVapeurShape(t, "no_done", vC+vF)
	assertTailComplete(t, "no_done", body, oc, 2)
}

var _ = fmt.Sprintf
