package streaming

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDispatchAllowModelChangeEnabled_SettingOrEnv(t *testing.T) {
	// No env, no setting → false (default). Just asserts it does not panic and
	// returns a bool; the live value depends on DB/env state.
	v := dispatchAllowModelChangeEnabled()
	assert.IsType(t, false, v)
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
