package executors

import (
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestIRScope_PerRequestAnomalyNotSuppressedByGlobalDedup — Step 4 audit
// gap fix. Production serialize call points must wrap each request in an
// ir.WithIRScope so per-request anomaly dedup is bounded to that request.
// Without the scope, the process-global reporterDed map suppresses the
// second request's identical (source, target, field) loss event for the
// entire process lifetime.
//
// This test drives finalizeOpenAIUpstreamBody twice with the SAME Anthropic
// body that carries a thinking.signature (a guaranteed ir_protocol_loss on
// the OpenAI Chat target). Both invocations represent independent requests;
// both loss events must surface to the captured reporter.
func TestIRScope_PerRequestAnomalyNotSuppressedByGlobalDedup(t *testing.T) {
	// Install a capturing reporter at the process level (the scope picks
	// this up via SetAsCurrent when r is nil).
	ir.ResetAnomalyReporter()
	var mu sync.Mutex
	var got []ir.AnomalyEvent
	prev := ir.SetAnomalyReporter(func(ev ir.AnomalyEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	t.Cleanup(func() {
		ir.SetAnomalyReporter(prev)
		ir.ResetAnomalyReporter()
		ir.UnsetIRScopedReporter()
	})

	adapter := &irAdapterForTest{}
	executor := &Executor{IR: adapter}

	// Anthropic body carrying a thinking.signature → loss on OpenAI target.
	anthropicBody := []byte(`{
		"model": "claude-3-5-sonnet",
		"max_tokens": 256,
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"step","signature":"sig-loss-1"},
				{"type":"text","text":"answer"}
			]}
		]
	}`)

	cand := provider.Candidate{
		ProviderID:   1,
		CredentialID: 100,
		Protocol:     "openai-completions",
		RawModel:     "gpt-4o",
	}

	// Two independent requests. Each must produce its own loss event.
	for i := 0; i < 2; i++ {
		params := &ExecParams{
			BodyBytes:      anthropicBody,
			ClientModel:    "claude-3-5-sonnet",
			OutboundModel:  "gpt-4o",
			ClientProtocol: "anthropic-messages",
		}
		if _, err := executor.finalizeOpenAIUpstreamBody(params, cand, anthropicBody); err != nil {
			t.Fatalf("request %d finalizeOpenAIUpstreamBody failed: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	lossCount := 0
	for _, ev := range got {
		if ev.AnomalyType == ir.AnomalyProtocolLoss &&
			ev.FieldPath == "messages[0].content[0].thinking.signature" &&
			ev.Reason == "loss" {
			lossCount++
		}
	}
	if lossCount != 2 {
		t.Fatalf("expected 2 thinking.signature loss events (one per request), got %d; events=%+v", lossCount, got)
	}
}
