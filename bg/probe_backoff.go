package bg

import (
	"math/rand"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

type ProbeBackoffConfig struct {
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Multiplier float64
	Jitter     time.Duration
}

func DefaultProbeBackoffConfig() ProbeBackoffConfig {
	return ProbeBackoffConfig{
		BaseDelay:  5 * time.Minute,
		MaxDelay:   2 * time.Hour,
		Multiplier: 2.0,
		Jitter:     30 * time.Second,
	}
}

func LoadProbeBackoffConfig() ProbeBackoffConfig {
	return ProbeBackoffConfig{
		BaseDelay:  time.Duration(settings.GetPlatformInt("probe.backoff_base_seconds", 300)) * time.Second,
		MaxDelay:   time.Duration(settings.GetPlatformInt("probe.backoff_max_seconds", 7200)) * time.Second,
		Multiplier: settings.GetPlatformFloat("probe.backoff_multiplier", 2.0),
		Jitter:     time.Duration(settings.GetPlatformInt("probe.backoff_jitter_seconds", 30)) * time.Second,
	}
}

func (cfg ProbeBackoffConfig) NextDelay(failures int) time.Duration {
	if failures <= 0 {
		return cfg.MaxDelay
	}
	delay := cfg.BaseDelay
	for i := 1; i < failures; i++ {
		delay = time.Duration(float64(delay) * cfg.Multiplier)
		if delay >= cfg.MaxDelay {
			delay = cfg.MaxDelay
			break
		}
	}
	if cfg.Jitter > 0 {
		j := time.Duration(rand.Int63n(int64(cfg.Jitter)))
		delay += j
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}
	return delay
}

var DefaultBackoff = DefaultProbeBackoffConfig()

func ChainBackoffIndex(failures int, chain []time.Duration) time.Duration {
	if len(chain) == 0 {
		return 0
	}
	if failures < 1 {
		return chain[0]
	}
	idx := failures - 1
	if idx >= len(chain) {
		idx = len(chain) - 1
	}
	return chain[idx]
}

var (
	NodeProbeBackoffChain = []time.Duration{
		5 * time.Second,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
		2 * time.Hour,
		24 * time.Hour,
	}

	ActiveProbeBackoffChain = []time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
	}

	HTTPProbeBackoffChain = []time.Duration{
		0,
		10 * time.Second,
		15 * time.Second,
		30 * time.Second,
	}
)
