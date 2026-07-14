package attachmentmirror

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// TestPersistHook_NilRepoIsNoOp pins the contract that nil repo →
// silently inert hook (the wiring layer often constructs the hook
// before the repo, and a forgotten wire-up must not crash).
func TestPersistHook_NilRepoIsNoOp(t *testing.T) {
	hook := PersistHook(nil)
	hook(&telemetry.RequestLogEntry{RequestID: "x"})
	// No panic, no observable side effect — that's the contract.
}

// TestPersistHook_EmptyAttachmentsIsNoOp asserts that the hook
// short-circuits when there is no JSONB payload to mirror.
func TestPersistHook_EmptyAttachmentsIsNoOp(t *testing.T) {
	hook := PersistHook(&attachments.Repository{}) // no DB → would err
	hook(&telemetry.RequestLogEntry{RequestID: "x"})
	hook(nil)
	// No panic; the empty-payload guard fires before the DB call.
}
