package credential

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestInMemoryStore_SaveAndGet(t *testing.T) {
	s := NewInMemoryStore()
	cred := &Credential{
		ID: "c1", TenantID: "t1", ProviderID: "openai", Model: "gpt-4",
		EncryptedKey: []byte("encrypted"), MaxConcurrent: 10, Priority: 50,
		Status: StatusActive,
	}
	if err := s.Save(cred); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	got, ok, err := s.Get("c1")
	if err != nil || !ok {
		t.Fatalf("Get failed: ok=%v err=%v", ok, err)
	}
	if got.TenantID != "t1" {
		t.Errorf("expected tenant t1, got %q", got.TenantID)
	}
}

func TestInMemoryStore_GetNotFound(t *testing.T) {
	s := NewInMemoryStore()
	_, ok, err := s.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get should not error: %v", err)
	}
	if ok {
		t.Error("expected ok=false")
	}
}

func TestInMemoryStore_GetReturnsCopy(t *testing.T) {
	s := NewInMemoryStore()
	cred := &Credential{ID: "c1", TenantID: "t1", Metadata: map[string]any{"k": "v"}}
	_ = s.Save(cred)
	got, _, _ := s.Get("c1")
	got.Metadata["k"] = "modified"
	original, _, _ := s.Get("c1")
	if original.Metadata["k"] != "v" {
		t.Error("Get should return copy")
	}
}

func TestInMemoryStore_Delete(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1"})
	_ = s.Delete("c1")
	if s.Count() != 0 {
		t.Errorf("expected 0, got %d", s.Count())
	}
}

func TestInMemoryStore_ListByTenant(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1"})
	_ = s.Save(&Credential{ID: "c2", TenantID: "t1"})
	_ = s.Save(&Credential{ID: "c3", TenantID: "t2"})
	list, _ := s.List("t1")
	if len(list) != 2 {
		t.Errorf("expected 2 credentials for t1, got %d", len(list))
	}
}

func TestInMemoryStore_SaveRequiresID(t *testing.T) {
	s := NewInMemoryStore()
	if err := s.Save(&Credential{}); err == nil {
		t.Error("expected error for empty ID")
	}
	if err := s.Save(nil); err == nil {
		t.Error("expected error for nil")
	}
}

func TestHealthChecker_MarkSuccess(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	_ = s.Save(&Credential{ID: "c1", Status: StatusDegraded, ConsecutiveFails: 1})
	if err := hc.MarkSuccess("c1"); err != nil {
		t.Fatalf("MarkSuccess failed: %v", err)
	}
	cred, _, _ := s.Get("c1")
	if cred.Status != StatusActive {
		t.Errorf("expected StatusActive, got %q", cred.Status)
	}
	if cred.ConsecutiveFails != 0 {
		t.Errorf("expected 0 fails, got %d", cred.ConsecutiveFails)
	}
}

func TestHealthChecker_MarkFailure(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	hc.failThreshold = 2
	_ = s.Save(&Credential{ID: "c1", Status: StatusActive})

	_ = hc.MarkFailure("c1")
	cred, _, _ := s.Get("c1")
	if cred.Status != StatusDegraded {
		t.Errorf("expected StatusDegraded, got %q", cred.Status)
	}

	_ = hc.MarkFailure("c1")
	cred, _, _ = s.Get("c1")
	if cred.Status != StatusUnhealthy {
		t.Errorf("expected StatusUnhealthy, got %q", cred.Status)
	}
}

func TestHealthChecker_FilterHealthy(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	creds := []*Credential{
		{ID: "c1", Status: StatusActive},
		{ID: "c2", Status: StatusUnhealthy},
		{ID: "c3", Status: StatusActive},
	}
	healthy := hc.FilterHealthy(creds)
	if len(healthy) != 2 {
		t.Errorf("expected 2 healthy, got %d", len(healthy))
	}
}

func TestHealthChecker_MarkRequiresID(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	if err := hc.MarkSuccess(""); err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestLimiter_AcquireAndRelease(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", MaxConcurrent: 2})
	l := NewLimiter(s)
	for i := 0; i < 2; i++ {
		ok, err := l.Acquire("c1")
		if err != nil || !ok {
			t.Errorf("acquire %d failed: ok=%v err=%v", i, ok, err)
		}
	}
	ok, _ := l.Acquire("c1")
	if ok {
		t.Error("3rd acquire should fail (limit=2)")
	}
	l.Release("c1")
	ok, _ = l.Acquire("c1")
	if !ok {
		t.Error("after release, acquire should succeed")
	}
}

func TestLimiter_NoLimit(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", MaxConcurrent: 0})
	l := NewLimiter(s)
	for i := 0; i < 10; i++ {
		if ok, _ := l.Acquire("c1"); !ok {
			t.Errorf("acquire %d should succeed (no limit)", i)
		}
	}
}

func TestLimiter_RequiresID(t *testing.T) {
	s := NewInMemoryStore()
	l := NewLimiter(s)
	if _, err := l.Acquire(""); err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestCircuitBreaker_OpenAfterFailures(t *testing.T) {
	cb := NewCircuitBreaker(2, 100*time.Millisecond)
	if !cb.Allow() {
		t.Fatal("should allow initially")
	}
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Errorf("expected StateOpen, got %q", cb.State())
	}
	if cb.Allow() {
		t.Error("should not allow when open")
	}
	time.Sleep(150 * time.Millisecond)
	if !cb.Allow() {
		t.Error("should allow after open timeout (half-open)")
	}
	if cb.State() != StateHalfOpen {
		t.Errorf("expected StateHalfOpen, got %q", cb.State())
	}
}

func TestCircuitBreaker_RecoveryToClosed(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	_ = cb.Allow() // half-open
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Errorf("expected StateClosed after success, got %q", cb.State())
	}
}

func TestHealthCheckHook_LoadsCandidates(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1", ProviderID: "p1", Status: StatusActive})
	_ = s.Save(&Credential{ID: "c2", TenantID: "t1", ProviderID: "p2", Status: StatusUnhealthy})

	hook := NewHealthCheckHook(s, hc)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t1"
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	count, _ := env.Metadata["credential_count"].(int)
	if count != 1 {
		t.Errorf("expected 1 healthy credential, got %d", count)
	}
}

func TestHealthCheckHook_DifferentTenants(t *testing.T) {
	s := NewInMemoryStore()
	hc := NewHealthChecker(s)
	_ = s.Save(&Credential{ID: "c1", TenantID: "t1", Status: StatusActive})
	_ = s.Save(&Credential{ID: "c2", TenantID: "t2", Status: StatusActive})

	hook := NewHealthCheckHook(s, hc)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TenantID = "t1"
	_ = hook.Execute(context.Background(), env)
	count, _ := env.Metadata["credential_count"].(int)
	if count != 1 {
		t.Errorf("expected 1 credential for t1, got %d", count)
	}
}

func TestLimiterHook_AcquiresAndReleases(t *testing.T) {
	s := NewInMemoryStore()
	_ = s.Save(&Credential{ID: "c1", MaxConcurrent: 1})
	l := NewLimiter(s)
	hook := NewLimiterHook(l)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SelectedCredential = &domain.PipelineCredential{ID: "c1"}
	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if locked, _ := env.Metadata["credential_locked"].(bool); !locked {
		t.Error("expected credential_locked=true")
	}

	// 2nd request 应被限流
	env2 := domain.NewRequestEnvelope(context.Background(), nil)
	env2.SelectedCredential = &domain.PipelineCredential{ID: "c1"}
	if err := hook.Execute(context.Background(), env2); err == nil {
		t.Error("2nd request should be rate-limited")
	}

	// OnError 释放槽位
	_ = hook.OnError(context.Background(), env, errors.New("oops"))
	if l.InFlight("c1") != 0 {
		t.Errorf("expected 0 in-flight after release, got %d", l.InFlight("c1"))
	}
}

func TestLimiterHook_NoSelectedCredential(t *testing.T) {
	s := NewInMemoryStore()
	l := NewLimiter(s)
	hook := NewLimiterHook(l)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := hook.Execute(context.Background(), env)
	if err == nil {
		t.Error("expected error when no SelectedCredential")
	}
}

func TestPlainCrypto_Roundtrip(t *testing.T) {
	c := PlainCrypto{}
	enc, _ := c.Encrypt([]byte("secret"))
	dec, _ := c.Decrypt(enc)
	if string(dec) != "secret" {
		t.Errorf("expected 'secret', got %q", string(dec))
	}
}
