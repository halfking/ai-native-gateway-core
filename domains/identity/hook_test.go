package identity

import (
	"context"
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestIdentityHook_Name(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	if h.Name() != "identity.inject" {
		t.Fatalf("Name() = %q, want identity.inject", h.Name())
	}
}

func TestIdentityHook_Priority(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	if h.Priority() != 100 {
		t.Fatalf("Priority() = %d, want 100", h.Priority())
	}
}

func TestIdentityHook_Enabled_NilEnvelope(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	if h.Enabled(context.Background(), nil) {
		t.Fatal("Enabled should return false for nil envelope")
	}
}

func TestIdentityHook_Enabled_EmptyIdentity(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if !h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return true when ClientIdentity is nil")
	}
}

func TestIdentityHook_Enabled_AlreadyInjected(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.ClientIdentity = &domain.PipelineClientIdentity{Hash: "x"}
	if h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return false when ClientIdentity is set")
	}
}

func TestIdentityHook_Execute_NilEnvelope(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	if err := h.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute(nil) = %v, want nil", err)
	}
}

func TestIdentityHook_Execute_InjectsIdentity(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "hook-test")
	ctx := WithRequest(context.Background(), r)

	builder := NewIdentityBuilder()
	h := NewIdentityHook(builder)

	env := domain.NewRequestEnvelope(ctx, nil)
	env.TenantID = "tenant-a"

	if err := h.Execute(ctx, env); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if env.ClientIdentity == nil {
		t.Fatal("ClientIdentity not injected")
	}
	if env.ClientIdentity.Hash == "" {
		t.Fatal("ClientIdentity.Hash is empty")
	}
	if env.ClientIdentity.VirtualIP == "" {
		t.Fatal("ClientIdentity.VirtualIP is empty")
	}
	if env.ClientIdentity.VirtualMAC == "" {
		t.Fatal("ClientIdentity.VirtualMAC is empty")
	}
}

func TestIdentityHook_OnError_PassThrough(t *testing.T) {
	h := NewIdentityHook(NewIdentityBuilder())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := h.OnError(context.Background(), env, errSentinel{})
	if err == nil {
		t.Fatal("OnError should not swallow errors")
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }
