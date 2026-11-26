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
		BaseDelay: 5 * time.Minute,
		// ★ 2026-11-26（探测回退周期上限轮）：2h → 1h，与通用梯末档一致。
		MaxDelay:   1 * time.Hour,
		Multiplier: 2.0,
		Jitter:     30 * time.Second,
	}
}

func LoadProbeBackoffConfig() ProbeBackoffConfig {
	return ProbeBackoffConfig{
		BaseDelay:  time.Duration(settings.GetPlatformInt("probe.backoff_base_seconds", 300)) * time.Second,
		MaxDelay:   time.Duration(settings.GetPlatformInt("probe.backoff_max_seconds", 3600)) * time.Second,
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
	// NodeProbeBackoffChain defines the retry intervals for consecutive probe failures.
	//
	// 2026-07-24: changed from [5s,30s,60s,5m,1h,2h,24h] to
	// [5s,30s,60s,5m,1h,2h,6h].
	//
	// ★ 2026-11-26（探测回退周期上限轮）：末两级（2h / 6h）被移除 ⇒ 链现为
	// [5s, 30s, 60s, 5m, 1h]。**形状不变**（仍是逐层回退），只是封顶从 6h
	// 收到 1h ⇒ ⇒ 「探测回退周期：根据现有的规则逐层回退，最多不超过 1
	// 小时」这条口径在**通用梯**上落地。
	//
	//   · 失败次数 ≥ 5 后 ChainBackoffIndex 停在末档（1h）⇒ 任何持续失败
	//     的对，**下一次自动探测最多等 1 小时**（原先最多等 6 小时）。
	//   · 链长从 7 变 5 ⇒ nodeProbeMaxAttempts 必须同步（bg/node_probe.go
	//     里那条常量与链长是绑定关系，注释写明「matches the length of
	//     NodeProbeBackoffChain」）⇒ ⇒ 一代队列任务的 attempt 上限同步收到5。
	//   · ★ 连带影响：bg/probe_recovery_policy.go 的 networkProbeBackoff
	//     取的是本链的尾段 tail := NodeProbeBackoffChain[3:] ⇒ 尾段由
	//     [5m,1h,2h,6h] 变成 [5m,1h] ⇒ 网络长尾同样封顶 1h（再被
	//     probe.recovery_max_backoff_seconds 默认 300s 削到 5m）。
	//
	// Rationale for the 6h cap originally: 24h was too long, stranding nodes
	// for a full day; 6h balanced backoff pressure against recovery speed.
	// The 1h cap keeps that spirit (timely re-verification) while honoring the
	// operator's explicit "at most 1 hour" ceiling.
	NodeProbeBackoffChain = []time.Duration{
		5 * time.Second,
		30 * time.Second,
		60 * time.Second,
		5 * time.Minute,
		1 * time.Hour,
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
