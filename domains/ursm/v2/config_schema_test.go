package v2

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

func TestLoadFromEnvKeySchemaMode(t *testing.T) {
	// Unset ⇒ legacy: the migration never flips a default silently.
	c := LoadFromEnv()
	if c.KeySchemaMode != store.KeySchemaModeLegacy {
		t.Fatalf("default key schema mode = %v, want legacy", c.KeySchemaMode)
	}
	t.Setenv("URSM_V2_KEY_SCHEMA_MODE", "dual")
	if c := LoadFromEnv(); c.KeySchemaMode != store.KeySchemaModeDual {
		t.Fatalf("dual env = %v", c.KeySchemaMode)
	}
	t.Setenv("URSM_V2_KEY_SCHEMA_MODE", "canonical")
	if c := LoadFromEnv(); c.KeySchemaMode != store.KeySchemaModeCanonical {
		t.Fatalf("canonical env = %v", c.KeySchemaMode)
	}
	// A typo must fail validation instead of silently booting legacy.
	t.Setenv("URSM_V2_KEY_SCHEMA_MODE", "k2")
	if err := LoadFromEnv().Validate(); err == nil {
		t.Fatal("invalid URSM_V2_KEY_SCHEMA_MODE must fail validation")
	}
}
