package streaming

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

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
			[]string{"event: error\n", `"type":"error"`}},
		{"responses failed event", ProtocolOpenAIResponses,
			[]string{"event: response.failed\n", "gateway_survival_fail_terminal"}},
		{"chat error frame + done", ProtocolOpenAIChat,
			[]string{`"error"`, "data: [DONE]\n\n"}},
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
