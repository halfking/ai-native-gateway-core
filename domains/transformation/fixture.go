package transformation

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// FixtureMetadata identifies the origin and redaction status of a golden fixture.
type FixtureMetadata struct {
	SourceURL         string `json:"source_url"`
	CapturedAt        string `json:"captured_at"`
	APIVersion        string `json:"api_version"`
	Model             string `json:"model"`
	ProviderProfile   string `json:"provider_profile"`
	RedactionVersion  string `json:"redaction_version"`
	FixtureKind       string `json:"fixture_kind"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
}

func LoadFixtureMetadata(path string) (FixtureMetadata, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return FixtureMetadata{}, fmt.Errorf("read fixture metadata: %w", err)
	}
	var meta FixtureMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return FixtureMetadata{}, fmt.Errorf("parse fixture metadata: %w", err)
	}
	return meta, nil
}

func ValidateFixtureMetadata(meta FixtureMetadata) error {
	fields := map[string]string{
		"source_url":        meta.SourceURL,
		"captured_at":       meta.CapturedAt,
		"api_version":       meta.APIVersion,
		"model":             meta.Model,
		"provider_profile":  meta.ProviderProfile,
		"redaction_version": meta.RedactionVersion,
		"fixture_kind":      meta.FixtureKind,
	}
	for name, value := range fields {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("fixture metadata missing %s", name)
		}
	}
	return nil
}
