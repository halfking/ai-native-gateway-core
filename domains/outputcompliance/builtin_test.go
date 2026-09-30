package outputcompliance

import (
	"context"
	"strings"
	"testing"
)

func TestBuiltinCheckerRedactsWithoutDatabase(t *testing.T) {
	checker := NewBuiltinChecker()
	for _, value := range []string{
		"phone 13800138000",
		"email user@example.com",
		"password = hunter2",
	} {
		result, err := checker.Check(context.Background(), "tenant-lite", value)
		if err != nil || result == nil || len(result.Issues) == 0 {
			t.Fatalf("Check(%q): result=%+v err=%v", value, result, err)
		}
		if strings.Contains(result.RedactedOutput, strings.TrimPrefix(value, "phone ")) && strings.HasPrefix(value, "phone ") {
			t.Fatalf("phone not redacted: %q", result.RedactedOutput)
		}
		if result.RedactedOutput == value {
			t.Fatalf("output unchanged: %q", value)
		}
	}
}
