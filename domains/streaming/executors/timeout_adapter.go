package executors

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
)

// TimeoutConfigAdapter adapts config.TimeoutConfig to implement TimeoutCalculator
type TimeoutConfigAdapter struct {
	config *config.TimeoutConfig
}

// NewTimeoutConfigAdapter creates a new adapter
func NewTimeoutConfigAdapter(cfg *config.TimeoutConfig) *TimeoutConfigAdapter {
	return &TimeoutConfigAdapter{config: cfg}
}

// Calculate implements TimeoutCalculator interface
func (a *TimeoutConfigAdapter) Calculate(input AdaptiveTimeoutInput) time.Duration {
	// Map AdaptiveTimeoutInput to TimeoutCalculationInput
	contextTokens := input.RequestSize / 4 // rough estimate: 1 token ≈ 4 chars

	// Get historical latency from RecentTTFB
	historicalLatency := 0
	if input.RecentTTFB != nil {
		historicalLatency = int(input.RecentTTFB.Milliseconds())
	}

	// Calculate effective timeout
	result := a.config.CalculateEffectiveTimeout(config.TimeoutCalculationInput{
		ContextSizeTokens:   contextTokens,
		HistoricalLatencyMS: historicalLatency,
	})

	return time.Duration(result.EffectiveTimeoutSeconds) * time.Second
}
