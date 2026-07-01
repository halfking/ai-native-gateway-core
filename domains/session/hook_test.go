package session

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestSessionLoaderHook_Name(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	if h.Name() != "session.load" {
		t.Fatalf("Name() = %q, want session.load", h.Name())
	}
}

func TestSessionLoaderHook_Priority(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	if h.Priority() != 200 {
		t.Fatalf("Priority() = %d, want 200", h.Priority())
	}
}

func TestSessionLoaderHook_Enabled_NilEnvelope(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	if h.Enabled(context.Background(), nil) {
		t.Fatal("Enabled should return false for nil envelope")
	}
}

func TestSessionLoaderHook_Enabled_EmptySessionID(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return false when SessionID is empty")
	}
}

func TestSessionLoaderHook_Enabled_WithSessionID(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SessionID = "s1"
	if !h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return true when SessionID is set")
	}
}

func TestSessionLoaderHook_Execute_NilEnvelope(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	if err := h.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute(nil) = %v, want nil", err)
	}
}

func TestSessionLoaderHook_Execute_SessionNotFound(t *testing.T) {
	store := newStubStore()
	h := NewSessionLoaderHook(store, NewStickyRouter(store))
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SessionID = "nonexistent"
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute err = %v, want nil (吞掉 ErrSessionNotFound)", err)
	}
}

func TestSessionLoaderHook_Execute_InjectsPreferredCredential(t *testing.T) {
	store := newStubStore()
	store.store["s1"] = &Session{SessionID: "s1", LastCredentialID: "cred-42"}
	router := NewStickyRouter(store)
	h := NewSessionLoaderHook(store, router)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.SessionID = "s1"
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if env.Metadata["preferred_credential"] != "cred-42" {
		t.Fatalf("preferred_credential = %v, want cred-42", env.Metadata["preferred_credential"])
	}
}

func TestSessionLoaderHook_OnError_Swallows(t *testing.T) {
	h := NewSessionLoaderHook(newStubStore(), NewStickyRouter(newStubStore()))
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := h.OnError(context.Background(), env, errSentinel); err != nil {
		t.Fatalf("OnError should swallow errors, got %v", err)
	}
}

type errSentinelImpl struct{}

func (errSentinelImpl) Error() string { return "sentinel" }

var errSentinel = errSentinelImpl{}
