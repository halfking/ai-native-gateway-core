package compliance

import (
	"strings"
	"testing"
	"time"
)

// TestRender_DJCP_HappyPath verifies a basic 等保 2.0 report renders with
// expected Markdown structure: header, summary table, per-control sections.
func TestRender_DJCP_HappyPath(t *testing.T) {
	r := Report{
		Title:     "等保 2.0 合规报告",
		Framework: FrameworkDJCP,
		Period:    "2026-06",
		TenantID:  "t1",
		Summary:   "All critical controls passed.",
		Evidence: []Evidence{
			{ControlID: "8.1.2", Title: "身份鉴别", Status: StatusPass,
				Detail: "JWT 鉴权已启用, 100% 请求通过."},
			{ControlID: "8.1.4", Title: "访问控制", Status: StatusPartial,
				Detail: "RLS 已部署, 个别 admin 路径待加固."},
		},
	}
	out, err := Render(r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Must contain title, both control IDs, both statuses, and the template version.
	for _, want := range []string{
		"等保 2.0 合规报告",
		"8.1.2", "8.1.4",
		string(StatusPass), string(StatusPartial),
		TemplateVersion,
		"租户", "t1",
		"摘要",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q", want)
		}
	}
}

// TestRender_GDPR_HappyPath verifies the GDPR template renders.
func TestRender_GDPR_HappyPath(t *testing.T) {
	r := Report{
		Title:     "GDPR Article 32 Compliance",
		Framework: FrameworkGDPR,
		Period:    "2026-06",
		TenantID:  "eu-tenant-1",
		Evidence: []Evidence{
			{ControlID: "Art.32", Title: "Security of Processing", Status: StatusPass,
				Detail: "TLS 1.3 enforced; encryption at rest via RLS."},
		},
	}
	out, err := Render(r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"GDPR", "Art.32", "Security of Processing",
		"eu-tenant-1", "Executive Summary", TemplateVersion,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("GDPR render missing %q", want)
		}
	}
}

// TestRender_UnknownFramework_Rejected rejects typos like "djcp_2" (forgot ".0").
func TestRender_UnknownFramework_Rejected(t *testing.T) {
	r := Report{Title: "x", Framework: Framework("djcp_2"), Period: "p", TenantID: "t"}
	_, err := Render(r)
	if err == nil {
		t.Fatal("expected error for unknown framework, got nil")
	}
	if !strings.Contains(err.Error(), "framework") {
		t.Errorf("error should mention 'framework', got: %v", err)
	}
}

// TestRender_EmptyTitle_Rejected enforces that a report MUST have a title.
func TestRender_EmptyTitle_Rejected(t *testing.T) {
	r := Report{Framework: FrameworkDJCP, Period: "p", TenantID: "t"}
	_, err := Render(r)
	if err == nil {
		t.Fatal("expected error for empty title")
	}
	if !strings.Contains(err.Error(), "Title") {
		t.Errorf("error should mention Title, got: %v", err)
	}
}

// TestRender_EmptyEvidence_StillValid verifies a report with zero evidence
// is allowed (e.g., a skeleton that the admin fills in later).
func TestRender_EmptyEvidence_StillValid(t *testing.T) {
	r := Report{
		Title: "Skeleton", Framework: FrameworkDJCP, Period: "p", TenantID: "t",
		Summary: "No findings yet.",
	}
	out, err := Render(r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "No findings yet.") {
		t.Error("summary should appear even with empty evidence")
	}
}

// TestRender_StampsGeneratedAt verifies GeneratedAt is auto-filled if zero.
func TestRender_StampsGeneratedAt(t *testing.T) {
	r := Report{Title: "t", Framework: FrameworkDJCP, Period: "p", TenantID: "x"}
	before := time.Now().UTC().Add(-time.Second) // clock skew tolerance
	out, err := Render(r)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC().Add(time.Second)
	// The template prints "2006-01-02T15:04:05Z07:00"; grep for a year.
	if !strings.Contains(out, time.Now().UTC().Format("2006")) {
		t.Errorf("generated year missing from output:\n%s", out)
	}
	_ = before
	_ = after
}

// TestRender_AllStatuses_IconCorrect verifies each status renders an icon.
func TestRender_AllStatuses_IconCorrect(t *testing.T) {
	statuses := []Status{StatusPass, StatusFail, StatusPartial, StatusNotApply}
	for _, s := range statuses {
		got := statusIcon(s)
		if got == "?" {
			t.Errorf("statusIcon(%q) returned default '?'", s)
		}
	}
}

// TestCountByStatus verifies the tally helper used by the summary table.
func TestCountByStatus(t *testing.T) {
	ev := []Evidence{
		{Status: StatusPass},
		{Status: StatusPass},
		{Status: StatusFail},
		{Status: StatusPartial},
		{Status: StatusPass},
	}
	counts := CountByStatus(ev)
	if counts[StatusPass] != 3 {
		t.Errorf("pass count = %d, want 3", counts[StatusPass])
	}
	if counts[StatusFail] != 1 {
		t.Errorf("fail count = %d, want 1", counts[StatusFail])
	}
	if counts[StatusPartial] != 1 {
		t.Errorf("partial count = %d, want 1", counts[StatusPartial])
	}
}

// TestValidFrameworks_Stable ensures the supported frameworks don't drift.
func TestValidFrameworks_Stable(t *testing.T) {
	for _, fw := range []Framework{FrameworkDJCP, FrameworkGDPR} {
		if !ValidFrameworks[fw] {
			t.Errorf("framework %q should be valid", fw)
		}
		if _, ok := templates[fw]; !ok {
			t.Errorf("framework %q has no template registered", fw)
		}
	}
}

// TestRender_CrossTenantEvidence_NoLeak is a defensive test: a single
// Report struct is rendered as-is. Multi-tenancy is enforced at the
// caller (Admin API), not here. This test documents that contract.
func TestRender_CrossTenantEvidence_NoLeak(t *testing.T) {
	r := Report{
		Title: "isolation-test", Framework: FrameworkDJCP, Period: "p",
		TenantID: "tenant-A",
		Evidence: []Evidence{
			{ControlID: "X", Title: "tenant-A control", Status: StatusPass, Detail: "..."},
		},
	}
	out, err := Render(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tenant-A") {
		t.Error("expected tenant ID in output")
	}
	if strings.Contains(out, "tenant-B") {
		t.Error("should not contain other tenant names (callers scope evidence)")
	}
}
