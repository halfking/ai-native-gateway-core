package v2

import (
	"os"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

// Config is the static, mode-toggled configuration for the v2 facade.
// Always construct via DefaultConfig(); the zero value is not safe
// (RedisKeyPrefix is empty and Mode is "" rather than api.ModeOff).
type Config struct {
	Mode                api.RolloutMode
	CanaryPercent       int
	CanaryTenants       []string
	CanaryModels        []string
	ShadowSampleRate    float64
	RecordTimeoutMs     int
	RecoveryBlockOnMiss bool
	PersistIntervalSec  int
	RedisKeyPrefix      string
	Window1mTTL         time.Duration
	Window5mTTL         time.Duration
	Window30mTTL        time.Duration
	NodeTTL             time.Duration
}

func DefaultConfig() Config {
	return Config{
		Mode:                api.ModeOff,
		CanaryPercent:       0,
		ShadowSampleRate:    0.01,
		RecordTimeoutMs:     20,
		RecoveryBlockOnMiss: true,
		PersistIntervalSec:  60,
		RedisKeyPrefix:      "ursm:v2:",
		Window1mTTL:         90 * time.Second,
		Window5mTTL:         6 * time.Minute,
		Window30mTTL:        35 * time.Minute,
		NodeTTL:             60 * time.Minute,
	}
}

func LoadFromEnv() Config {
	c := DefaultConfig()
	if v := os.Getenv("URSM_V2_MODE"); v != "" {
		c.Mode = api.RolloutMode(v)
	}
	if v := os.Getenv("URSM_V2_CANARY_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.CanaryPercent = n
		}
	}
	return c
}
