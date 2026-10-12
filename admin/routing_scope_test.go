package admin

import "testing"

func TestIsValidRoutingScope(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"all", true},
		{"chat_only", true},
		{"media_only", true},
		{"", false},
		{"special", false},
		{"MEDIA_ONLY", false},
		{" chat_only ", true},
	}
	for _, tc := range cases {
		if got := isValidRoutingScope(tc.in); got != tc.want {
			t.Errorf("isValidRoutingScope(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRoutingScope(t *testing.T) {
	if got := normalizeRoutingScope(""); got != routingScopeAll {
		t.Fatalf("empty → all, got %q", got)
	}
	if got := normalizeRoutingScope(" media_only "); got != "media_only" {
		t.Fatalf("trim failed: %q", got)
	}
}
