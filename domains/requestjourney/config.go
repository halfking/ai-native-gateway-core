package requestjourney

import (
	"reflect"
	"time"
)

const (
	// DefaultTotalRequestCapacity is the observation-projection capacity of
	// the total (cross-model) FIFO view.
	DefaultTotalRequestCapacity = 100
	// DefaultPerModelCapacity is the observation-projection capacity of each
	// per-model FIFO view. 30 since 会话优化 v4 (was 100): it is a DISPLAY
	// bound only, explicitly distinct from the dispatch scheduling bound
	// (300, llmgw_dispatch_max_queue_depth) and the request registry bound
	// (1000, llmgw_dispatch_registry_capacity). Being squeezed out of the
	// projection never affects execution.
	DefaultPerModelCapacity = 30
	// DefaultPerNodeCapacity is the observation-projection capacity of each
	// per-node FIFO view.
	DefaultPerNodeCapacity = 100
	DefaultDetailTTL       = 24 * time.Hour

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
	if isNilConfigSource(source) {
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

// isNilConfigSource also catches a nil pointer stored in IntConfigSource. This
// happens when optional *hotconfig.Config initialization fails: Go considers
// the interface non-nil, but calling GetInt would dereference the nil pointer.
func isNilConfigSource(source IntConfigSource) bool {
	if source == nil {
		return true
	}
	v := reflect.ValueOf(source)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
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
