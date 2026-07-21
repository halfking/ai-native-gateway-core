package rollout

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

type Config struct {
	Mode          api.RolloutMode
	CanaryPercent int
	CanaryTenants []string
	CanaryModels  []string
}

type Controller struct{ cfg Config }

func New(cfg Config) *Controller { return &Controller{cfg: cfg} }

func (c *Controller) Mode() api.RolloutMode { return c.cfg.Mode }

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
		return false
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