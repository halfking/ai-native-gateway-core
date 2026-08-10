package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadSeedJSON[T any](t *testing.T, name string, target *T) {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "omnifree", "seed", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func TestBundledSeedDataContract(t *testing.T) {
	var resources []FreeResourceEntry
	loadSeedJSON(t, "free_resource_catalog.json", &resources)
	if len(resources) != 523 {
		t.Fatalf("resource count = %d, want 523", len(resources))
	}
	resourceKeys := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		key := resource.ProviderCode + "\x00" + resource.ModelID
		if resource.ProviderCode == "" || resource.ModelID == "" {
			t.Fatalf("resource has empty provider/model: %+v", resource)
		}
		if _, exists := resourceKeys[key]; exists {
			t.Fatalf("duplicate resource key %q", key)
		}
		resourceKeys[key] = struct{}{}
		if len(resource.ConstraintsJSON) == 0 || !json.Valid(resource.ConstraintsJSON) {
			t.Fatalf("resource %q has invalid constraints_json", key)
		}
		// tos_verdict must be one of the CHECK-constraint values.
		switch resource.ToSVerdict {
		case "ok", "caution", "ambiguous", "avoid", "unknown":
		default:
			t.Fatalf("resource %q has invalid tos_verdict %q", key, resource.ToSVerdict)
		}
		// free_type must be one of the CHECK-constraint values.
		switch resource.FreeType {
		case "recurring-daily", "recurring-monthly", "one-time-initial", "recurring-credit", "recurring-uncapped", "keyless", "discontinued":
		default:
			t.Fatalf("resource %q has invalid free_type %q", key, resource.FreeType)
		}
		// discontinued entries must be disabled; everything else enabled.
		if resource.FreeType == "discontinued" && resource.Enabled {
			t.Fatalf("discontinued resource %q must have enabled=false", key)
		}
	}

	var templates []AutoComboTemplate
	loadSeedJSON(t, "auto_combo_templates.json", &templates)
	if len(templates) != 6 {
		t.Fatalf("template count = %d, want 6", len(templates))
	}
	templateKeys := make(map[string]struct{}, len(templates))
	for _, template := range templates {
		if template.ComboName == "" || template.DisplayName == "" {
			t.Fatalf("template has empty name/display: %+v", template)
		}
		if _, exists := templateKeys[template.ComboName]; exists {
			t.Fatalf("duplicate template %q", template.ComboName)
		}
		templateKeys[template.ComboName] = struct{}{}
		if len(template.ScoringWeightsJSON) == 0 || !json.Valid(template.ScoringWeightsJSON) {
			t.Fatalf("template %q has invalid scoring_weights_json", template.ComboName)
		}
	}

	var providers []KeylessProvider
	loadSeedJSON(t, "keyless_providers.json", &providers)
	if len(providers) != 8 {
		t.Fatalf("keyless provider count = %d, want 8", len(providers))
	}
	providerKeys := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if provider.ProviderCode == "" || provider.DisplayName == "" {
			t.Fatalf("keyless provider has empty code/display: %+v", provider)
		}
		if _, exists := providerKeys[provider.ProviderCode]; exists {
			t.Fatalf("duplicate keyless provider %q", provider.ProviderCode)
		}
		providerKeys[provider.ProviderCode] = struct{}{}
		if provider.RPMLimit <= 0 || provider.RPDLimit <= 0 || provider.ConcurrentLimit <= 0 {
			t.Fatalf("provider %q has invalid limits", provider.ProviderCode)
		}
	}
}
