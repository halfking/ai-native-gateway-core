package settings

import "testing"

func TestGatewaySpecsPromptBudget(t *testing.T) {
	specs := GatewaySpecs()
	if len(specs) != 1 {
		t.Fatalf("GatewaySpecs() returned %d specs, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Key != "gateway.max_prompt_tokens" {
		t.Fatalf("key = %q", spec.Key)
	}
	if spec.Default != 1048576 {
		t.Fatalf("default = %v, want 1048576", spec.Default)
	}
	if !spec.HotReload {
		t.Fatal("prompt budget must be hot-reloadable")
	}
	if err := spec.Validate(1048576); err != nil {
		t.Fatalf("1M should validate: %v", err)
	}
	if err := spec.Validate(-1); err == nil {
		t.Fatal("negative budget should fail validation")
	}
	if err := spec.Validate(1.5); err == nil {
		t.Fatal("fractional budget should fail validation")
	}
}
