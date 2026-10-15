package sanitize

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRuleMetadataSurvivesEditedPatterns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.yaml")
	body := `pii:
  enabled: true
  patterns:
    credential_pair_account:
      kind: credential_pair
      regex: '组合=([^/ ]+)/(\S+)'
      group: 1
secret:
  enabled: true
  patterns:
    credential_pair_secret:
      kind: credential_pair
      regex: '组合=([^/ ]+)/(\S+)'
      group: 2
    password:
      kind: credential_declaration
      regex: '口令设成(\S+)'
      group: 1
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := NewPatternDetectorFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"口令设成correcthorse", "组合=ops/correcthorse"} {
		input, err := d.DetectScope(context.Background(), text, ScopeInputDeclarations)
		if err != nil || len(input) == 0 {
			t.Fatal("input pattern missing")
		}
		output, err := d.DetectScope(context.Background(), text, ScopeEgress)
		if err != nil || len(output) != 0 {
			t.Fatal("edited input rule widened tool blocking")
		}
	}
}
