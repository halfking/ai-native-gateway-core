package outputcompliance

import (
	"testing"
)

func TestActionForIssue(t *testing.T) {
	c := &Checker{}
	policy := &Policy{
		ActionOnPII:           "redact",
		ActionOnToxicity:      "warn",
		ActionOnSecrets:       "block",
		ActionOnInternalIP:    "log",
		ActionOnHallucination: "log",
	}

	cases := []struct {
		issueType string
		want      string
	}{
		{"pii", "redact"},
		{"toxic", "warn"},
		{"secret", "block"},
		{"internal_ip", "log"},
		{"unknown", "log"},
	}

	for _, tc := range cases {
		got := c.actionForIssue(ComplianceIssue{Type: tc.issueType}, policy)
		if got != tc.want {
			t.Errorf("actionForIssue(%q) = %q, want %q", tc.issueType, got, tc.want)
		}
	}
}

func TestShouldBlockWithActionBlock(t *testing.T) {
	c := &Checker{}
	policy := &Policy{EnforcementMode: "enforce", ActionOnSecrets: "block"}
	issues := []ComplianceIssue{{Type: "secret", Severity: 5}}
	if !c.shouldBlock(issues, policy) {
		t.Error("expected block for action=block in enforce mode")
	}
}

func TestShouldBlockExceptionMatched(t *testing.T) {
	c := &Checker{}
	policy := &Policy{EnforcementMode: "enforce", ActionOnSecrets: "block"}
	issues := []ComplianceIssue{{Type: "secret", Severity: 5, ExceptionMatched: true}}
	if c.shouldBlock(issues, policy) {
		t.Error("exception-matched issue should not block")
	}
}

func TestDetectSecrets(t *testing.T) {
	c := &Checker{}
	policy := &Policy{ActionOnSecrets: "redact"}
	issues := c.detectSecrets("call with sk-abcd1234abcd1234 and token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJz.dummy", policy)
	if len(issues) != 2 {
		t.Fatalf("expected 2 secret issues, got %d", len(issues))
	}
}

func TestDetectInternalIP(t *testing.T) {
	c := &Checker{}
	policy := &Policy{ActionOnInternalIP: "redact"}
	issues := c.detectInternalIP("server at 192.168.1.1", policy)
	if len(issues) != 1 {
		t.Fatalf("expected 1 internal ip issue, got %d", len(issues))
	}
}

func TestRedactOutputSkipsException(t *testing.T) {
	c := &Checker{}
	policy := &Policy{AutoRedact: true, ActionOnSecrets: "redact"}
	output := "key is sk-abcd1234abcd1234"
	issues := []ComplianceIssue{
		{Type: "secret", Location: "char:8-29", Content: "************************", ExceptionMatched: true},
	}
	got := c.redactOutput(output, issues, policy)
	if got != output {
		t.Errorf("exception-matched issue should not be redacted; got %q", got)
	}
}

func TestRedactOutputSecret(t *testing.T) {
	c := &Checker{}
	policy := &Policy{AutoRedact: true, ActionOnSecrets: "redact"}
	output := "key is sk-abcd1234abcd1234"
	issues := []ComplianceIssue{
		{Type: "secret", Location: "char:7-26", Content: "*******************"},
	}
	got := c.redactOutput(output, issues, policy)
	want := "key is *******************"
	if got != want {
		t.Errorf("redactOutput = %q, want %q", got, want)
	}
}
