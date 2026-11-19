package modelname

import "testing"

// TestLastSegment pins the prefix-stripping contract: vendor prefixes
// (single or multi-level) are display routing, the last '/'-segment is the
// identity (owner ruling 2026-11-20).
func TestLastSegment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"glm-5.3-flash", "glm-5.3-flash"},
		{"z-ai/glm-5.3-flash", "glm-5.3-flash"},
		{"minimaxai/minimax-m3.1-flash", "minimax-m3.1-flash"},
		{"a/b/minimax-m3.1-flash", "minimax-m3.1-flash"}, // multi-level prefix
		{"  glm-5.3-flash  ", "glm-5.3-flash"},           // whitespace trimmed
		{"glm-5.3-flash/", "glm-5.3-flash"},              // trailing slash
		{"z-ai/glm-5.3-flash///", "glm-5.3-flash"},
		{"", ""},
		{"   ", ""},
		{"/", ""},
		{"///", ""},
		{"/glm-5.3-flash", "glm-5.3-flash"}, // leading slash = one empty prefix
	}
	for _, c := range cases {
		if got := LastSegment(c.in); got != c.want {
			t.Errorf("LastSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestConsistent pins the integrity-chain judgment against the exact
// production false-positive classes (211, 14 days) plus the genuine
// substitution controls that must keep firing.
func TestConsistent(t *testing.T) {
	cases := []struct {
		name     string
		reported string
		trusted  []string
		want     bool
	}{
		// ── owner's example: prefix vs bare name ⇒ 一致 ──
		{"owner example", "minimax-m3.1-flash", []string{"minimaxai/minimax-m3.1-flash"}, true},
		{"z-ai prefix", "glm-5.3-flash", []string{"z-ai/glm-5.3-flash"}, true},
		{"multi-level prefix", "minimax-m3.1-flash", []string{"a/b/minimax-m3.1-flash"}, true},
		{"case + prefix", "glm-5.3-flash", []string{"zcode/GLM-5.3-Flash"}, true},
		// ── either-match: trusted set = {outbound, client} ──
		{"matches client of the two", "minimax-m3.1-flash", []string{"MiniMax-M3.1-Flash-Preview", "minimax-m3.1-flash"}, true},
		{"matches outbound of the two", "glm-5.3-flash", []string{"z-ai/glm-5.3-flash", "glm-5.3-flash"}, true},
		// ── genuine substitutions must stay flagged ──
		{"different model", "glm-5.2", []string{"glm-5.3-flash", "glm-5.3-flash"}, false},
		{"preview suffix is part of the name", "minimax-m3.1-flash", []string{"minimax-m3.1-flash-preview"}, false},
		{"vendor word is not a prefix", "sonnet-5", []string{"claude-sonnet-5"}, false},
		{"relay internal id", "group/auto-glm-5-3-flash", []string{"auto/glm-5.3-flash"}, false},
		// ── emptiness is not consistency ──
		{"empty reported", "", []string{"glm-5.3-flash"}, false},
		{"empty trusted skipped", "glm-5.3-flash", []string{"", ""}, false},
		{"no trusted", "glm-5.3-flash", nil, false},
		{"mixed empty trusted", "glm-5.3-flash", []string{"", "z-ai/glm-5.3-flash"}, true},
	}
	for _, c := range cases {
		if got := Consistent(c.reported, c.trusted...); got != c.want {
			t.Errorf("%s: Consistent(%q, %v) = %v, want %v", c.name, c.reported, c.trusted, got, c.want)
		}
	}
}
