package ir

// Pins for the symmetric reasoning-loss reporting added after the 2026-10-02
// D02 audit: SerializeResponsesRequest and SerializeOllama both drop
// budget/effort-shaped reasoning intents without a wire equivalent, and both
// must say so via ReportProtocolLoss instead of staying silent.

import (
	"testing"
)

func TestSerializeResponsesRequest_ReportsBudgetReasoningLoss(t *testing.T) {
	cap := resetDedupAndInstall(t)
	budget := 4096
	req := &InternalRequest{
		Model:          "gpt-5",
		SourceProtocol: ProtocolGeminiGenerate,
		Reasoning:      &ReasoningConfig{Effort: "high", BudgetTokens: &budget},
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	if _, err := SerializeResponsesRequest(req); err != nil {
		t.Fatalf("serialize responses: %v", err)
	}
	if !cap.hasEvent(AnomalyEvent{
		AnomalyType:    AnomalyProtocolLoss,
		FieldPath:      "reasoning.budget_tokens",
		TargetProtocol: ProtocolOpenAIResponses,
	}) {
		t.Fatalf("budget-shaped reasoning intent lost silently; events: %+v", cap.snapshot())
	}
}

func TestSerializeResponsesRequest_EffortOnlyReasoningIsNotALoss(t *testing.T) {
	cap := resetDedupAndInstall(t)
	req := &InternalRequest{
		Model:          "gpt-5",
		SourceProtocol: ProtocolOpenAIChat,
		Reasoning:      &ReasoningConfig{Effort: "high"},
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	if _, err := SerializeResponsesRequest(req); err != nil {
		t.Fatalf("serialize responses: %v", err)
	}
	for _, e := range cap.snapshot() {
		if e.FieldPath == "reasoning.budget_tokens" {
			t.Fatalf("effort-only reasoning must not emit a budget loss; got %+v", e)
		}
	}
}

func TestSerializeOllama_ReportsReasoningLoss(t *testing.T) {
	cap := resetDedupAndInstall(t)
	budget := 2048
	maxRT := 4096
	req := &InternalRequest{
		Model:          "llama3.1",
		SourceProtocol: ProtocolOpenAIChat,
		Reasoning:      &ReasoningConfig{Type: "enabled", Effort: "high", BudgetTokens: &budget, MaxReasoningTokens: &maxRT},
		Thinking:       &ThinkingConfig{Type: "enabled", BudgetTokens: budget},
		Messages:       []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	if _, err := SerializeOllama(req); err != nil {
		t.Fatalf("serialize ollama: %v", err)
	}
	// 五臂全量：max_reasoning_tokens 臂曾是唯一没有钉测的臂（R33 审计 #4），
	// 腐化时全部测试仍绿——补上后五臂齐钉。
	for _, field := range []string{"thinking", "reasoning.type", "reasoning.effort", "reasoning.budget_tokens", "reasoning.max_reasoning_tokens"} {
		if !cap.hasEvent(AnomalyEvent{
			AnomalyType:    AnomalyProtocolLoss,
			FieldPath:      field,
			TargetProtocol: ProtocolOllamaChat,
		}) {
			t.Fatalf("reasoning field %q dropped without a loss event; events: %+v", field, cap.snapshot())
		}
	}
}

func TestSerializeOllama_NoReasoningNoLoss(t *testing.T) {
	cap := resetDedupAndInstall(t)
	req := &InternalRequest{
		Model:    "llama3.1",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	if _, err := SerializeOllama(req); err != nil {
		t.Fatalf("serialize ollama: %v", err)
	}
	for _, e := range cap.snapshot() {
		if e.Reason == "ollama_reasoning_drop" {
			t.Fatalf("plain request must not emit reasoning loss; got %+v", e)
		}
	}
}
