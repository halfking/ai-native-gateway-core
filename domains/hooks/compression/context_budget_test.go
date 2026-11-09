package compression

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
)

func TestSummaryContextBudgetRejectsBeforeRequestBuild(t *testing.T) {
	window := 128000
	cand := &ProviderCandidate{ContextWindow: &window, BaseURL: "https://supplier.invalid", RawModel: "model"}
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 70000) + `"}]}`)
	ctx := contextbudget.WithSpec(context.Background(), contextbudget.Spec{})
	if req, err := buildCompactionRequest(ctx, cand, body, false); err == nil || req != nil {
		t.Fatalf("oversized summary escaped budget: req=%v err=%v", req, err)
	}
}

func TestSummaryContextBudgetUsesCurrentCandidateOnFailover(t *testing.T) {
	large, small := 2000000, 128000
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 70000) + `"}]}`)
	ctx := contextbudget.WithSpec(context.Background(), contextbudget.Spec{ContextWindow: large})
	cand := &ProviderCandidate{ContextWindow: &large, BaseURL: "https://supplier.invalid", RawModel: "model"}
	if _, err := buildCompactionRequest(ctx, cand, body, false); err != nil {
		t.Fatalf("large summary candidate rejected: %v", err)
	}
	cand.ContextWindow = &small
	if _, err := buildCompactionRequest(ctx, cand, body, true); err == nil {
		t.Fatal("summary failover reused the previous candidate's context window")
	}
}

func TestSummaryContextBudgetUnknownStrictAndLegacy(t *testing.T) {
	cand := &ProviderCandidate{BaseURL: "https://supplier.invalid", RawModel: "model"}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	if _, err := buildCompactionRequest(contextbudget.WithSpec(context.Background(), contextbudget.Spec{}), cand, body, false); err == nil {
		t.Fatal("strict summary accepted unknown context")
	}
	if _, err := buildCompactionRequest(context.Background(), cand, body, false); err != nil {
		t.Fatalf("legacy isolated integration changed: %v", err)
	}
}
