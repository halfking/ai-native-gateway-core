package metricsstore

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config controls the space between projection rounds and the work per round.
type Config struct {
	Interval         time.Duration
	BatchSize        int
	MaxRoundsPerTick int
}

func DefaultConfig() Config {
	return Config{Interval: 15 * time.Second, BatchSize: 1000, MaxRoundsPerTick: 1}
}

// ConfigFromEnv applies the request metrics worker's existing runtime settings.
func ConfigFromEnv(base Config) Config {
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_METRICS_INTERVAL")); raw != "" {
		if interval, err := time.ParseDuration(raw); err == nil && interval > 0 {
			base.Interval = interval
		} else {
			slog.Warn("invalid request metrics interval", "setting", "LLM_GATEWAY_METRICS_INTERVAL", "default", base.Interval)
		}
	}
	base.BatchSize = boundedEnvInt("LLM_GATEWAY_METRICS_BATCH_SIZE", base.BatchSize, 1_000_000)
	base.MaxRoundsPerTick = boundedEnvInt("LLM_GATEWAY_METRICS_MAX_ROUNDS", base.MaxRoundsPerTick, 200)
	return base
}

func boundedEnvInt(key string, fallback, upperLimit int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if value, err := strconv.Atoi(raw); err == nil && value > 0 {
		if value > upperLimit {
			slog.Warn("request metrics setting capped", "setting", key, "maximum", upperLimit)
		}
		return min(value, upperLimit)
	}
	slog.Warn("invalid request metrics setting", "setting", key, "default", fallback)
	return fallback
}
