package bg

import (
	"strings"
	"testing"
	"time"
)

// TestNodeProbeBackoffLadder pins the 5s/30s/60s/5m/1h/2h/24h
// sequence mandated by the spec — any change here must be a
// deliberate spec update.
func TestNodeProbeBackoffLadder(t *testing.T) {
	want := []time.Duration{
		5 * time.Second,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		24 * time.Hour,
	}
	if len(NodeProbeBackoffChain) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(NodeProbeBackoffChain), len(want))
	}
	for i, v := range want {
		if NodeProbeBackoffChain[i] != v {
			t.Fatalf("backoff[%d] = %v, want %v", i, NodeProbeBackoffChain[i], v)
		}
	}
}

func TestDirectProbeEndpointUsesAnthropicMessages(t *testing.T) {
	if got := directProbeEndpoint("https://apiclaude.cc", "anthropic-messages"); got != "https://apiclaude.cc/v1/messages" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := directProbeEndpoint("https://token.sensenova.cn/v1", "openai-completions"); got != "https://token.sensenova.cn/v1/chat/completions" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestDirectProbeBodyUsesAnthropicMessagesShape(t *testing.T) {
	body := directProbeBody("claude-sonnet-5", "anthropic-messages")
	if !strings.Contains(body, `"max_tokens":10`) || !strings.Contains(body, `"content":"ping"`) {
		t.Fatalf("unexpected anthropic probe body: %s", body)
	}
}

func TestNodeProbeMaxAttemptsRearmsFreshFailure(t *testing.T) {
	// Submit's SQL uses the cap as a reset boundary. Keep the invariant here
	// so a terminal backoff row cannot become a silent dead letter again.
	if nodeProbeMaxAttempts != 7 {
		t.Fatalf("unexpected probe cap: %d", nodeProbeMaxAttempts)
	}
}

// TestNodeProbeMaxAttempts ensures attempt=7 marks paused (the
// "max one day" cap from the spec).
func TestNodeProbeMaxAttempts(t *testing.T) {
	if nodeProbeMaxAttempts != 7 {
		t.Fatalf("expected 7, got %d", nodeProbeMaxAttempts)
	}
}

// TestNodeProbeInFlightWindow sanity-checks the dedup window.
func TestNodeProbeInFlightWindow(t *testing.T) {
	if nodeProbeInFlightWindow < 1*time.Minute {
		t.Fatalf("in-flight window too short: %v", nodeProbeInFlightWindow)
	}
}

// TestNodeProbeDedupHashStable ensures the same (cred, model) pair
// produces the same hash across calls (used for testing the dedup
// map key).
func TestNodeProbeDedupHashStable(t *testing.T) {
	a := nodeProbeDedupHash(42, "gpt-5.4")
	b := nodeProbeDedupHash(42, "gpt-5.4")
	if a != b {
		t.Fatalf("hash not stable: %q vs %q", a, b)
	}
	if nodeProbeDedupHash(43, "gpt-5.4") == a {
		t.Fatalf("hash should differ across cred IDs")
	}
}
