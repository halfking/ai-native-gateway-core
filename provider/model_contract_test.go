package provider

import (
	"strings"
	"testing"
)

func TestModelKnownSQLUsesCanonicalEquality(t *testing.T) {
	for _, forbidden := range []string{
		"lower(",
		"raw_model_name",
	} {
		if strings.Contains(strings.ToLower(modelKnownSQL), forbidden) {
			t.Fatalf("modelKnownSQL contains forbidden %q: %s", forbidden, modelKnownSQL)
		}
	}
	for _, required := range []string{
		"canonical_raw_name = $1",
		"standardized_name = $1",
		"raw_name = $1",
	} {
		if !strings.Contains(modelKnownSQL, required) {
			t.Fatalf("modelKnownSQL missing %q: %s", required, modelKnownSQL)
		}
	}
}

func TestUniqueRawModelsPreservesVendorCasing(t *testing.T) {
	got := uniqueRawModels([]string{"MiniMax-M3", "minimax-m3", " GPT-4o ", ""})
	want := []string{"MiniMax-M3", "GPT-4o"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("raw model %d = %q, want %q", i, got[i], want[i])
		}
	}
}
