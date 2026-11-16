package streaming

import (
	"net/http"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/stretchr/testify/assert"
)

func TestDispatchAllowModelChangeEnabled_SettingOrEnv(t *testing.T) {
	previous := dispatch.IsModelChangeEnabled()
	t.Cleanup(func() { dispatch.SetModelChangeEnabled(previous) })

	t.Setenv("AUTO_ROUTE_FALLBACK_ENABLED", "")
	dispatch.SetModelChangeEnabled(false)
	assert.False(t, dispatchAllowModelChangeEnabled(), "an explicit runtime opt-out must disable the gate")

	dispatch.SetModelChangeEnabled(true)
	assert.True(t, dispatchAllowModelChangeEnabled(), "runtime setting must apply immediately")

	dispatch.SetModelChangeEnabled(false)
	t.Setenv("AUTO_ROUTE_FALLBACK_ENABLED", "true")
	assert.True(t, dispatchAllowModelChangeEnabled(), "legacy env remains force-on")
}

// TestDispatchAllowModelChangeEnabled_DefaultOn pins the auto-mode contract
// introduced on 2026-11-16: model-level failover is ON by default, because
// auto mode has to survive a dead model instead of handing the error back to
// the client (the "原来的模型不可用" symptom).
//
// The gate itself only opens the model-change ladder; every consumer also
// requires DispatchAllowModelChange / qr.AllowModelChange, whose only source
// is logCtx.IsAutoRequest — so non-auto requests are unaffected.
func TestDispatchAllowModelChangeEnabled_DefaultOn(t *testing.T) {
	previous := dispatch.IsModelChangeEnabled()
	t.Cleanup(func() { dispatch.SetModelChangeEnabled(previous) })

	t.Setenv("AUTO_ROUTE_FALLBACK_ENABLED", "")
	dispatch.SetModelChangeEnabled(true)
	assert.True(t, dispatchAllowModelChangeEnabled(), "model-level failover must be available by default for auto requests")
}

func TestParsePinCredentialHeader(t *testing.T) {
	// Absent → nil.
	assert.Nil(t, parsePinCredentialHeader(&http.Request{Header: http.Header{}}))
	assert.Nil(t, parsePinCredentialHeader(nil))

	// Valid id → pointer to that id.
	r := &http.Request{Header: http.Header{}}
	r.Header.Set("X-LLM-Pin-Credential", "  42  ")
	p := parsePinCredentialHeader(r)
	if assert.NotNil(t, p) {
		assert.Equal(t, 42, *p)
	}

	// Non-numeric / non-positive → nil (ignored, never forces cred 0).
	for _, bad := range []string{"abc", "0", "-3", ""} {
		r2 := &http.Request{Header: http.Header{}}
		r2.Header.Set("X-LLM-Pin-Credential", bad)
		assert.Nil(t, parsePinCredentialHeader(r2), "bad=%q", bad)
	}
}
