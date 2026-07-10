package compression

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// fakeDBBackend is a minimal in-memory settings backend for testing.
type fakeDBBackend struct {
	store map[string][]byte
}

func (f *fakeDBBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeDBBackend) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}
func (f *fakeDBBackend) GetTenant(tenantID, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeDBBackend) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}

// TestCompactionModelsFromSettings verifies the new settings-driven model
// resolution: the compression.llm_model setting beats the legacy env var,
// which in turn beats the built-in default.
func TestCompactionModelsFromSettings(t *testing.T) {
	// Snapshot Global so we can restore it after the test.
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	// Build a fresh registry with the new spec + an in-memory DB backend.
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeDBBackend{
		store: map[string][]byte{
			"compression.llm_model": []byte(`"gpt-5-mega-2,claude-sonnet-99"`),
		},
	})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	registry.MustRegisterSpec(&settings.Spec{
		Key:      "compression.llm_model",
		Type:     settings.TypeString,
		Scope:    settings.ScopePlatform,
		Default:  "minimax-text-01,gemini-2.5-flash",
	})
	settings.Global = registry

	got := compactionModelsFromSettings()
	want := []string{"gpt-5-mega-2", "claude-sonnet-99"}
	if len(got) != len(want) {
		t.Fatalf("got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

// TestCompactionModelsResolutionOrder covers the full fallback chain:
// settings → env → built-in default.
func TestCompactionModelsResolutionOrder(t *testing.T) {
	t.Run("settings_beats_env", func(t *testing.T) {
		prevGlobal := settings.Global
		t.Cleanup(func() { settings.Global = prevGlobal })

		registry := settings.NewRegistry()
		registry.RegisterBackend(settings.ScopePlatform, &fakeDBBackend{
			store: map[string][]byte{
				"compression.llm_model": []byte(`"from-settings"`),
			},
		})
		registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
		registry.MustRegisterSpec(&settings.Spec{
			Key: "compression.llm_model", Type: settings.TypeString, Scope: settings.ScopePlatform,
		})
		settings.Global = registry
		// Legacy env should be ignored when settings has a value.
		t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "from-env")

		got := compactionModelsFromEnv()
		if len(got) != 1 || got[0] != "from-settings" {
			t.Errorf("settings should win, got %v", got)
		}
	})

	t.Run("env_when_settings_empty", func(t *testing.T) {
		prevGlobal := settings.Global
		t.Cleanup(func() { settings.Global = prevGlobal })

		registry := settings.NewRegistry()
		registry.RegisterBackend(settings.ScopePlatform, &fakeDBBackend{store: map[string][]byte{}})
		registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
		registry.MustRegisterSpec(&settings.Spec{
			Key: "compression.llm_model", Type: settings.TypeString, Scope: settings.ScopePlatform,
		})
		settings.Global = registry
		t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", " env-only-model ")

		got := compactionModelsFromEnv()
		if len(got) != 1 || got[0] != "env-only-model" {
			t.Errorf("env fallback failed, got %v", got)
		}
	})

	t.Run("built_in_default_when_nothing_set", func(t *testing.T) {
		prevGlobal := settings.Global
		t.Cleanup(func() { settings.Global = prevGlobal })

		registry := settings.NewRegistry()
		registry.RegisterBackend(settings.ScopePlatform, &fakeDBBackend{store: map[string][]byte{}})
		registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
		registry.MustRegisterSpec(&settings.Spec{
			Key: "compression.llm_model", Type: settings.TypeString, Scope: settings.ScopePlatform,
		})
		settings.Global = registry
		// Ensure legacy env is empty.
		t.Setenv("LLM_GATEWAY_COMPACTION_MODELS", "")

		got := compactionModelsFromEnv()
		if len(got) != 2 || got[0] != "minimax-text-01" || got[1] != "gemini-2.5-flash" {
			t.Errorf("built-in default failed, got %v", got)
		}
	})
}
