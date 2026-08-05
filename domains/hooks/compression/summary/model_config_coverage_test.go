// Package summary - model_config_coverage_test.go
//
// Coverage-gap tests for AsSummaryConfig and decodeSettingString.

package summary

import (
	"encoding/json"
	"testing"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
)

func TestAsSummaryConfig(t *testing.T) {
	mc := ModelConfig{
		Dimension: appconfig.SummaryDimensionDecisions,
		Key:       "summary_models.decisions",
		Source:    "settings:summary_models.decisions",
		Models:    []string{"gpt-4o", "gpt-4o-mini"},
	}
	sc := mc.AsSummaryConfig()
	if sc.Dimension != mc.Dimension {
		t.Errorf("Dimension = %v, want %v", sc.Dimension, mc.Dimension)
	}
	if len(sc.Models) != 2 || sc.Models[0] != "gpt-4o" || sc.Models[1] != "gpt-4o-mini" {
		t.Errorf("Models = %v, want [gpt-4o gpt-4o-mini]", sc.Models)
	}
	// Verify it's a copy, not a shared slice.
	sc.Models[0] = "mutated"
	if mc.Models[0] == "mutated" {
		t.Error("AsSummaryConfig returned a shared slice; expected independent copy")
	}
}

func TestAsSummaryConfig_EmptyModels(t *testing.T) {
	mc := ModelConfig{Dimension: appconfig.SummaryDimensionKeywords}
	sc := mc.AsSummaryConfig()
	if len(sc.Models) != 0 {
		t.Errorf("Models = %v, want empty", sc.Models)
	}
}

func TestDecodeSettingString_JSONQuoted(t *testing.T) {
	b, _ := json.Marshal("gpt-4o-mini")
	got := decodeSettingString(b)
	if got != "gpt-4o-mini" {
		t.Errorf("decodeSettingString(JSON-quoted) = %q, want %q", got, "gpt-4o-mini")
	}
}

func TestDecodeSettingString_RawString(t *testing.T) {
	got := decodeSettingString([]byte("gpt-4o-mini"))
	if got != "gpt-4o-mini" {
		t.Errorf("decodeSettingString(raw) = %q, want %q", got, "gpt-4o-mini")
	}
}

func TestDecodeSettingString_Empty(t *testing.T) {
	got := decodeSettingString([]byte{})
	if got != "" {
		t.Errorf("decodeSettingString(empty) = %q, want empty", got)
	}
}

func TestDecodeSettingString_Whitespace(t *testing.T) {
	b, _ := json.Marshal("  gpt-4o  ")
	got := decodeSettingString(b)
	if got != "gpt-4o" {
		t.Errorf("decodeSettingString(whitespace) = %q, want %q", got, "gpt-4o")
	}
}
