package reqprobe

import (
	"context"
	"testing"
	"time"
)

// P0-B：白名单可选字段在上游点名拒绝时立即进入 LearnedParams（不必等
// 重试成功）。chat 线 stream_options 仍不学（骨架保护——前提在场配对）。

func waitStoreVisible(t *testing.T, store *MemoryStore, provider, model string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recs, _, err := store.List(context.Background(), Filter{ProviderCode: provider, Model: model})
		if err == nil && len(recs) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("record never landed for %s/%s", provider, model)
}

func TestP0BLearnOnRefusalImmediate(t *testing.T) {
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	defer coord.Close()
	coord.RecordTerminal(
		Input{HTTPStatus: 400, ErrorBody: []byte(`unknown`), Protocol: "openai-responses"},
		Diagnosis{Trigger: TriggerParamRejected, Param: "service_tier"},
		TerminalMeta{ProviderCode: "vapeur", OutboundModel: "grok-4.6", ProviderID: 1},
		false, // 拒绝即学习：recovered=false 也必须进 LearnedParams
	)
	waitStoreVisible(t, store, "vapeur", "grok-4.6")
	coord.InvalidateLearned()
	got := coord.LearnedParams(context.Background(), "vapeur", "grok-4.6")
	found := false
	for _, p := range got {
		if p == "service_tier" {
			found = true
		}
	}
	if !found {
		t.Fatalf("LearnedParams after refusal = %v, want service_tier (learn-on-refusal)", got)
	}
}

func TestP0BChatStreamOptionsNotLearnedOnRefusal(t *testing.T) {
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	defer coord.Close()
	coord.RecordTerminal(
		Input{HTTPStatus: 400, ErrorBody: []byte(`unknown`), Protocol: "openai-chat"},
		Diagnosis{Trigger: TriggerParamRejected, Param: "stream_options"},
		TerminalMeta{ProviderCode: "vapeur", OutboundModel: "grok-4.6"},
		false,
	)
	waitStoreVisible(t, store, "vapeur", "grok-4.6")
	coord.InvalidateLearned()
	got := coord.LearnedParams(context.Background(), "vapeur", "grok-4.6")
	for _, p := range got {
		if p == "stream_options" {
			t.Fatalf("chat wire must NOT learn stream_options on refusal, got %v", got)
		}
	}
}

func TestP0BResponsesStreamOptionsLearnedOnRefusal(t *testing.T) {
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	defer coord.Close()
	coord.RecordTerminal(
		Input{HTTPStatus: 400, ErrorBody: []byte(`unknown`), Protocol: "openai-responses"},
		Diagnosis{Trigger: TriggerParamRejected, Param: "stream_options"},
		TerminalMeta{ProviderCode: "vapeur", OutboundModel: "grok-4.6"},
		false,
	)
	waitStoreVisible(t, store, "vapeur", "grok-4.6")
	coord.InvalidateLearned()
	got := coord.LearnedParams(context.Background(), "vapeur", "grok-4.6")
	found := false
	for _, p := range got {
		if p == "stream_options" {
			found = true
		}
	}
	if !found {
		t.Fatalf("responses wire must learn stream_options on refusal, got %v", got)
	}
}

func TestP0BCoreFieldNeverLearnedOnRefusal(t *testing.T) {
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	defer coord.Close()
	coord.RecordTerminal(
		Input{HTTPStatus: 400, ErrorBody: []byte(`unknown`), Protocol: "openai-chat"},
		Diagnosis{Trigger: TriggerParamRejected, Param: "messages"},
		TerminalMeta{ProviderCode: "vapeur", OutboundModel: "grok-4.6"},
		false,
	)
	waitStoreVisible(t, store, "vapeur", "grok-4.6")
	coord.InvalidateLearned()
	if got := coord.LearnedParams(context.Background(), "vapeur", "grok-4.6"); len(got) != 0 {
		t.Fatalf("core field messages must never be learned, got %v", got)
	}
}
