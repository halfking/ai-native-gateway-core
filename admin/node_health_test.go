package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTimelineSince(t *testing.T) {
	d, err := parseTimelineSince("")
	require.NoError(t, err)
	assert.Equal(t, nodeHealthDefaultSince, d)

	d, err = parseTimelineSince("12h")
	require.NoError(t, err)
	assert.Equal(t, 12*time.Hour, d)

	d, err = parseTimelineSince("3d")
	require.NoError(t, err)
	assert.Equal(t, 72*time.Hour, d)

	d, err = parseTimelineSince("30d")
	require.NoError(t, err)
	assert.Equal(t, nodeHealthMaxSince, d)

	_, err = parseTimelineSince("bogus")
	assert.Error(t, err)

	_, err = parseTimelineSince("0h")
	assert.Error(t, err)
}

func TestMapProbeRunToEvent(t *testing.T) {
	completed := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	directErr := "http_429"
	gatewayErr := "upstream_timeout"

	t.Run("failed prefers direct err", func(t *testing.T) {
		ev := mapProbeRunToEvent(probeRunRow{
			CredentialID:   11,
			RawModelName:   "gpt-5.4",
			TriggerKind:    "periodic",
			Success:        false,
			DirectErrCode:  &directErr,
			GatewayErrCode: &gatewayErr,
			DurationMs:     158,
			StartedAt:      completed.Add(-time.Second),
			CompletedAt:    &completed,
		})
		assert.Equal(t, "11", ev.CredentialID)
		assert.Equal(t, "failed", ev.EventType)
		assert.Equal(t, completed.Format(time.RFC3339Nano), ev.OccurredAt)
		require.NotNil(t, ev.DurationMs)
		assert.Equal(t, 158, *ev.DurationMs)
		require.NotNil(t, ev.ReasonCode)
		assert.Equal(t, "http_429", *ev.ReasonCode)
		require.NotNil(t, ev.Note)
		assert.Equal(t, "gpt-5.4 · periodic", *ev.Note)
	})

	t.Run("success recovered", func(t *testing.T) {
		ev := mapProbeRunToEvent(probeRunRow{
			CredentialID: 12,
			RawModelName: "glm-5.2",
			TriggerKind:  "periodic",
			Success:      true,
			DurationMs:   200,
			StartedAt:    completed,
		})
		assert.Equal(t, "recovered", ev.EventType)
		assert.Nil(t, ev.ReasonCode)
	})

	t.Run("credential_recovery success reconnected", func(t *testing.T) {
		ev := mapProbeRunToEvent(probeRunRow{
			CredentialID: 19,
			RawModelName: "minimax-m3",
			TriggerKind:  "credential_recovery",
			Success:      true,
			StartedAt:    completed,
		})
		assert.Equal(t, "reconnected", ev.EventType)
	})

	t.Run("skips none reason", func(t *testing.T) {
		none := "none"
		ev := mapProbeRunToEvent(probeRunRow{
			CredentialID:  1,
			TriggerKind:   "admin",
			Success:       false,
			DirectErrCode: &none,
			StartedAt:     completed,
		})
		assert.Nil(t, ev.ReasonCode)
	})
}

func TestHandleNodeHealthTimelineValidation(t *testing.T) {
	h := &Handler{} // db nil → 503 after path validation

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/node-health/abc/timeline", nil)
	r.SetPathValue("credential_id", "abc")
	h.handleNodeHealthTimeline(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/admin/node-health/0/timeline", nil)
	r.SetPathValue("credential_id", "0")
	h.handleNodeHealthTimeline(rec, r)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/admin/node-health/11/timeline", nil)
	r.SetPathValue("credential_id", "11")
	h.handleNodeHealthTimeline(rec, r)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/admin/node-health/11/timeline", nil)
	r.SetPathValue("credential_id", "11")
	h.handleNodeHealthTimeline(rec, r)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
}

func TestFormatProbeNote(t *testing.T) {
	assert.Equal(t, "m · t", formatProbeNote("m", "t"))
	assert.Equal(t, "m", formatProbeNote("m", ""))
	assert.Equal(t, "t", formatProbeNote("", "t"))
	assert.Equal(t, "", formatProbeNote("", ""))
}
