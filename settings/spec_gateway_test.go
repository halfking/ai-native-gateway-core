package settings

import "testing"

func TestPlatformSpecsIncludesGatewayPromptBudget(t *testing.T) {
	for _, spec := range PlatformSpecs() {
		if spec.Key == "gateway.max_prompt_tokens" {
			return
		}
	}
	t.Fatal("PlatformSpecs() does not include gateway.max_prompt_tokens")
}

func TestGatewaySpecsPromptBudget(t *testing.T) {
	specs := GatewaySpecs()
	if len(specs) == 0 {
		t.Fatal("GatewaySpecs() returned empty list")
	}
	var spec *Spec
	for _, s := range specs {
		if s.Key == "gateway.max_prompt_tokens" {
			spec = s
			break
		}
	}
	if spec == nil {
		t.Fatal("gateway.max_prompt_tokens not found in GatewaySpecs()")
	}
	if spec.Default != 2097152 {
		t.Fatalf("default = %v, want 2097152", spec.Default)
	}
	if !spec.HotReload {
		t.Fatal("prompt budget must be hot-reloadable")
	}
	if err := spec.Validate(2097152); err != nil {
		t.Fatalf("2M should validate: %v", err)
	}
	if err := spec.Validate(-1); err == nil {
		t.Fatal("negative budget should fail validation")
	}
	if err := spec.Validate(1.5); err == nil {
		t.Fatal("fractional budget should fail validation")
	}
}
