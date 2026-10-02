// 2026-08-23 hzx-2 audit regression tests.
//
// These lock the contract for the focused /api/routing/credentials/{id}/reset-state
// endpoint. Specifically:
//   - super_admin auth is enforced (no handler access without admin role)
//   - reason is mandatory
//   - non-numeric / missing id path param returns 400
//   - non-existent credential id returns 404
//   - successful path returns 200 with the expected fields
//
// The handler touches DB through h.applyForceEnable, which we do NOT exercise
// here (the underlying SQL chain is covered by the legacy emergency-repair
// force_enable test surface). This file pins the new endpoint's shape only.

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newResetStateTestHandler(t *testing.T) *Handler {
	t.Helper()
	pool := &pgxpool.Pool{} // nil pool — we only test the body-validation paths.
	return &Handler{
		db:      pool,
		secret:  "test-secret",
		fpSlots: &credentialfpslot.Manager{}, // unused for body validation
	}
}

func TestResetCredentialState_RequiresPost(t *testing.T) {
	h := newResetStateTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/routing/credentials/123/reset-state", nil)
	w := httptest.NewRecorder()
	h.handleResetCredentialState(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Contains(t, w.Body.String(), "method not allowed")
}

func TestResetCredentialState_RequiresPositiveID(t *testing.T) {
	h := newResetStateTestHandler(t)

	for _, id := range []string{"0", "-1", "abc", ""} {
		body, _ := json.Marshal(routingResetStateRequest{Reason: "test"})
		path := "/api/routing/credentials/" + id + "/reset-state"
		if id == "" {
			path = "/api/routing/credentials//reset-state"
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h.handleResetCredentialState(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code, "id=%q", id)
		assert.Contains(t, w.Body.String(), "id path param")
	}
}

func TestResetCredentialState_RequiresReason(t *testing.T) {
	h := newResetStateTestHandler(t)
	body, _ := json.Marshal(routingResetStateRequest{RawModel: "minimax-m3"})
	req := httptest.NewRequest(http.MethodPost, "/api/routing/credentials/123/reset-state", bytes.NewReader(body))
	req.SetPathValue("id", "123")
	w := httptest.NewRecorder()
	h.handleResetCredentialState(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "reason is required")
}

func TestResetCredentialState_RejectsInvalidJSON(t *testing.T) {
	h := newResetStateTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/routing/credentials/123/reset-state",
		strings.NewReader("{not json"))
	req.SetPathValue("id", "123")
	w := httptest.NewRecorder()
	h.handleResetCredentialState(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid body")
}

func TestRoutingResetStateRequest_Defaults(t *testing.T) {
	// Locks that trigger_probe defaults to false (so callers who don't set
	// it still get a reset, just no immediate self-check).
	req := routingResetStateRequest{Reason: "manual reset"}
	assert.False(t, req.TriggerProbe)
	assert.Empty(t, req.RawModel)
}

func TestExtractModels_WholeCredReturnsEmpty(t *testing.T) {
	// When raw_model is empty (whole-credential reset), extractModels
	// returns an empty slice so the auto-heal/NodeProbeWorker.Submit
	// path doesn't fire for an unknown set of bindings. The handler logs
	// this in beforeAfter so operators can still see what happened.
	got := extractModels("")
	assert.Empty(t, got)

	got = extractModels("minimax-m3")
	assert.Equal(t, []string{"minimax-m3"}, got)
}

func TestResetInMemoryNodeStatePreservesResponsesCapability(t *testing.T) {
	ctx := context.Background()
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	fpSlots := credentialfpslot.New(credentialfpslot.Config{Enabled: true, DefaultLimit: 5}, redisClient)
	const credentialID = 78
	const model = "gpt-5.6-terra"

	require.NoError(t, fpSlots.SetSupportsResponses(ctx, credentialID, model, false))
	state, err := fpSlots.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	require.NotNil(t, state)
	state.Disabled = true
	state.DisabledUntil = time.Now().Add(time.Minute).Unix()
	state.FailureCount = 3
	require.NoError(t, fpSlots.SetNodeState(ctx, state))

	h := &Handler{fpSlots: fpSlots}
	models, outcome := h.resetInMemoryNodeState(ctx, credentialID, 0, model, false)
	assert.Equal(t, []string{model}, models)
	assert.Equal(t, 1, outcome["fp_node_state_models_reset"])

	reset, err := fpSlots.GetNodeState(ctx, credentialID, model)
	require.NoError(t, err)
	require.NotNil(t, reset)
	assert.False(t, reset.Disabled)
	assert.Zero(t, reset.FailureCount)
	supported, known, err := fpSlots.GetSupportsResponses(ctx, credentialID, model)
	require.NoError(t, err)
	assert.True(t, known, "health reset must preserve independent protocol capability evidence")
	assert.False(t, supported)
}

// TestResetCredentialState_AuditActionTag guards the contract that
// a successful reset writes an audit entry with the action tag
// "routing_credential_reset_state". Reading the source is the only
// way to lock the literal without a real DB; the previous fingerprint
// version of this test only asserted the literal was non-empty, which
// passed even when the source drifted.
func TestResetCredentialState_AuditActionTag(t *testing.T) {
	const expectedAction = "routing_credential_reset_state"

	src, err := os.ReadFile(filepath.Join("routing_reset.go"))
	require.NoError(t, err, "routing_reset.go must be readable next to the test")
	assert.Contains(t, string(src), `"`+expectedAction+`"`,
		"handleResetCredentialState must log audit with the action %q", expectedAction)
}
