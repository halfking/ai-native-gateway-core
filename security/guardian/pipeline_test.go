package guardian

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestPipelineInputGuardHook_Pass(t *testing.T) {
	h := NewPipelineInputGuardHook(&mockInputGuard{name: "test_guard", action: ActionPass}, nil, nil, nil)
	err := h.Execute(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("hello"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPipelineInputGuardHook_Block(t *testing.T) {
	h := NewPipelineInputGuardHook(
		&mockInputGuard{name: "test_guard", action: ActionBlock},
		NewGuardDecider(ModeBlock),
		nil, nil,
	)
	err := h.Execute(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("bad"),
	})
	if err == nil {
		t.Fatal("expected block error")
	}
}

func TestPipelineInputGuardHook_Observe(t *testing.T) {
	h := NewPipelineInputGuardHook(
		&mockInputGuard{name: "test_guard", action: ActionBlock},
		NewGuardDecider(ModeObserve),
		nil, nil,
	)
	err := h.Execute(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("bad"),
	})
	if err != nil {
		t.Fatalf("observe mode should not block: %v", err)
	}
}

func TestPipelineInputGuardHook_Rewrite(t *testing.T) {
	h := NewPipelineInputGuardHook(
		&mockInputGuard{name: "test_guard", action: ActionRewrite},
		NewGuardDecider(ModeBlock),
		nil, nil,
	)
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("original"),
	}
	err := h.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPipelineInputGuardHook_OnError(t *testing.T) {
	h := NewPipelineInputGuardHook(
		&mockInputGuard{name: "err_guard", action: ActionBlock},
		NewGuardDecider(ModeBlock),
		nil, nil,
	)
	err := h.OnError(context.Background(), &domain.PipelineRequest{}, errors.New("test"))
	if err == nil {
		t.Fatal("expected error from OnError")
	}
}

func TestPipelineGuardMeta(t *testing.T) {
	h := NewPipelineInputGuardHook(&mockInputGuard{name: "foo"}, nil, nil, nil)
	if h.Name() != "guardian_input_foo" {
		t.Errorf("name = %q", h.Name())
	}
	if h.Priority() != 100 {
		t.Errorf("priority = %d", h.Priority())
	}
	if !h.Enabled(context.Background(), &domain.PipelineRequest{}) {
		t.Error("expected enabled by default")
	}
}
