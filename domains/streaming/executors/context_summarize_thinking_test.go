// 2026-08-12 P0 regression test for the thinking-event emit on context-length
// recovery. The user-visible fix: when the gateway transparently compresses
// a too-long conversation before retrying, the client (Cursor / Claude Code /
// opencode) must see a thinking SSE event — not just a silent retry that
// culminates in a 4xx the user can't explain.
//
// This test exercises the legacy mechanical-trim branch of
// handleContextLengthRecovery (RecoveryCoord == nil → skip the smart-window
// path → fall through to the mechanical trim path) and asserts that
// params.OnNodeJump fires with a "context length exceeds model window"
// prefix.
package executors

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestHandleContextLengthRecovery_EmitsThinkingOnMechanicalTrim proves the
// 2026-08-12 fix: mechanical trim path notifies the client via OnNodeJump
// before retrying. Without this, the user only sees a generic 4xx from
// upstream and cannot tell the gateway is helping.
func TestHandleContextLengthRecovery_EmitsThinkingOnMechanicalTrim(t *testing.T) {
	// 1) Build a body large enough to trip CompressMessagesIfNeeded.
	//    contextWindow=50 (tokens) → soft limit ~42 tokens → 11-message
	//    payload of 200-char blocks is well over budget and must trim.
	long := strings.Repeat("a", 200)
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"},
		{"role":"user","content":"` + long + `"},
		{"role":"assistant","content":"` + long + `"}
	]}`)

	// 2) Wire OnNodeJump to capture the thinking message.
	var thinkingMsg string
	params := &ExecParams{
		R: httptest.NewRequest("POST", "/v1/chat/completions", nil),
		OnNodeJump: func(msg string) {
			thinkingMsg = msg
		},
		ClientProtocol: "openai-completions",
	}

	// 3) Construct the Executor with NO RecoveryCoord → legacy path →
	//    mechanical trim is the first fallback that succeeds.
	cw := 50
	e := &Executor{RecoveryCoord: nil}

	// 4) Drive the recovery state machine.
	sourceBody := append([]byte(nil), body...)
	st := &contextLengthRecoveryState{}
	action := e.handleContextLengthRecovery(
		t.Context(),
		params,
		provider.Candidate{
			CredentialID:  22, // cred 22 = zhipu-roocode-v2 (illustrative)
			ProviderID:    32,
			RawModel:      "glm-5.2",
			ContextWindow: &cw,
		},
		&sourceBody,
		st,
		413, // upstream returned 413 → handleContextLengthRecovery is the
		// right path. (We don't actually send the request; the function
		// only uses `status` for log lines.)
	)

	// 5) Assert: the mechanical trim succeeded, the body shrank, AND
	//    the client got a thinking event BEFORE the retry.
	if action != ctxLenRetry {
		t.Fatalf("expected ctxLenRetry (mechanical trim should fire), got %v", action)
	}
	if len(sourceBody) >= len(body) {
		t.Fatalf("expected sourceBody to shrink; before=%d after=%d", len(body), len(sourceBody))
	}
	if thinkingMsg == "" {
		t.Fatal("expected OnNodeJump to be called with a non-empty thinking message, got empty string")
	}
	wantPrefixes := []string{
		"context length exceeds model window",
		"mechanical_trim",
	}
	for _, want := range wantPrefixes {
		if !strings.Contains(thinkingMsg, want) {
			t.Errorf("thinking message missing %q; got=%q", want, thinkingMsg)
		}
	}
	t.Logf("thinking message emitted: %q", thinkingMsg)
}
