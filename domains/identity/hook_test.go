package identity

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestClientIdentityHook_Name(t *testing.T) {
	h := NewClientIdentityHook()
	if h.Name() != "identity.client_identity" {
		t.Fatalf("Name() = %q, want identity.client_identity", h.Name())
	}
}

func TestClientIdentityHook_Priority(t *testing.T) {
	h := NewClientIdentityHook()
	if h.Priority() != 20 {
		t.Fatalf("Priority() = %d, want 20", h.Priority())
	}
}

func TestClientIdentityHook_Enabled_Nil(t *testing.T) {
	h := NewClientIdentityHook()
	if h.Enabled(context.Background(), nil) {
		t.Fatal("Enabled should return false for nil env")
	}
}

func TestClientIdentityHook_Enabled_NoTransport(t *testing.T) {
	h := NewClientIdentityHook()
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return false when envelope has no Transport")
	}
}

func TestClientIdentityHook_Enabled_AlreadyComputed(t *testing.T) {
	h := NewClientIdentityHook()
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{"identity_hash": "x"}
	if h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return false when identity_hash already set")
	}
}

func TestClientIdentityHook_Execute_Nil(t *testing.T) {
	h := NewClientIdentityHook()
	if err := h.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute(nil) = %v, want nil", err)
	}
}

func TestClientIdentityHook_Execute_SetsMetadata(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "hook-test-123")
	ctx := context.Background()

	env := domain.NewRequestEnvelope(ctx, &domain.RequestEnvelope{
		RequestID: "test-req",
		CreatedAt: time.Now(),
		GoContext: ctx,
		Transport: &domain.TransportContext{R: r},
	})

	h := NewClientIdentityHook()
	if err := h.Execute(ctx, env); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if env.Metadata == nil {
		t.Fatal("Metadata is nil after Execute")
	}
	hash, ok := env.Metadata["identity_hash"]
	if !ok || hash == "" {
		t.Fatal("identity_hash not set in Metadata")
	}
	shortID, ok := env.Metadata["identity_short_id"]
	if !ok || shortID == "" {
		t.Fatal("identity_short_id not set in Metadata")
	}
	ci, ok := env.Metadata["client_identity"]
	if !ok {
		t.Fatal("client_identity not set in Metadata")
	}
	if _, ok := ci.(ClientIdentity); !ok {
		t.Fatalf("client_identity is %T, want ClientIdentity", ci)
	}
}

func TestClientIdentityHook_OnError_Swallows(t *testing.T) {
	h := NewClientIdentityHook()
	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := h.OnError(context.Background(), env, errSentinel{})
	if err != nil {
		t.Fatal("OnError should swallow errors (advisory hook)")
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }
