package sessionmeta

import "testing"

// The 2026-10-29 domestic-vendor round: the nine new CN-vendor coding
// tools must classify as coding_agent. agentRole runs AFTER
// normalizeAgent → clienttype.Normalize, so a name missing from the
// allowlist OR from this switch falls to other_agent — the exact gap the
// R52 minimax/deepseek fix closed, now closed for the rest of the
// domestic set.
func TestAgentRole_DomesticVendorRound(t *testing.T) {
	for _, name := range []string{
		"codebuddy", "trae", "comate", "qwen-code", "kimi-cli",
		"iflow", "raccoon", "iflycode", "lingma",
	} {
		if got := agentRole(name); got != "coding_agent" {
			t.Fatalf("agentRole(%q) = %q, want coding_agent", name, got)
		}
	}
}

// End-to-end through normalizeAgent: the header-supplied spelling is
// lowercased and validated against the clienttype allowlist before the
// role switch sees it.
func TestNormalizeAgent_DomesticVendorRound(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"CodeBuddy", "codebuddy"},
		{" TRAE ", "trae"},
		{"Zulu", "comate"},
		{"kimi-cli", "kimi-cli"},
		{"iflow", "iflow"},
		{"Raccoon", "raccoon"},
		{"iFlyCode", "iflycode"},
		{"Lingma", "lingma"},
		// Not a known client: collapses to unknown, never invents a name.
		{"nomad-dev", ""},
	} {
		want := tc.want
		if tc.want == "" {
			want = "unknown"
		}
		if got := normalizeAgent(tc.in); got != want {
			t.Fatalf("normalizeAgent(%q) = %q, want %q", tc.in, got, want)
		}
	}
}
