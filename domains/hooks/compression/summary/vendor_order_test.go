package summary

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
)

// Wave 3 B9 钉桩：同厂优先稳定重排 + Summarize 的链顺序生效。

func TestReorderSameVendorFirst(t *testing.T) {
	chain := []string{"glm-5.2", "claude-haiku-4", "minimax-m2", "claude-sonnet-4-5", "gpt-5"}
	got := ReorderSameVendorFirst(chain, "claude-opus-5")
	want := []string{"claude-haiku-4", "claude-sonnet-4-5", "glm-5.2", "minimax-m2", "gpt-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// Stable: glm is already first in the chain, so the reorder must be
	// the identity here (rest keeps its original relative order).
	got2 := ReorderSameVendorFirst(chain, "glm-4.5")
	if !reflect.DeepEqual(got2, chain) {
		t.Fatalf("glm-first reorder wrong: %v", got2)
	}
	// No same-vendor entries → unchanged.
	if got3 := ReorderSameVendorFirst(chain, "grok-4"); !reflect.DeepEqual(got3, chain) {
		t.Fatalf("no-hit reorder must be identity: %v", got3)
	}
	// Empty target / single-entry chain → unchanged.
	if got4 := ReorderSameVendorFirst(chain, ""); !reflect.DeepEqual(got4, chain) {
		t.Fatalf("empty target must be identity")
	}
	if got5 := ReorderSameVendorFirst([]string{"glm-5.2"}, "claude-opus-5"); len(got5) != 1 {
		t.Fatalf("single entry must be identity")
	}
	// Input slice must not be mutated.
	if chain[0] != "glm-5.2" || chain[1] != "claude-haiku-4" {
		t.Fatalf("input mutated: %v", chain)
	}
}

type recordingClient struct {
	tried   []string
	success string // model that "succeeds"; empty = all fail
}

func (r *recordingClient) Complete(ctx context.Context, prompt string, opts ...CompletionOption) (string, error) {
	cfg := CompletionConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	r.tried = append(r.tried, cfg.Model)
	if cfg.Model == r.success {
		return "ok summary", nil
	}
	return "", errors.New("downstream unavailable")
}

func TestSummarize_TriesSameVendorModelFirst(t *testing.T) {
	// Inject a known chain through the env source (ResolveModelConfig reads
	// it fresh on every call).
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "glm-5.2,claude-sonnet-4-5,minimax-m2")

	client := &recordingClient{success: "claude-sonnet-4-5"}
	s := NewSummarizer(client).WithTargetModelHint("claude-opus-5")
	out, err := s.Summarize(context.Background(), appconfig.SummaryDimensionProject, "conv")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if out != "ok summary" {
		t.Fatalf("unexpected summary %q", out)
	}
	// claude-sonnet-4-5 (same vendor as the claude-opus-5 target) must be
	// attempted FIRST even though the configured chain lists it second.
	if !reflect.DeepEqual(client.tried, []string{"claude-sonnet-4-5"}) {
		t.Fatalf("expected same-vendor-first attempts, got %v", client.tried)
	}

	// Without the hint, the configured order applies (glm first, which
	// fails before claude gets its turn).
	client2 := &recordingClient{success: "claude-sonnet-4-5"}
	s2 := NewSummarizer(client2)
	if _, err := s2.Summarize(context.Background(), appconfig.SummaryDimensionProject, "conv"); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !reflect.DeepEqual(client2.tried, []string{"glm-5.2", "claude-sonnet-4-5"}) {
		t.Fatalf("hint-less chain order wrong: %v", client2.tried)
	}
}
