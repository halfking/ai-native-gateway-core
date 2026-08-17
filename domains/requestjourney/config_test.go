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
	// v4: per-model projection capacity is 30 (display bound); total/node
	// stay 100. See DefaultPerModelCapacity doc for the capacity layering.
	if got.TotalRequestCapacity != 100 || got.PerModelCapacity != 30 || got.PerNodeCapacity != 100 {
		t.Fatalf("default capacities = %#v", got)
	}
	if got.DetailTTL != 24*time.Hour {
		t.Fatalf("default detail TTL = %s", got.DetailTTL)
	}
}

func TestLoadConfigTypedNilSource(t *testing.T) {
	var source *hotconfig.Config
	got := LoadConfig(source)
	if got != DefaultConfig() {
		t.Fatalf("typed nil source should use defaults: %#v", got)
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

// TestPerModelCapacityDefault30HotUpdate (UT-DQ-08): the v4 default is 30
// and the hotconfig key live-overrides it (total/node remain 100).
func TestPerModelCapacityDefault30HotUpdate(t *testing.T) {
	if DefaultPerModelCapacity != 30 {
		t.Fatalf("DefaultPerModelCapacity = %d, want 30 (v4)", DefaultPerModelCapacity)
	}
	if got := DefaultConfig(); got.TotalRequestCapacity != 100 || got.PerNodeCapacity != 100 {
		t.Fatalf("total/node defaults must stay 100: %#v", got)
	}
	got := LoadConfig(configValues{HotKeyPerModelCapacity: 45})
	if got.PerModelCapacity != 45 {
		t.Fatalf("PerModelCapacity hot update = %d, want 45", got.PerModelCapacity)
	}
}
