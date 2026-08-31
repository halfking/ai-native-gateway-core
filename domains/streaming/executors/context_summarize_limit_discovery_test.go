// 2026-09-01 regression tests for context-limit discovery in
// handleContextLengthRecovery. When the upstream rejection body carries the
// real context window ("This model's maximum context length is N tokens"),
// recovery must trim against the discovered limit rather than the configured
// one, which may be unset or stale.
package executors

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// largeChatBody builds an 11-message chat payload of 200-char blocks, large
// enough to trip the mechanical trim at any small context window.
func largeChatBody() []byte {
	long := strings.Repeat("a", 200)
	return []byte(`{"model":"m","messages":[
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
}

func newRecoveryParams() *ExecParams {
	return &ExecParams{
		R:              httptest.NewRequest("POST", "/v1/chat/completions", nil),
		OnNodeJump:     func(string) {},
		ClientProtocol: "openai-completions",
	}
}

// TestHandleContextLengthRecovery_DiscoversLimitWhenWindowUnset proves that a
// candidate with no configured ContextWindow can still be recovered: the limit
// is read out of the upstream error body, which unblocks the mechanical trim
// tier (it is skipped entirely when the window is unknown).
func TestHandleContextLengthRecovery_DiscoversLimitWhenWindowUnset(t *testing.T) {
	body := largeChatBody()
	sourceBody := append([]byte(nil), body...)
	st := &contextLengthRecoveryState{}
	e := &Executor{RecoveryCoord: nil}

	errBody := []byte(`{"error":{"message":"This model's maximum context length is 200 tokens. However, your messages resulted in 700 tokens."}}`)

	action := e.handleContextLengthRecovery(
		t.Context(),
		newRecoveryParams(),
		provider.Candidate{
			CredentialID: 22,
			ProviderID:   32,
			RawModel:     "minimax-m3",
			// ContextWindow deliberately nil: without discovery the
			// mechanical tier is skipped and no trim can happen.
		},
		&sourceBody,
		st,
		400,
		errBody,
	)

	if action != ctxLenRetry {
		t.Fatalf("expected ctxLenRetry after discovering the limit, got %v", action)
	}
	if len(sourceBody) >= len(body) {
		t.Fatalf("expected body to shrink; before=%d after=%d", len(body), len(sourceBody))
	}
	if st.lastStrategy != "mechanical_trim" {
		t.Errorf("expected mechanical_trim strategy, got %q", st.lastStrategy)
	}
}

// TestHandleContextLengthRecovery_DiscoveredLimitOverridesStaleConfig proves the
// discovered limit wins over a configured value that is wrong by more than 5%.
// The configured window here is large enough that trimming against it would be
// a no-op, so a shrunken body is proof the discovered limit was used.
func TestHandleContextLengthRecovery_DiscoveredLimitOverridesStaleConfig(t *testing.T) {
	body := largeChatBody()
	sourceBody := append([]byte(nil), body...)
	st := &contextLengthRecoveryState{}
	e := &Executor{RecoveryCoord: nil}

	staleWindow := 1_000_000
	errBody := []byte(`{"error":{"message":"This model's maximum context length is 200 tokens."}}`)

	action := e.handleContextLengthRecovery(
		t.Context(),
		newRecoveryParams(),
		provider.Candidate{
			CredentialID:  22,
			ProviderID:    32,
			RawModel:      "minimax-m3",
			ContextWindow: &staleWindow,
		},
		&sourceBody,
		st,
		400,
		errBody,
	)

	if action != ctxLenRetry {
		t.Fatalf("expected ctxLenRetry using the discovered limit, got %v", action)
	}
	if len(sourceBody) >= len(body) {
		t.Fatalf("stale config was used instead of the discovered limit; before=%d after=%d",
			len(body), len(sourceBody))
	}
	if staleWindow != 1_000_000 {
		t.Errorf("caller's ContextWindow was mutated: got %d", staleWindow)
	}
}

// TestHandleContextLengthRecovery_AggressiveTargetLeavesHeadroom proves the 4xx
// recovery path trims to the aggressive 60% target, not the default 85%. The
// 2026-09-01 Minimax-m3 case overshot the window by only 0.4%; trimming to just
// under the limit would have come back 4xx a second time.
func TestHandleContextLengthRecovery_AggressiveTargetLeavesHeadroom(t *testing.T) {
	body := largeChatBody()
	sourceBody := append([]byte(nil), body...)
	st := &contextLengthRecoveryState{}
	e := &Executor{RecoveryCoord: nil}

	const window = 700
	errBody := []byte(`{"error":{"message":"This model's maximum context length is 700 tokens."}}`)

	action := e.handleContextLengthRecovery(
		t.Context(),
		newRecoveryParams(),
		provider.Candidate{
			CredentialID: 22,
			ProviderID:   32,
			RawModel:     "minimax-m3",
		},
		&sourceBody,
		st,
		400,
		errBody,
	)

	if action != ctxLenRetry {
		t.Fatalf("expected ctxLenRetry, got %v", action)
	}
	// charsPerToken = 3.5 in transformation; mirror the estimate here rather
	// than importing the package-private helper.
	estimated := int(float64(len(sourceBody)) / 3.5)
	aggressiveLimit := int(float64(window) * 0.60)
	if estimated > aggressiveLimit {
		t.Errorf("body was not trimmed to the aggressive target: estimated=%d limit=%d(60%% of %d)",
			estimated, aggressiveLimit, window)
	}
}

// TestHandleContextLengthRecovery_IgnoresUnparseableErrorBody proves a body with
// no limit in it leaves the configured window in force, so the existing
// behaviour is preserved for providers that don't report their limit.
func TestHandleContextLengthRecovery_IgnoresUnparseableErrorBody(t *testing.T) {
	body := largeChatBody()
	sourceBody := append([]byte(nil), body...)
	st := &contextLengthRecoveryState{}
	e := &Executor{RecoveryCoord: nil}

	cw := 50
	action := e.handleContextLengthRecovery(
		t.Context(),
		newRecoveryParams(),
		provider.Candidate{
			CredentialID:  22,
			ProviderID:    32,
			RawModel:      "glm-5.2",
			ContextWindow: &cw,
		},
		&sourceBody,
		st,
		413,
		[]byte(`{"error":{"message":"Request Entity Too Large"}}`),
	)

	if action != ctxLenRetry {
		t.Fatalf("expected ctxLenRetry from the configured window, got %v", action)
	}
	if len(sourceBody) >= len(body) {
		t.Fatalf("expected body to shrink; before=%d after=%d", len(body), len(sourceBody))
	}
	if cw != 50 {
		t.Errorf("configured window was overwritten by an unparseable body: got %d", cw)
	}
}
