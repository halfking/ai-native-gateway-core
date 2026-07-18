package sensitive

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/security/guardian"
)

func buildTestEngine(t *testing.T) *SensitiveWordEngine {
	t.Helper()
	e := NewSensitiveWordEngine()
	err := e.Build(&SensitiveWordConfig{
		Version: "1.0",
		Categories: map[string]CategoryConf{
			"political":       {Name: "政治敏感词", Words: []string{"六四", "法轮功"}},
			"sexual_violence": {Name: "色情暴力", Words: []string{"色情", "暴力"}},
			"test_sensitive":  {Name: "测试词", Words: []string{"测试敏感词1"}},
		},
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	return e
}

func TestSensitiveInputGuard_Pass(t *testing.T) {
	e := buildTestEngine(t)
	g := NewSensitiveInputGuard(e)

	v, err := g.CheckInput(context.Background(), "今天天气不错")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != guardian.ActionPass {
		t.Errorf("expected ActionPass, got %v", v.Action)
	}
}

func TestSensitiveInputGuard_P0(t *testing.T) {
	e := buildTestEngine(t)
	g := NewSensitiveInputGuard(e)

	v, err := g.CheckInput(context.Background(), "这是一个色情视频")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != guardian.ActionBlock {
		t.Errorf("expected ActionBlock for P0 word, got %v", v.Action)
	}
	if v.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestSensitiveInputGuard_P1(t *testing.T) {
	e := buildTestEngine(t)
	g := NewSensitiveInputGuard(e)

	v, err := g.CheckInput(context.Background(), "法轮功相关内容")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != guardian.ActionWarn {
		t.Errorf("expected ActionWarn for P1 word, got %v", v.Action)
	}
}

func TestSensitiveInputGuard_P2(t *testing.T) {
	e := buildTestEngine(t)
	g := NewSensitiveInputGuard(e)

	v, err := g.CheckInput(context.Background(), "测试敏感词1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != guardian.ActionPass {
		t.Errorf("expected ActionPass for P2 word, got %v", v.Action)
	}
}

func TestSensitiveOutputGuard_Block(t *testing.T) {
	e := buildTestEngine(t)
	g := NewSensitiveOutputGuard(e)

	v, err := g.CheckOutput(context.Background(), "req", "输出的内容包含色情词汇")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != guardian.ActionBlock {
		t.Errorf("expected ActionBlock for P0 output, got %v", v.Action)
	}
}

func TestSensitiveOutputGuard_Pass(t *testing.T) {
	e := buildTestEngine(t)
	g := NewSensitiveOutputGuard(e)

	v, err := g.CheckOutput(context.Background(), "req", "正常输出内容")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Action != guardian.ActionPass {
		t.Errorf("expected ActionPass, got %v", v.Action)
	}
}

func TestGuardName(t *testing.T) {
	g1 := NewSensitiveInputGuard(&SensitiveWordEngine{})
	g2 := NewSensitiveOutputGuard(&SensitiveWordEngine{})

	if g1.Name() != "sensitive_word_in" {
		t.Errorf("input guard name = %q", g1.Name())
	}
	if g2.Name() != "sensitive_word_out" {
		t.Errorf("output guard name = %q", g2.Name())
	}
}

func TestGuardIntegratedWithGuardian(t *testing.T) {
	e := buildTestEngine(t)
	guard := NewSensitiveInputGuard(e)

	g := guardian.NewGuardian(
		[]guardian.InputGuard{guard},
		nil,
		guardian.WithDecider(guardian.NewGuardDecider(guardian.ModeBlock)),
	)

	_, err := g.GuardInput(context.Background(), "t1", "色情内容")
	if err == nil {
		t.Fatal("expected block error from guardian")
	}
}

func TestGuardianObserveMode(t *testing.T) {
	e := buildTestEngine(t)
	guard := NewSensitiveInputGuard(e)

	g := guardian.NewGuardian(
		[]guardian.InputGuard{guard},
		nil,
		guardian.WithDecider(guardian.NewGuardDecider(guardian.ModeObserve)),
	)

	body, err := g.GuardInput(context.Background(), "t1", "色情内容")
	if err != nil {
		t.Fatalf("observe mode should not block: %v", err)
	}
	if body != "色情内容" {
		t.Errorf("body should be unchanged in observe mode")
	}
}
