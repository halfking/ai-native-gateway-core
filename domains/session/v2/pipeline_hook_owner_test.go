package v2

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionPersistHook_NoOpAfterDeadCodeRemoval (Round 3, 2026-07-28)
//
// Step 3 Round 1: pipeline_hook.go had a SessionV2Event / EventPublisher pair
// plus a real Publisher.Publish() call inside Execute. The Step 3 design
// says the production V2 write owner is telemetry onPersisted, and the v2
// pipeline must NOT load a second writer. Round 3 strips the dead code
// (EventPublisher / SessionV2Event / real Publish) and turns the hook into
// a compile-compatible shell.
//
// This smoke test pins the new contract:
//   - Execute always returns nil (no Publish side-effect).
//   - Enabled always returns false (no rollout, no shadow_write path).
//   - Constructor tolerates any value (legacy callers still compile).
func TestSessionPersistHook_NoOpAfterDeadCodeRemoval(t *testing.T) {
	hook := NewSessionPersistHook(nil)
	require.NotNil(t, hook)
	assert.Equal(t, "session.persist", hook.Name())
	assert.Equal(t, 70, hook.Priority())

	env := &domain.PipelineRequest{
		Envelope:   &domain.RequestEnvelope{RequestID: "req-r3-1"},
		SessionID:  "session-r3",
		TenantID:   "tenant-r3",
		StatusCode: 200,
	}
	assert.False(t, hook.Enabled(context.Background(), env),
		"Enabled must always be false now that telemetry owns V2 writes")
	assert.NoError(t, hook.Execute(context.Background(), env),
		"Execute must be a no-op (no Publish call)")
}

func TestSessionPersistHook_NilSafe(t *testing.T) {
	assert.NotPanics(t, func() {
		_ = (*SessionPersistHook)(nil).Enabled(context.Background(), nil)
		_ = (*SessionPersistHook)(nil).Execute(context.Background(), nil)
		_ = (*SessionPersistHook)(nil).OnError(context.Background(), nil, nil)
	})
}
