// Package envinjector provides credential injection for deployment targets.
// It decrypts SOPS-encrypted .env.<target>.enc files and emits shell-compatible
// export statements for use with eval.
package envinjector

// Target describes a deployment target's credential envelope and SSH key.
type Target struct {
	Alias     string // short alias used on CLI (252, 154, kaixuan-1, etc.)
	EncFile   string // SOPS-encrypted envelope path relative to repo root
	SSHKeyEnv string // env var name that holds the SSH key path
	SSHKeyDef string // default SSH key path
}

// legacyAliases maps old server numbers to current canonical aliases.
var legacyAliases = map[string]string{
	"184": "252",
	"71":  "154",
}

// canonicalTargets is the registry of known deployment targets.
var canonicalTargets = []Target{
	{Alias: "252", EncFile: ".env.252.enc", SSHKeyEnv: "SSH_KEY_252", SSHKeyDef: "~/.ssh/id_ed25519"},
	{Alias: "154", EncFile: ".env.154.enc", SSHKeyEnv: "SSH_KEY_154", SSHKeyDef: "~/.ssh/id_ed25519"},
	{Alias: "245", EncFile: ".env.245.enc", SSHKeyEnv: "SSH_KEY_245", SSHKeyDef: "~/.ssh/id_ed25519"},
	{Alias: "kaixuan-1", EncFile: ".env.kaixuan-1.enc", SSHKeyEnv: "SSH_KEY_KAIXUAN_1", SSHKeyDef: "~/.ssh/kaixuan1_id_rsa"},
}

// ResolveAlias returns the canonical alias for a given input, expanding
// legacy server numbers (184→252, 71→154) to their current names.
func ResolveAlias(input string) string {
	if canonical, ok := legacyAliases[input]; ok {
		return canonical
	}
	return input
}

// FindTarget looks up a target by alias (after legacy expansion).
// Returns nil if the alias is unknown.
func FindTarget(alias string) *Target {
	canonical := ResolveAlias(alias)
	for i := range canonicalTargets {
		if canonicalTargets[i].Alias == canonical {
			return &canonicalTargets[i]
		}
	}
	return nil
}

// AllTargets returns the full target registry.
func AllTargets() []Target {
	out := make([]Target, len(canonicalTargets))
	copy(out, canonicalTargets)
	return out
}

// KnownAliases returns just the alias names for display.
func KnownAliases() []string {
	out := make([]string, len(canonicalTargets))
	for i, t := range canonicalTargets {
		out[i] = t.Alias
	}
	return out
}
