package streaming

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/retryowner"
)

// SR-07 wiring unit tests: the survival branch is glue over tested
// components (gate/coordinator/ExecuteAttempt); what needs pinning here is
// the protocol mapping, the terminal frame shapes, and that the handler
// stays inert until armed.

func TestSurvivalClientProtocolMapping(t *testing.T) {
	cases := []struct {
		path string
		want ClientProtocol
	}{
		{"/v1/chat/completions", ProtocolOpenAIChat},
		{"/v1/messages", ProtocolAnthropic},
		{"/v1/responses", ProtocolOpenAIResponses},
		{"", ProtocolOpenAIChat},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.URL.Path = tc.path
		if got := survivalClientProtocol(r); got != tc.want {
			t.Errorf("survivalClientProtocol(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if got := survivalClientProtocol(nil); got != ProtocolOpenAIChat {
		t.Errorf("nil request protocol = %v, want openai_chat", got)
	}
}

func TestRenderSurvivalTerminalFramesPerProtocol(t *testing.T) {
	cases := []struct {
		name     string
		protocol ClientProtocol
		contains []string
	}{
		{"anthropic error event", ProtocolAnthropic,
			[]string{"event: error\n", `"type":"error"`, `"reason":"terminal_candidate"`, `"retryable":false`}},
		{"responses failed event", ProtocolOpenAIResponses,
			[]string{"event: response.failed\n", "gateway_survival_fail_terminal", `"reason":"terminal_candidate"`}},
		{"chat error frame + done", ProtocolOpenAIChat,
			[]string{`"error"`, "data: [DONE]\n\n", `"reason":"terminal_candidate"`, `"retryable":false`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &trackingFlusher{}
			sw := NewSerializedStreamWriter(f)
			renderSurvivalTerminal(sw, tc.protocol,
				TaskDecision{Action: TaskActionFailTerminal, Reason: "terminal_candidate"}, false)
			out := f.buf.String()
			for _, want := range tc.contains {
				if !strings.Contains(out, want) {
					t.Fatalf("terminal frame %q missing %q in %q", tc.name, want, out)
				}
			}
			if !strings.HasSuffix(out, "\n\n") {
				t.Fatalf("terminal frame must end with a frame boundary: %q", out)
			}
		})
	}
}

func TestRenderSurvivalTerminalNativeSSEIsExactlyOneTerminal(t *testing.T) {
	cases := []struct {
		name     string
		protocol ClientProtocol
		terminal string
	}{
		{"messages", ProtocolAnthropic, "event: error\n"},
		{"responses", ProtocolOpenAIResponses, "event: response.failed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &trackingFlusher{}
			sw := NewSerializedStreamWriter(f)
			renderSurvivalTerminal(sw, tc.protocol,
				TaskDecision{Action: TaskActionFailTerminal, Reason: "deadline_exceeded"}, false)
			out := f.buf.String()
			if strings.Count(out, tc.terminal) != 1 {
				t.Fatalf("terminal marker count = %d, want 1: %q", strings.Count(out, tc.terminal), out)
			}
			if strings.Contains(out, "data: {") && strings.Contains(out, "data: [DONE]") {
				t.Fatal("native endpoint terminal must not append OpenAI chat JSON or DONE")
			}
		})
	}
}

func TestRenderSurvivalTerminalEscapesReasonJSON(t *testing.T) {
	f := &trackingFlusher{}
	sw := NewSerializedStreamWriter(f)
	reason := `provider returned "bad"\nresponse`
	renderSurvivalTerminal(sw, ProtocolOpenAIChat,
		TaskDecision{Action: TaskActionFailTerminal, Reason: reason}, false)
	if !strings.Contains(f.buf.String(), `gateway request survival ended: provider returned \"bad\"\\nresponse`) {
		t.Fatalf("terminal reason was not JSON escaped: %q", f.buf.String())
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(f.buf.String(), "data: "), "\n\ndata: [DONE]\n\n")
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("terminal payload is invalid JSON: %v (%q)", err, payload)
	}
	if decoded.Error.Message != "gateway request survival ended: "+reason {
		t.Fatalf("decoded reason = %q", decoded.Error.Message)
	}
}

// The handler branch must stay inert until SetRequestSurvival arms it —
// nil gate never routes a request into the survival path.
func TestSetRequestSurvivalArmsAndDisarms(t *testing.T) {
	h := &ChatHandler{}
	if h.survivalTenantAllowed != nil {
		t.Fatal("zero-value handler must have survival disabled")
	}
	h.SetRequestSurvival(func(tenantID string) bool { return tenantID == "t1" },
		SurvivalOptions{Deadline: 60 * 1e9}) // 60s
	if h.survivalTenantAllowed == nil || !h.survivalTenantAllowed("t1") {
		t.Fatal("armed gate must honor the allowlist closure")
	}
	if h.survivalTenantAllowed("t2") {
		t.Fatal("allowlist must reject other tenants")
	}
}

// The frozen request context must carry the survival owner so the
// streamretry wrapper (if mounted despite the startup check) steps aside.
func TestRunSurvivalFreezesOwner(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	frozen := retryowner.FreezeOwner(r.Context(), retryowner.Survival)
	if retryowner.OwnerFrom(frozen) != retryowner.Survival {
		t.Fatal("frozen ctx must report the survival owner")
	}
	fr := r.WithContext(frozen)
	if retryowner.OwnerFrom(fr.Context()) != retryowner.Survival {
		t.Fatal("WithContext must preserve the frozen owner")
	}
}

// 2026-09-23 (strategy fix): resume_blocked only ever fires for retryable
// failure kinds with committed output (errorsx.DecideNextAction). The
// in-connection transparent retry stays blocked, but the envelope must tell
// agent clients the failure class itself is retryable so their
// discard-and-regenerate turn machinery engages instead of hard-failing.
func TestRenderSurvivalTerminalResumeBlockedIsClientRetryable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol ClientProtocol
		marker   string
	}{
		{"chat", ProtocolOpenAIChat, `"code":"gateway_survival_resume_blocked"`},
		{"anthropic", ProtocolAnthropic, `"code":"gateway_survival_resume_blocked"`},
		{"responses", ProtocolOpenAIResponses, `"code":"gateway_survival_resume_blocked"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &trackingFlusher{}
			sw := NewSerializedStreamWriter(f)
			renderSurvivalTerminal(sw, tc.protocol,
				TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}, true)
			out := f.buf.String()
			if !strings.Contains(out, tc.marker) {
				t.Fatalf("resume_blocked envelope missing %q: %q", tc.marker, out)
			}
			if !strings.Contains(out, `"reason":"committed_output"`) {
				t.Fatalf("envelope missing committed_output reason: %q", out)
			}
			if !strings.Contains(out, `"retryable":true`) {
				t.Fatalf("resume_blocked envelope must be client-retryable: %q", out)
			}
		})
	}
}

// fail_terminal keeps retryable=false — those kinds are genuinely terminal
// (model_not_found, content_filter, ...) and a client retry would fail the
// same way.
func TestRenderSurvivalTerminalFailTerminalStaysNonRetryable(t *testing.T) {
	f := &trackingFlusher{}
	sw := NewSerializedStreamWriter(f)
	renderSurvivalTerminal(sw, ProtocolOpenAIChat,
		TaskDecision{Action: TaskActionFailTerminal, Reason: "model_not_found"}, true)
	out := f.buf.String()
	if !strings.Contains(out, `"retryable":false`) {
		t.Fatalf("fail_terminal envelope must stay non-retryable: %q", out)
	}
}

// 2026-09-23 critique round: both the survival envelope path and the
// dispatch-path §11.6 wrap must funnel into the handler's single
// blackhole guard via errorsx.ErrProtocolTerminalRendered (the survival
// sentinel wraps it; ExecuteError.LastErr is inspected explicitly because
// ExecuteError has no Unwrap).

// 2026-09-23 critique round: both the survival envelope path and the
// dispatch-path §11.6 wrap must funnel into the handler's single blackhole
// guard via errorsx.ErrProtocolTerminalRendered (the survival sentinel wraps
// it; ExecuteError.LastErr is inspected explicitly because ExecuteError has
// no Unwrap).
func TestExecTerminalRenderedMatchesBothWrapStyles(t *testing.T) {
	dispatchWrap := fmt.Errorf("%w (%w)", errors.New("stream_interrupted: eof_without_done"), errorsx.ErrProtocolTerminalRendered)
	if !execTerminalRendered(dispatchWrap) {
		t.Fatal("dispatch-style wrap must match")
	}
	ee := &executors.ExecuteError{Exhausted: true, LastErr: dispatchWrap}
	if !execTerminalRendered(ee) {
		t.Fatal("ExecuteError.LastErr wrap must match")
	}
	if !execTerminalRendered(errSurvivalTerminalRendered) {
		t.Fatal("survival sentinel must match (it wraps the errorsx sentinel)")
	}
	if execTerminalRendered(errors.New("ordinary failure")) {
		t.Fatal("ordinary errors must not match")
	}
	if execTerminalRendered(&executors.ExecuteError{LastErr: errors.New("x")}) {
		t.Fatal("ExecuteError without the sentinel must not match")
	}
	if execTerminalRendered(nil) {
		t.Fatal("nil must not match")
	}
}
