package summary

import (
	"encoding/json"
	"testing"

	appconfig "github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/settings"
)

type fakeSettingsBackend struct {
	store map[string][]byte
}

func (f *fakeSettingsBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}
func (f *fakeSettingsBackend) GetTenant(tenantID, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}

func TestResolveModelConfigPriority(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{
		"summary_models.project_context": jsonString(t, "project-a, project-b"),
		"summary_models.fallback":        jsonString(t, "fallback-a"),
		"compression.llm_model":          jsonString(t, "compact-a"),
	}})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerSummaryTestSpecs(registry)
	settings.Global = registry
	t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "env-a")

	cfg := ResolveModelConfig(appconfig.SummaryDimensionProject)
	if cfg.Source != "settings:summary_models.project_context" {
		t.Fatalf("source = %q, want dimension setting", cfg.Source)
	}
	assertModels(t, cfg.Models, []string{"project-a", "project-b"})
}

func TestResolveModelConfigFallbackChain(t *testing.T) {
	t.Run("fallback_setting", func(t *testing.T) {
		prevGlobal := settings.Global
		t.Cleanup(func() { settings.Global = prevGlobal })

		registry := settings.NewRegistry()
		registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{
			"summary_models.fallback": jsonString(t, "fallback-a"),
			"compression.llm_model":   jsonString(t, "compact-a"),
		}})
		registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
		registerSummaryTestSpecs(registry)
		settings.Global = registry

		cfg := ResolveModelConfig(appconfig.SummaryDimensionTechnical)
		if cfg.Source != "settings:summary_models.fallback" {
			t.Fatalf("source = %q, want fallback setting", cfg.Source)
		}
		assertModels(t, cfg.Models, []string{"fallback-a"})
	})

	t.Run("compression_llm_model", func(t *testing.T) {
		prevGlobal := settings.Global
		t.Cleanup(func() { settings.Global = prevGlobal })

		registry := settings.NewRegistry()
		registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{
			"compression.llm_model": jsonString(t, "compact-a,compact-b"),
		}})
		registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
		registerSummaryTestSpecs(registry)
		settings.Global = registry

		cfg := ResolveModelConfig(appconfig.SummaryDimensionTasks)
		if cfg.Source != "settings:compression.llm_model" {
			t.Fatalf("source = %q, want compression.llm_model", cfg.Source)
		}
		assertModels(t, cfg.Models, []string{"compact-a", "compact-b"})
	})

	t.Run("legacy_env", func(t *testing.T) {
		prevGlobal := settings.Global
		t.Cleanup(func() { settings.Global = prevGlobal })

		registry := settings.NewRegistry()
		registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{}})
		registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
		registerSummaryTestSpecs(registry)
		settings.Global = registry
		t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "env-a, env-b")

		cfg := ResolveModelConfig(appconfig.SummaryDimensionProblems)
		if cfg.Source != "env:LLM_GATEWAY_COMPACTION_MODELS" {
			t.Fatalf("source = %q, want legacy env", cfg.Source)
		}
		assertModels(t, cfg.Models, []string{"env-a", "env-b"})
	})
}

func TestResolveModelConfigHotReload(t *testing.T) {
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"summary_models.tasks": jsonString(t, "tasks-a"),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registerSummaryTestSpecs(registry)
	settings.Global = registry

	first := ResolveModelConfig(appconfig.SummaryDimensionTasks)
	assertModels(t, first.Models, []string{"tasks-a"})

	store["summary_models.tasks"] = jsonString(t, "tasks-b,tasks-c")
	second := ResolveModelConfig(appconfig.SummaryDimensionTasks)
	assertModels(t, second.Models, []string{"tasks-b", "tasks-c"})
}

func registerSummaryTestSpecs(registry *settings.Registry) {
	for _, sp := range settings.CompressionSpecs() {
		registry.MustRegisterSpec(sp)
	}
}

func jsonString(t *testing.T, value string) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertModels(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}
