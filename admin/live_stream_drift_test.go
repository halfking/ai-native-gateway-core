package admin

import (
	"testing"
)

// TestClassifyModelCategoryFallback_Minimax covers the 2026-07-09 fix for
// 问题3: "minimax-m3" used to fall through to "" because the fallback
// classifier had no "minimax" rule (it only matched "mimo" → xiaomi). The
// vendor (原厂) swim lane then fell back to the model name itself, so the
// provider dimension showed both "MiniMax" (from credential lookup) and
// "minimax-m3" (model-name fallback) as two separate lanes.
func TestClassifyModelCategoryFallback_Minimax(t *testing.T) {
	cases := map[string]string{
		"minimax-m3":            "minimax",
		"MiniMax-M3":            "minimax",
		"minimax-m2.7":          "minimax",
		"minimaxai/minimax-m3":  "minimax",
		// mimo must still map to xiaomi and not be shadowed by the new rule.
		"mimo-v2.5-pro": "xiaomi",
	}
	for model, want := range cases {
		if got := classifyModelCategoryFallback(model); got != want {
			t.Errorf("classifyModelCategoryFallback(%q)=%q, want %q", model, got, want)
		}
	}
}

// TestInferVendorFromModel_Minimax mirrors the above for the second vendor
// classifier (live_stream_redis_store.go). Both must agree so the vendor
// dimension never degrades to the model name.
func TestInferVendorFromModel_Minimax(t *testing.T) {
	cases := map[string]string{
		"minimax-m3":   "minimax",
		"MiniMax-M3":   "minimax",
		"mimo-v2.5":    "xiaomi",
		"glm-5.2":      "zhipu",
		"claude-opus-4": "anthropic",
	}
	for model, want := range cases {
		if got := InferVendorFromModel(model); got != want {
			t.Errorf("InferVendorFromModel(%q)=%q, want %q", model, got, want)
		}
	}
}

// TestLiveStreamDimensionKey_ProviderNeverUsesModelName is the core regression
// guard for 问题3: the provider (供应商) dimension must ONLY come from the
// credential → provider reverse lookup (ProviderCode). When ProviderCode is
// empty/unknown, the request must NOT appear in the provider dimension at all
// (empty key) — never the model name. Otherwise the same provider shows up as
// two lanes: the real provider name and a model-name lane.
func TestLiveStreamDimensionKey_ProviderNeverUsesModelName(t *testing.T) {
	// Provider resolved through credential lookup → real provider name.
	got := liveStreamDimensionKey("provider", LiveRequest{
		ProviderCode:  "MiniMax",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "MiniMax" {
		t.Errorf("provider dimension with resolved ProviderCode: got %q, want %q", got, "MiniMax")
	}

	// ProviderCode missing (credential_id==0 / credential deleted): the model
	// name must NOT leak into the provider dimension.
	got = liveStreamDimensionKey("provider", LiveRequest{
		ProviderCode:  "",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "" {
		t.Errorf("provider dimension must be empty when ProviderCode is missing, got %q (model name leaked)", got)
	}

	// Same for the "unknown" sentinel.
	got = liveStreamDimensionKey("provider", LiveRequest{
		ProviderCode:  "unknown",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "" {
		t.Errorf("provider dimension must be empty when ProviderCode is unknown, got %q", got)
	}
}

// TestLiveStreamDimensionKey_ModelStillShowsModelName confirms the model name
// still appears in the MODEL dimension (the fix only stops it leaking into the
// provider dimension).
func TestLiveStreamDimensionKey_ModelStillShowsModelName(t *testing.T) {
	got := liveStreamDimensionKey("model", LiveRequest{
		ProviderCode:  "",
		Model:         "minimax-m3",
		CanonicalName: "minimax-m3",
	})
	if got != "minimax-m3" {
		t.Errorf("model dimension should still show the model name, got %q", got)
	}
}
