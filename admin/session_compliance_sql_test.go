package admin

import (
	"strings"
	"testing"
)

func TestSessionComplianceSQLMatchesLivePromptInjectionColumns(t *testing.T) {
	sql := sessionComplianceSQL
	for _, part := range []string{
		"FROM prompt_injection_detections",
		"evidence_text AS evidence",
		"FROM output_compliance_audit",
	} {
		if !strings.Contains(sql, part) {
			t.Fatalf("compliance SQL missing %q", part)
		}
	}
	promptBranch := sql[:strings.Index(sql, "FROM prompt_injection_detections")]
	if strings.Contains(promptBranch, "issue_type") && !strings.Contains(promptBranch, "AS issue_type") {
		t.Fatal("prompt_injection_detections has no issue_type column")
	}
	if strings.Contains(sql, "combined") && strings.Contains(sql, "WHERE session_key = $1") {
		outer := sql[strings.LastIndex(sql, "combined"):]
		if strings.Contains(outer, "WHERE session_key") {
			t.Fatal("outer combined subquery does not expose session_key")
		}
	}
}
