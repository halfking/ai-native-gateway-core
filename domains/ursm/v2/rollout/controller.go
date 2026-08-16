package rollout

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type Config struct {
	Mode             api.RolloutMode
	CanaryPercent    int
	CanaryTenants    []string
	CanaryModels     []string
	ShadowSampleRate float64
	// ShadowDoubleWrite opts shadow mode into writing sidecar records to
	// the v2 store. Default off — by design shadow mode is silent so the
	// v2 Redis namespace stays clean until cutover. P0-3 (audit §7.1)
	// turns this on during the 7-day comparison window: routing still
	// reads legacy credentialstate.Manager, but every success/failure
	// ALSO writes to URSM v2 so we can diff the two after the run.
	//
	// When this is true, ModeShadow returns ShouldUseV2==true and the
	// executor's RecordRequest sidecar fires. Routing still picks
	// LegacyStateBackend because selectStateBackendWithReady only
	// promotes v2 in ModeAuthoritative. So ShadowDoubleWrite is a
	// record-only change; traffic behavior is unchanged.
	ShadowDoubleWrite bool
}

type Controller struct{ cfg Config }

func New(cfg Config) *Controller { return &Controller{cfg: cfg} }

func (c *Controller) Mode() api.RolloutMode { return c.cfg.Mode }

// ShadowDoubleWrite returns whether shadow mode is currently writing
// sidecar records to v2. Used by the executor / metrics layer to
// distinguish "shadow mode is on but quiet" from "shadow mode is on
// and double-writing for the 7-day cutover comparison".
func (c *Controller) ShadowDoubleWrite() bool {
	if c == nil {
		return false
	}
	return c.cfg.ShadowDoubleWrite
}

func (c *Controller) ShadowSampleRate() float64 {
	if c == nil {
		return 0
	}
	return c.cfg.ShadowSampleRate
}

// ShouldSampleShadow deterministically selects requests for observe-only v2
// planning. Outcome double-writes are controlled separately and remain 100%.
func (c *Controller) ShouldSampleShadow(tenant, model, requestID string) bool {
	if c == nil || (c.cfg.Mode != api.ModeShadow && c.cfg.Mode != api.ModeCanary) || c.cfg.ShadowSampleRate <= 0 {
		return false
	}
	if c.cfg.ShadowSampleRate >= 1 {
		return true
	}
	h := sha256.Sum256([]byte(tenant + "|" + model + "|" + requestID))
	v := binary.BigEndian.Uint32(h[:4]) % 1_000_000
	return float64(v)/1_000_000 < c.cfg.ShadowSampleRate
}

func (c *Controller) ShouldUseV2(tenant, model, requestID string) bool {
	if c == nil {
		return false
	}
	switch c.cfg.Mode {
	case api.ModeOff:
		return false
	case api.ModeAuthoritative:
		return true
	case api.ModeShadow:
		// Default false (shadow is silent). Flipped to true ONLY when
		// ShadowDoubleWrite is on — the P0-3 cutover-comparison window.
		// Routing still uses the legacy state manager because
		// selectStateBackendWithReady only trusts v2 in ModeAuthoritative.
		return c.cfg.ShadowDoubleWrite
	case api.ModeCanary:
		return c.inCanary(tenant, model, requestID)
	}
	return false
}

func (c *Controller) inCanary(tenant, model, requestID string) bool {
	for _, t := range c.cfg.CanaryTenants {
		if t == tenant {
			return true
		}
	}
	for _, m := range c.cfg.CanaryModels {
		if m == model {
			return true
		}
	}
	if c.cfg.CanaryPercent <= 0 {
		return false
	}
	if c.cfg.CanaryPercent >= 100 {
		return true
	}
	h := sha256.Sum256([]byte(tenant + "|" + model + "|" + requestID))
	v := binary.BigEndian.Uint32(h[:4]) % 100
	return int(v) < c.cfg.CanaryPercent
}
