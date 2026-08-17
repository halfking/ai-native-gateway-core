package requestjourney

import "time"

const (
	DefaultTotalRequestCapacity = 100
	DefaultPerModelCapacity     = 100
	DefaultPerNodeCapacity      = 100
	DefaultDetailTTL            = 24 * time.Hour

	HotKeyTotalRequestCapacity = "llmgw_request_history_total_capacity"
	HotKeyPerModelCapacity     = "llmgw_request_history_model_capacity"
	HotKeyPerNodeCapacity      = "llmgw_request_history_node_capacity"
	HotKeyDetailTTLSeconds     = "llmgw_request_history_detail_ttl_seconds"

	legacyHotKeyTotalRequestCapacity = "llmgw_request_journey_total_capacity"
	legacyHotKeyPerModelCapacity     = "llmgw_request_journey_per_model_capacity"
	legacyHotKeyPerNodeCapacity      = "llmgw_request_journey_per_node_capacity"
	legacyHotKeyDetailTTLSeconds     = "llmgw_request_journey_detail_ttl_seconds"
)

// IntConfigSource is implemented by hotconfig.Config. Keeping the interface
// local leaves this domain contract independent of the hotconfig package.
type IntConfigSource interface {
	GetInt(key string, defaultValue int) int
}

// Config controls the three in-memory FIFO views and journey detail retention.
type Config struct {
	TotalRequestCapacity int
	PerModelCapacity     int
	PerNodeCapacity      int
	DetailTTL            time.Duration
}

func DefaultConfig() Config {
	return Config{
		TotalRequestCapacity: DefaultTotalRequestCapacity,
		PerModelCapacity:     DefaultPerModelCapacity,
		PerNodeCapacity:      DefaultPerNodeCapacity,
		DetailTTL:            DefaultDetailTTL,
	}
}

// LoadConfig reads live values from hotconfig. Missing, mistyped, zero, and
// negative values all fall back independently to the documented defaults.
func LoadConfig(source IntConfigSource) Config {
	defaults := DefaultConfig()
	if source == nil {
		return defaults
	}
	return Config{
		TotalRequestCapacity: positiveOrDefault(configInt(source, HotKeyTotalRequestCapacity, legacyHotKeyTotalRequestCapacity, defaults.TotalRequestCapacity), defaults.TotalRequestCapacity),
		PerModelCapacity:     positiveOrDefault(configInt(source, HotKeyPerModelCapacity, legacyHotKeyPerModelCapacity, defaults.PerModelCapacity), defaults.PerModelCapacity),
		PerNodeCapacity:      positiveOrDefault(configInt(source, HotKeyPerNodeCapacity, legacyHotKeyPerNodeCapacity, defaults.PerNodeCapacity), defaults.PerNodeCapacity),
		DetailTTL: time.Duration(positiveOrDefault(
			configInt(source, HotKeyDetailTTLSeconds, legacyHotKeyDetailTTLSeconds, int(defaults.DetailTTL/time.Second)),
			int(defaults.DetailTTL/time.Second),
		)) * time.Second,
	}
}

func configInt(source IntConfigSource, key, legacyKey string, defaultValue int) int {
	const missing = -1 << 63
	if value := source.GetInt(key, missing); value != missing {
		return value
	}
	return source.GetInt(legacyKey, defaultValue)
}

func positiveOrDefault(value, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	return value
}
