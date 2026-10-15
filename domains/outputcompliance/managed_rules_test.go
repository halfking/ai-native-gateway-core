package outputcompliance

import (
	"context"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"regexp"
	"testing"
)

func TestManagedSourceReplacesLegacyDetectors(t *testing.T) {
	c := &Checker{piiPatterns: []*PIIPattern{{Enabled: true, PatternType: "phone", regex: regexp.MustCompile(`13800138000`)}}, biasDetector: NewBiasDetector()}
	text := "13800138000 password=R7#kQ?92! 10.2.3.4"
	before, err := c.Check(context.Background(), "a", text)
	if err != nil || len(before.Issues) == 0 {
		t.Fatal("legacy prerequisite missing", err)
	}
	c.SetManagedRuleSource(func() ManagedRuleCheck {
		return func(ctx context.Context, tenant, value string) (*ComplianceResult, error) {
			return &ComplianceResult{Compliant: true, RedactedOutput: value}, nil
		}
	})
	result, err := c.Check(context.Background(), "a", text)
	if err != nil || result.Blocked || result.RedactedOutput != text || len(result.Issues) != 0 {
		t.Fatal("legacy duplicate checks remain", result, err)
	}
}

func TestLegacyPIITypesImportAsRestorableConfiguration(t *testing.T) {
	c := NewBuiltinChecker()
	for _, kind := range []string{"bank_card", "ip", "ip_address", "ipv6", "passport", "ssn"} {
		c.piiPatterns = append(c.piiPatterns, &PIIPattern{PatternName: kind, PatternType: kind, RegexPattern: `VALUE-[0-9]+`, Enabled: true})
	}
	cfg := c.RuleConfig()
	if err := sanitizerules.Validate(cfg); err != nil {
		t.Fatal("existing PII table prevents manager startup", err)
	}
}
