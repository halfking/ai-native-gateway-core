package admin

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// ──────────────────────────────────────────────────────────────────────────────
// TDD: Agent Registry API tests (Phase 3 A3-1)
// ──────────────────────────────────────────────────────────────────────────────

// TestNewAgentsHandler verifies constructor doesn't panic.
func TestNewAgentsHandler(t *testing.T) {
	svc := apihub.New(nil) // nil store = no-op
	handler := NewAgentsHandler(svc)

	if handler == nil {
		t.Fatal("NewAgentsHandler returned nil")
	}
	if handler.svc != svc {
		t.Error("handler.svc not set correctly")
	}
}

// Full HTTP tests require real apihub.Service with PG, so we defer to
// integration tests after deploy. Unit tests here only verify constructor.
