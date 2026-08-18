package summary

import (
	"testing"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestResolveDocumentSummaryModelConfig(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{
		"summary_models.document_summary": jsonString(t, "cheap-document-a,cheap-document-b"),
		"compression.llm_model":           jsonString(t, "generic-fallback"),
	}})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerSummaryTestSpecs(registry)
	settings.Global = registry

	cfg := ResolveModelConfig(appconfig.SummaryDimensionDocumentSummary)
	if cfg.Source != "settings:summary_models.document_summary" {
		t.Fatalf("source = %q, want document summary setting", cfg.Source)
	}
	assertModels(t, cfg.Models, []string{"cheap-document-a", "cheap-document-b"})
}
