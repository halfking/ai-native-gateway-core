package catalog

import "testing"

func TestResolveVendor(t *testing.T) {
	cases := []struct {
		name, family, dbVendor, want string
	}{
		{"gpt-5.4", "gpt", "", "OpenAI"},
		{"minimax-m3", "minimax", "", "MiniMax"},
		{"glm-4.7", "glm", "", "Zhipu AI"},
	}
	for _, tc := range cases {
		if got := ResolveVendor(tc.name, tc.family, tc.dbVendor); got != tc.want {
			t.Errorf("ResolveVendor(%q,%q,%q)=%q want %q", tc.name, tc.family, tc.dbVendor, got, tc.want)
		}
	}
}

func TestEffectiveModality_minimaxM3(t *testing.T) {
	if got := EffectiveModality("minimax-m3", "text"); got != "multimodal" {
		t.Fatalf("got %q want multimodal", got)
	}
}
