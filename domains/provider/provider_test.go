package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestInMemoryStore_SaveAndGet(t *testing.T) {
	s := NewInMemoryStore()
	p := &Provider{
		ID: "openai-main", Name: "OpenAI", BaseURL: "https://api.openai.com",
		Protocol: ProtocolOpenAI, AuthType: "bearer",
		Models: []ModelSpec{{Name: "gpt-4", MaxContextTokens: 8192, SupportsStream: true}},
	}
	if err := s.Save(p); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	got, ok, err := s.Get("openai-main")
	if err != nil || !ok {
		t.Fatalf("Get failed: ok=%v err=%v", ok, err)
	}
	if got.Name != "OpenAI" {
		t.Errorf("expected 'OpenAI', got %q", got.Name)
	}
}

func TestInMemoryStore_SaveRequiresID(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Save(&Provider{}); err == nil {
		t.Error("expected error for empty ID")
	}
	if err := s.Save(nil); err == nil {
		t.Error("expected error for nil")
	}
}

func TestInMemoryStore_FindByModel(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1", Models: []ModelSpec{{Name: "gpt-4"}, {Name: "gpt-3.5"}}})
	_ = s.Save(&Provider{ID: "p2", Models: []ModelSpec{{Name: "gpt-4"}}})
	_ = s.Save(&Provider{ID: "p3", Models: []ModelSpec{{Name: "claude-3"}}})

	gpt4, _ := s.FindByModel("gpt-4")
	if len(gpt4) != 2 {
		t.Errorf("expected 2 providers for gpt-4, got %d", len(gpt4))
	}
	claude, _ := s.FindByModel("claude-3")
	if len(claude) != 1 {
		t.Errorf("expected 1 provider for claude-3, got %d", len(claude))
	}
	unknown, _ := s.FindByModel("unknown")
	if len(unknown) != 0 {
		t.Errorf("expected 0 providers, got %d", len(unknown))
	}
}

func TestInMemoryStore_DeleteAndList(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1"})
	_ = s.Save(&Provider{ID: "p2"})
	_ = s.Delete("p1")
	if s.Count() != 1 {
		t.Errorf("expected 1, got %d", s.Count())
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Errorf("expected 1 in list, got %d", len(list))
	}
}

func TestProvider_Status(t *testing.T) {
	tests := []struct {
		name     string
		p        *Provider
		expected Status
	}{
		{"active", &Provider{ConsecutiveFails: 0}, StatusActive},
		{"degraded", &Provider{ConsecutiveFails: 1}, StatusDegraded},
		{"unhealthy", &Provider{ConsecutiveFails: 5}, StatusUnhealthy},
		{"disabled", &Provider{ConsecutiveFails: 0, Disabled: true}, StatusDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Status(); got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestProvider_SupportsModel(t *testing.T) {
	p := &Provider{Models: []ModelSpec{{Name: "gpt-4"}, {Name: "gpt-3.5"}}}
	if !p.SupportsModel("gpt-4") {
		t.Error("should support gpt-4")
	}
	if p.SupportsModel("unknown") {
		t.Error("should not support unknown")
	}
}

func TestProber_MarkSuccess(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1", ConsecutiveFails: 2})
	pr := NewProber(s)
	if err := pr.MarkSuccess("p1"); err != nil {
		t.Fatalf("MarkSuccess failed: %v", err)
	}
	p, _, _ := s.Get("p1")
	if p.ConsecutiveFails != 0 {
		t.Errorf("expected 0 fails, got %d", p.ConsecutiveFails)
	}
}

func TestProber_MarkFailure(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1"})
	pr := NewProber(s)
	for i := 0; i < 3; i++ {
		_ = pr.MarkFailure("p1")
	}
	p, _, _ := s.Get("p1")
	if p.Status() != StatusUnhealthy {
		t.Errorf("expected StatusUnhealthy, got %q", p.Status())
	}
}

func TestProber_ProbeSuccess(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1", Disabled: false})
	pr := NewProber(s)
	err := pr.Probe("p1", func(p *Provider) error { return nil })
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestProber_ProbeFailure(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1"})
	pr := NewProber(s)
	err := pr.Probe("p1", func(p *Provider) error { return errors.New("fail") })
	if err == nil {
		t.Error("expected error")
	}
	p, _, _ := s.Get("p1")
	if p.ConsecutiveFails == 0 {
		t.Error("expected ConsecutiveFails to be incremented")
	}
}

func TestProber_ProbeDisabled(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1", Disabled: true})
	pr := NewProber(s)
	if err := pr.Probe("p1", nil); err == nil {
		t.Error("expected error for disabled provider")
	}
}

func TestProber_ProbeNotFound(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	if err := pr.Probe("nonexistent", nil); err == nil {
		t.Error("expected error for not-found")
	}
}

func TestProber_FilterHealthy(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1"}) // active
	_ = s.Save(&Provider{ID: "p2", Disabled: true})
	_ = s.Save(&Provider{ID: "p3", ConsecutiveFails: 5}) // unhealthy
	pr := NewProber(s)
	all, _ := s.List()
	healthy := pr.FilterHealthy(all)
	if len(healthy) != 1 {
		t.Errorf("expected 1 healthy, got %d", len(healthy))
	}
	if healthy[0].ID != "p1" {
		t.Errorf("expected p1, got %q", healthy[0].ID)
	}
}

func TestProviderDiscoveryHook_FindsProviders(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	_ = s.Save(&Provider{ID: "p1", Models: []ModelSpec{{Name: "gpt-4"}}})
	_ = s.Save(&Provider{ID: "p2", Models: []ModelSpec{{Name: "gpt-4"}, {Name: "gpt-3.5"}}})

	hook := NewProviderDiscoveryHook(s, pr)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{"model": "gpt-4"}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	count, _ := env.Metadata["provider_count"].(int)
	if count != 2 {
		t.Errorf("expected 2 providers, got %d", count)
	}
}

func TestProviderDiscoveryHook_NoModel(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	hook := NewProviderDiscoveryHook(s, pr)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Errorf("should not error when no model, got %v", err)
	}
}

func TestProviderDiscoveryHook_NoHealthyProvider(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	_ = s.Save(&Provider{ID: "p1", Models: []ModelSpec{{Name: "gpt-4"}}, Disabled: true})
	hook := NewProviderDiscoveryHook(s, pr)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{"model": "gpt-4"}
	if err := hook.Execute(context.Background(), env); err == nil {
		t.Error("expected error for no healthy provider")
	}
}

func TestProviderDiscoveryHook_EnabledNilEnv(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	hook := NewProviderDiscoveryHook(s, pr)
	if hook.Enabled(context.Background(), nil) {
		t.Error("should be disabled with nil env")
	}
}

func TestInMemoryStore_GetReturnsCopy(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1", Models: []ModelSpec{{Name: "gpt-4"}}})
	got, _, _ := s.Get("p1")
	got.Models[0].Name = "modified"
	original, _, _ := s.Get("p1")
	if original.Models[0].Name != "gpt-4" {
		t.Error("Get should return copy")
	}
}

func TestProber_RequiresID(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	if err := pr.MarkSuccess(""); err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestProvider_DisabledBlocksDiscovery(t *testing.T) {
	s := NewInMemoryStore()
	pr := NewProber(s)
	_ = s.Save(&Provider{ID: "p1", Models: []ModelSpec{{Name: "gpt-4"}}, Disabled: true})
	hook := NewProviderDiscoveryHook(s, pr)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{"model": "gpt-4"}
	err := hook.Execute(context.Background(), env)
	if err == nil {
		t.Error("disabled provider should be filtered out")
	}
}

func TestProber_HealthTimeUpdate(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Provider{ID: "p1"})
	pr := NewProber(s)
	before := time.Now()
	_ = pr.MarkSuccess("p1")
	p, _, _ := s.Get("p1")
	if p.LastHealthCheck.Before(before) {
		t.Error("LastHealthCheck should be updated")
	}
}
