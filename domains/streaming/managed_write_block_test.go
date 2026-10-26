package streaming

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

func TestWriteTimeManagedBlockCannotReturnOriginalBody(t *testing.T) {
	r1013RequireComplianceOn(t)
	checker := outputcompliance.NewBuiltinChecker()
	checker.SetManagedRuleSource(func() outputcompliance.ManagedRuleCheck {
		return func(context.Context, string, string) (*outputcompliance.ComplianceResult, error) {
			return &outputcompliance.ComplianceResult{Blocked: true, RedactedOutput: "[REDACTED:secret]"}, nil
		}
	})
	fn := BuildRedactBodyFn(checker, nil, nil)
	body := []byte(`{"choices":[{"message":{"content":"VALUE-1234"}}]}`)
	out := fn(body, "", "a")
	if strings.Contains(string(out), "VALUE-1234") {
		t.Fatal("write-time block returned original sensitive output")
	}
	var envelope map[string]any
	if json.Unmarshal(out, &envelope) != nil {
		t.Fatal("invalid block response")
	}
}
