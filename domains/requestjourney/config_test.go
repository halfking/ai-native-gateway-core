package requestjourney

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/hotconfig"
)

var _ IntConfigSource = (*hotconfig.Config)(nil)

type configValues map[string]int

func (v configValues) GetInt(key string, defaultValue int) int {
	if value, ok := v[key]; ok {
		return value
	}
	return defaultValue
}

func TestLoadConfigDefaults(t *testing.T) {
	got := LoadConfig(nil)
	if got.TotalRequestCapacity != 100 || got.PerModelCapacity != 100 || got.PerNodeCapacity != 100 {
		t.Fatalf("default capacities = %#v", got)
	}
	if got.DetailTTL != 24*time.Hour {
		t.Fatalf("default detail TTL = %s", got.DetailTTL)
	}
}

func TestLoadConfigFromHotConfig(t *testing.T) {
	got := LoadConfig(configValues{
		HotKeyTotalRequestCapacity: 250,
		HotKeyPerModelCapacity:     80,
		HotKeyPerNodeCapacity:      40,
		HotKeyDetailTTLSeconds:     7200,
	})
	if got.TotalRequestCapacity != 250 || got.PerModelCapacity != 80 || got.PerNodeCapacity != 40 {
		t.Fatalf("loaded capacities = %#v", got)
	}
	if got.DetailTTL != 2*time.Hour {
		t.Fatalf("loaded detail TTL = %s", got.DetailTTL)
	}
}

func TestLoadConfigSupportsLegacyJourneyKeys(t *testing.T) {
	got := LoadConfig(configValues{
		legacyHotKeyTotalRequestCapacity: 150,
		legacyHotKeyPerModelCapacity:     120,
		legacyHotKeyPerNodeCapacity:      110,
		legacyHotKeyDetailTTLSeconds:     3600,
	})
	if got.TotalRequestCapacity != 150 || got.PerModelCapacity != 120 || got.PerNodeCapacity != 110 {
		t.Fatalf("legacy capacities = %#v", got)
	}
	if got.DetailTTL != time.Hour {
		t.Fatalf("legacy detail TTL = %s", got.DetailTTL)
	}
}

func TestLoadConfigRejectsNonPositiveHotValues(t *testing.T) {
	got := LoadConfig(configValues{
		HotKeyTotalRequestCapacity: 0,
		HotKeyPerModelCapacity:     -1,
		HotKeyPerNodeCapacity:      0,
		HotKeyDetailTTLSeconds:     -1,
	})
	if got != DefaultConfig() {
		t.Fatalf("invalid values should fall back to defaults: %#v", got)
	}
}
