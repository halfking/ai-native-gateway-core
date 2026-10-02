package handoff

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// shippedAbsoluteThreshold mirrors the shipped default of the
// settings key `handoff.absolute_threshold` (settings/handoff_specs.go).
// It is duplicated here on purpose: the handoff domain deliberately does not
// import the settings package, so the spec-side bound is asserted separately
// in settings/handoff_1m_contract_test.go. If the shipped default ever moves
// to or above 1M, that settings test fails loudly and this one is updated in
// the same change.
const shippedAbsoluteThreshold = 300_000

// oneMillionContext is the contract this file pins: a session whose cumulative
// token count has reached 1M must hand off, because the absolute threshold is a
// backstop for contexts that no percentage rule can model (the model window
// may be unknown or enormous).
const oneMillionContext = 1_000_000

func newOneMillionTriggerHook(t *testing.T, store *memoryStore, absoluteThreshold int) *TriggerHook {
	t.Helper()
	return NewTriggerHook(TriggerConfig{
		Enabled:             true,
		TriggerMode:         TriggerModeAuto,
		AbsoluteThreshold:   absoluteThreshold,
		PercentageThreshold: 0.8,
		MessageThreshold:    0,
		MinMessages:         5,
		SkillName:           "handoff",
		SummaryEngine:       SummaryRule,
		CooldownSeconds:     60,
		MaxPerSession:       5,
		SettingsGetter:      &stubSettings{},
	}, store)
}

func oneMillionRequest() *response.InterceptRequest {
	return &response.InterceptRequest{
		SessionID:     "sess-1m",
		TenantID:      "t-1",
		ClientModel:   "gpt-4o",
		TokensUsed:    oneMillionContext,
		ContextWindow: 4_000_000, // oversized window: 1M is only 25% of it
		MessageCount:  200,
	}
}

// TestAbsoluteThreshold_OneMillionContextHandsOff pins the "1M handoff
// contract": under the shipped default, a 1M-token session hands off and the
// recorded reason is the absolute-threshold one. The oversized ContextWindow
// is deliberate — at 25% of the window the percentage rule cannot fire, so
// this test fails if the absolute backstop stops working.
func TestAbsoluteThreshold_OneMillionContextHandsOff(t *testing.T) {
	store := &memoryStore{tokenCount: oneMillionContext, msgCount: 200, lastActivity: time.Now()}
	h := newOneMillionTriggerHook(t, store, shippedAbsoluteThreshold)

	result, err := h.InterceptNonStream(context.Background(), oneMillionRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatalf("1M-token session must hand off under the shipped absolute threshold (%d); "+
			"got no handoff (TokensUsed=%d, ContextWindow=%d)",
			shippedAbsoluteThreshold, oneMillionContext, oneMillionRequest().ContextWindow)
	}
	if result.Action != "handoff" {
		t.Errorf("expected action=handoff, got %q", result.Action)
	}
	if !strings.Contains(string(result.InjectFollowUp), "/handoff") {
		t.Errorf("expected follow-up to mention /handoff skill, got %q", string(result.InjectFollowUp))
	}
	if len(store.rows) != 1 {
		t.Fatalf("expected 1 recorded handoff, got %d", len(store.rows))
	}
	if !strings.HasPrefix(store.rows[0].TriggerReason, "absolute_threshold") {
		t.Errorf("expected trigger_reason=absolute_threshold:*, got %q", store.rows[0].TriggerReason)
	}
}

// TestAbsoluteThreshold_OneMillionIsGatedByTheThreshold is the discriminating
// half of the contract. Without it, TestAbsoluteThreshold_OneMillionContextHandsOff
// would still pass if the hook handed off unconditionally. Raising the
// threshold above 1M must suppress the handoff for the same session.
func TestAbsoluteThreshold_OneMillionIsGatedByTheThreshold(t *testing.T) {
	store := &memoryStore{tokenCount: oneMillionContext, msgCount: 200, lastActivity: time.Now()}
	h := newOneMillionTriggerHook(t, store, 1_500_000)

	result, err := h.InterceptNonStream(context.Background(), oneMillionRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("1M tokens must NOT hand off when the threshold is raised to 1.5M; got action=%q", result.Action)
	}
	if len(store.rows) != 0 {
		t.Fatalf("expected no recorded handoff, got %d", len(store.rows))
	}
}

// TestAbsoluteThreshold_BoundaryIsInclusive pins that the gate fires *at* the
// threshold, not only above it. The spec wording is "当会话累计 token 数达到此
// 阈值时触发" — reaching the threshold triggers. A 1M-only test cannot tell
// `>= thr` from `> 2*thr`, because both hold at 1M with the shipped 300K
// default; only the boundary pair discriminates them.
func TestAbsoluteThreshold_BoundaryIsInclusive(t *testing.T) {
	cases := []struct {
		name        string
		tokens      int
		wantHandoff bool
	}{
		{name: "one_below_threshold", tokens: shippedAbsoluteThreshold - 1, wantHandoff: false},
		{name: "exactly_at_threshold", tokens: shippedAbsoluteThreshold, wantHandoff: true},
		{name: "one_above_threshold", tokens: shippedAbsoluteThreshold + 1, wantHandoff: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{tokenCount: tc.tokens, msgCount: 200, lastActivity: time.Now()}
			h := newOneMillionTriggerHook(t, store, shippedAbsoluteThreshold)

			req := oneMillionRequest()
			req.SessionID = "sess-boundary"
			req.TokensUsed = tc.tokens

			result, err := h.InterceptNonStream(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := result != nil
			if got != tc.wantHandoff {
				t.Fatalf("TokensUsed=%d (threshold=%d): handoff=%v, want %v",
					tc.tokens, shippedAbsoluteThreshold, got, tc.wantHandoff)
			}
			if got && !strings.HasPrefix(store.rows[0].TriggerReason, "absolute_threshold") {
				t.Errorf("expected absolute_threshold reason, got %q", store.rows[0].TriggerReason)
			}
		})
	}
}

// TestAbsoluteThreshold_ZeroDisablesAbsoluteGate pins the documented "0 means
// disabled" branch of trigger_hook.go: with the absolute threshold set to
// 0 the gate is skipped entirely, and a 1M-token session is then governed only
// by the percentage rule (which cannot fire at 25% of a 4M window).
func TestAbsoluteThreshold_ZeroDisablesAbsoluteGate(t *testing.T) {
	store := &memoryStore{tokenCount: oneMillionContext, msgCount: 200, lastActivity: time.Now()}
	h := newOneMillionTriggerHook(t, store, 0)

	result, err := h.InterceptNonStream(context.Background(), oneMillionRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("absolute threshold 0 must disable the gate; got action=%q", result.Action)
	}
	if len(store.rows) != 0 {
		t.Fatalf("expected no recorded handoff, got %d", len(store.rows))
	}
}
