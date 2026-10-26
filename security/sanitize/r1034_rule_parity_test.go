package sanitize

import (
	"os"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/credentialtext"
	"gopkg.in/yaml.v3"
)

func TestR1034DeclarationRulesMatchProductionConfig(t *testing.T) {
	data, err := os.ReadFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg patternConfig
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"account": cfg.PII.Patterns["account"].Regex, "password": cfg.Secret.Patterns["password"].Regex,
		"pair_account": cfg.PII.Patterns["credential_pair_account"].Regex, "pair_secret": cfg.Secret.Patterns["credential_pair_secret"].Regex,
	} {
		want := credentialtext.CredentialPairPattern
		if name == "account" {
			want = credentialtext.AccountPattern
		}
		if name == "password" {
			want = credentialtext.PasswordPattern
		}
		if got != want {
			t.Errorf("%s rule differs from builtin declaration rule", name)
		}
	}
}
