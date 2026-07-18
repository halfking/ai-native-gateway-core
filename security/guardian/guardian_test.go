package guardian

import (
	"context"
	"errors"
	"testing"
)

type mockInputGuard struct {
	name   string
	action GuardAction
	err    error
}

func (m *mockInputGuard) Name() string { return m.name }

func (m *mockInputGuard) CheckInput(_ context.Context, body string) (*GuardVerdict, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &GuardVerdict{
		Action:    m.action,
		GuardName: m.name,
		Message:   "mock check",
	}, nil
}

type mockOutputGuard struct {
	name   string
	action GuardAction
	err    error
}

func (m *mockOutputGuard) Name() string { return m.name }

func (m *mockOutputGuard) CheckOutput(_ context.Context, reqBody, respBody string) (*GuardVerdict, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &GuardVerdict{
		Action:    m.action,
		GuardName: m.name,
		Message:   "mock output check",
	}, nil
}

func TestNewGuardian(t *testing.T) {
	g := NewGuardian(nil, nil)
	if g == nil {
		t.Fatal("NewGuardian returned nil")
	}
	if g.decider == nil {
		t.Fatal("decider should not be nil")
	}
	if g.auditor == nil {
		t.Fatal("auditor should not be nil")
	}
}

func TestGuardian_InputPass(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "pass-guard", action: ActionPass}},
		nil,
	)
	body, err := g.GuardInput(context.Background(), "t-1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "hello" {
		t.Fatalf("body = %q, want %q", body, "hello")
	}
}

func TestGuardian_InputBlock(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "block-guard", action: ActionBlock}},
		nil,
		WithDecider(NewGuardDecider(ModeBlock)),
	)
	_, err := g.GuardInput(context.Background(), "t-1", "bad content")
	if err == nil {
		t.Fatal("expected block error")
	}
}

func TestGuardian_InputWarn(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "warn-guard", action: ActionWarn}},
		nil,
	)
	body, err := g.GuardInput(context.Background(), "t-1", "suspicious")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "suspicious" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_InputRewrite(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{
			name:   "rewrite-guard",
			action: ActionRewrite,
		}},
		nil,
	)
	body, err := g.GuardInput(context.Background(), "t-1", "original")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "original" {
		t.Fatalf("body = %q, want %q", body, "original")
	}
}

func TestGuardian_InputRewriteWithBody(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuardRewrite{name: "rewrite-guard"}},
		nil,
		WithDecider(NewGuardDecider(ModeBlock)),
	)
	body, err := g.GuardInput(context.Background(), "t-1", "original")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "rewritten" {
		t.Fatalf("body = %q, want %q", body, "rewritten")
	}
}

type mockInputGuardRewrite struct {
	name string
}

func (m *mockInputGuardRewrite) Name() string { return m.name }

func (m *mockInputGuardRewrite) CheckInput(_ context.Context, body string) (*GuardVerdict, error) {
	return &GuardVerdict{
		Action:        ActionRewrite,
		GuardName:     m.name,
		Message:       "rewritten",
		RewrittenBody: []byte("rewritten"),
	}, nil
}

func TestGuardian_InputErrorBlockMode(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "err-guard", err: errors.New("check failed")}},
		nil,
		WithDecider(NewGuardDecider(ModeBlock)),
	)
	_, err := g.GuardInput(context.Background(), "t-1", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGuardian_InputErrorObserveMode(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "err-guard", err: errors.New("check failed")}},
		nil,
	)
	body, err := g.GuardInput(context.Background(), "t-1", "hello")
	if err != nil {
		t.Fatalf("observe mode should swallow guard errors: %v", err)
	}
	if body != "hello" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_MultipleInputGuards_Pass(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{
			&mockInputGuard{name: "g1", action: ActionPass},
			&mockInputGuard{name: "g2", action: ActionPass},
		},
		nil,
	)
	body, err := g.GuardInput(context.Background(), "t-1", "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "text" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_MultipleInputGuards_FirstBlocks(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{
			&mockInputGuard{name: "g1", action: ActionBlock},
			&mockInputGuard{name: "g2", action: ActionPass},
		},
		nil,
		WithDecider(NewGuardDecider(ModeBlock)),
	)
	_, err := g.GuardInput(context.Background(), "t-1", "bad")
	if err == nil {
		t.Fatal("expected block error")
	}
}

func TestGuardian_OutputPass(t *testing.T) {
	g := NewGuardian(
		nil,
		[]OutputGuard{&mockOutputGuard{name: "output-guard", action: ActionPass}},
	)
	body, err := g.GuardOutput(context.Background(), "t-1", "req", "resp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "resp" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_OutputBlock(t *testing.T) {
	g := NewGuardian(
		nil,
		[]OutputGuard{&mockOutputGuard{name: "output-block", action: ActionBlock}},
		WithDecider(NewGuardDecider(ModeBlock)),
	)
	_, err := g.GuardOutput(context.Background(), "t-1", "req", "bad resp")
	if err == nil {
		t.Fatal("expected block error")
	}
}

func TestGuardian_ObserveMode_DoesNotBlock(t *testing.T) {
	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "obs-guard", action: ActionBlock}},
		nil,
		WithDecider(NewGuardDecider(ModeObserve)),
	)
	body, err := g.GuardInput(context.Background(), "t-1", "should observe")
	if err != nil {
		t.Fatalf("observe mode should not block: %v", err)
	}
	if body != "should observe" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_TenantSpecificPolicy(t *testing.T) {
	decider := NewGuardDecider(ModeObserve)
	decider.SetTenantPolicy("t-1", &TenantGuardPolicy{Mode: ModeBlock})

	g := NewGuardian(
		[]InputGuard{&mockInputGuard{name: "block-guard", action: ActionBlock}},
		nil,
		WithDecider(decider),
	)

	_, err := g.GuardInput(context.Background(), "t-1", "bad")
	if err == nil {
		t.Fatal("t-1 should be blocked")
	}

	body, err := g.GuardInput(context.Background(), "t-2", "bad")
	if err != nil {
		t.Fatalf("t-2 should pass in observe mode: %v", err)
	}
	if body != "bad" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_InputNoGuards(t *testing.T) {
	g := NewGuardian(nil, nil)
	body, err := g.GuardInput(context.Background(), "t-1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "hello" {
		t.Fatalf("body = %q", body)
	}
}

func TestGuardian_OutputNoGuards(t *testing.T) {
	g := NewGuardian(nil, nil)
	body, err := g.GuardOutput(context.Background(), "t-1", "req", "resp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "resp" {
		t.Fatalf("body = %q", body)
	}
}
