package authentication

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestAPIKeyAuthHook_Name(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	if h.Name() != "authentication.api_key" {
		t.Fatalf("Name() = %q, want authentication.api_key", h.Name())
	}
}

func TestAPIKeyAuthHook_Priority(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	if h.Priority() != 100 {
		t.Fatalf("Priority() = %d, want 100", h.Priority())
	}
}

func TestAPIKeyAuthHook_Enabled_NilEnvelope(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	if h.Enabled(context.Background(), nil) {
		t.Fatal("Enabled should return false for nil envelope")
	}
}

func TestAPIKeyAuthHook_Enabled_NoKey(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if !h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return true when APIKey is nil")
	}
}

func TestAPIKeyAuthHook_Enabled_AlreadyAuthenticated(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.APIKey = &domain.PipelineAPIKey{ID: "x"}
	if h.Enabled(context.Background(), env) {
		t.Fatal("Enabled should return false when APIKey is set")
	}
}

func TestAPIKeyAuthHook_Execute_NilEnvelope(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	if err := h.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute(nil) = %v, want nil", err)
	}
}

func TestAPIKeyAuthHook_Execute_MissingKey(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
}

func TestAPIKeyAuthHook_Execute_WrongType(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["api_key"] = 123 // not a string
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error for non-string api_key")
	}
}

func TestAPIKeyAuthHook_Execute_EmptyKey(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["api_key"] = ""
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error for empty api_key")
	}
}

func TestAPIKeyAuthHook_Execute_VerifierDisabled(t *testing.T) {
	// 没配置 DB 的 verifier → Verify 返回 error
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["api_key"] = "sk-test"
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected error when verifier is not configured")
	}
}

func TestAPIKeyAuthHook_OnError_PassThrough(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	err := h.OnError(context.Background(), env, errSentinelImpl2{})
	if err == nil {
		t.Fatal("OnError should not swallow errors")
	}
	if env.Error == nil {
		t.Fatal("OnError should set env.Error")
	}
}

func TestAPIKeyAuthHook_OnError_NilEnvelope(t *testing.T) {
	h := NewAPIKeyAuthHook(&Verifier{})
	if err := h.OnError(context.Background(), nil, errSentinelImpl2{}); err == nil {
		t.Fatal("OnError should still return err even with nil env")
	}
}

type errSentinelImpl2 struct{}

func (errSentinelImpl2) Error() string { return "sentinel" }
