// timeout_adapter.go — 自适应超时计算器实现
package executors

import (
	"math"
	"strings"
	"time"
)

// TimeoutAdapter 自适应超时计算器
type TimeoutAdapter struct {
	BaseTimeout         time.Duration
	MinTimeout          time.Duration
	MaxTimeout          time.Duration
	SizeThresholdSmall  int
	SizeThresholdLarge  int
	SizeMultiplierSmall float64
	SizeMultiplierLarge float64
	ProviderMultipliers map[string]float64
}

// NewTimeoutAdapter 创建默认配置的自适应超时计算器
func NewTimeoutAdapter() *TimeoutAdapter {
	return &TimeoutAdapter{
		BaseTimeout:         30 * time.Second,
		MinTimeout:          15 * time.Second,
		MaxTimeout:          120 * time.Second,
		SizeThresholdSmall:  100 * 1024,
		SizeThresholdLarge:  500 * 1024,
		SizeMultiplierSmall: 0.5,
		SizeMultiplierLarge: 2.0,
		ProviderMultipliers: map[string]float64{
			"minimaxi.com":  1.5,
			"anthropic.com": 1.2,
			"claude.ai":     1.2,
			"openai.com":    1.0,
			"deepseek.com":  0.8,
			"nvidia.com":    1.0,
			"volces.com":    1.0,
		},
	}
}

// Calculate 计算自适应超时
func (a *TimeoutAdapter) Calculate(input AdaptiveTimeoutInput) time.Duration {
	timeout := a.BaseTimeout

	sizeMultiplier := a.calculateSizeMultiplier(input.RequestSize)
	timeout = time.Duration(float64(timeout) * sizeMultiplier)

	providerMultiplier := a.getProviderMultiplier(input.ProviderURL)
	timeout = time.Duration(float64(timeout) * providerMultiplier)

	if input.RecentTTFB != nil {
		historyMultiplier := a.calculateHistoryMultiplier(*input.RecentTTFB)
		timeout = time.Duration(float64(timeout) * historyMultiplier)
	}

	if input.IsSession {
		timeout = time.Duration(float64(timeout) * 1.5)
	}

	if input.IsRetry {
		retryMultiplier := math.Max(0.5, 1.0-float64(input.AttemptNum)*0.2)
		timeout = time.Duration(float64(timeout) * retryMultiplier)
	}

	if timeout < a.MinTimeout {
		timeout = a.MinTimeout
	}
	if timeout > a.MaxTimeout {
		timeout = a.MaxTimeout
	}

	return timeout
}

func (a *TimeoutAdapter) calculateSizeMultiplier(requestSize int) float64 {
	if requestSize < a.SizeThresholdSmall {
		return a.SizeMultiplierSmall
	} else if requestSize > a.SizeThresholdLarge {
		extraKB := (requestSize - a.SizeThresholdLarge) / 1024
		extra100KB := extraKB / 100
		extraMultiplier := float64(extra100KB) * 0.2
		return math.Min(a.SizeMultiplierLarge+extraMultiplier, 4.0)
	}
	return 1.0
}

func (a *TimeoutAdapter) getProviderMultiplier(providerURL string) float64 {
	for pattern, multiplier := range a.ProviderMultipliers {
		if strings.Contains(providerURL, pattern) {
			return multiplier
		}
	}
	return 1.0
}

func (a *TimeoutAdapter) calculateHistoryMultiplier(recentTTFB time.Duration) float64 {
	ratio := float64(recentTTFB) / float64(a.BaseTimeout)
	multiplier := 1.0 + (ratio - 0.5)
	return math.Max(0.5, math.Min(multiplier, 2.0))
}
