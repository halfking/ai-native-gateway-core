package licensing

// R36 (2026-09-17 audit) — the FREE- machine-binding guard must run BEFORE
// Activator.Activate. The R34 fix placed it after, so a foreign hardware_hash
// first occupied the single seat inside DeviceManager.ActivateDevice (no
// rollback) and only then got 403, leaving the real machine with 409
// need_deactivate. This pin asserts: mismatched hash → 403 AND no seat taken.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleActivate_FreeLicenseForeignMachineRejectedBeforeSeatTaken(t *testing.T) {
	store := newFakeStore()
	crypto := newTestCrypto(t)
	validator := NewValidator(crypto, store)
	dm := NewDeviceManager(store, validator)
	h := &BootstrapHandler{
		Activator: NewActivator(crypto, store, dm),
		Store:     store,
	}

	body, _ := json.Marshal(map[string]string{
		"license_key":   "FREE-foreignmachine",
		"hardware_hash": "definitely-not-this-machine-fingerprint",
		"device_name":   "attacker-box",
	})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.handleActivate(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, "foreign hardware_hash on FREE- key must be rejected with 403")

	// The seat must NOT have been occupied by the rejected activation —
	// on the pre-R36 ordering, ActivateDevice ran first and registered the
	// device before the 403 came back.
	store.mu.Lock()
	devices := len(store.devices["FREE-foreignmachine"])
	store.mu.Unlock()
	assert.Zero(t, devices, "rejected activation must not consume a device seat")
}
