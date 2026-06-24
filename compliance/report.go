// Package compliance generates regulatory compliance reports (等保 2.0,
// GDPR) from gateway audit data. This is B4-1: the template engine that
// renders a Report (structured data) into Markdown using Go text/template.
//
// Domain boundary (see docs/产品方案/2026-06-23-llmgw-domain-architecture-refactor.md):
//   - compliance OWNS: report struct, template rendering, evidence aggregation
//   - compliance does NOT own: audit storage (audit/), policy enforcement
//     (armor/), the data collection queries (those live in callers — Admin API)
//
// Design:
//   - Templates are embedded strings (compile-time constants), versioned.
//   - A Report is the input data model; templates render it to Markdown.
//   - The engine is pure (no I/O) so it's fully unit-testable.
//   - Evidence is a list of {check, status, detail} — templates iterate it.
package compliance

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"
)

// TemplateVersion is bumped when any template string changes. Reports carry
// this so reviewers know which template generated them (reproducibility).
const TemplateVersion = "compliance-template-v1"

// Report is the structured input to the template engine. Callers (Admin API)
// populate this from audit/armor/apihub data, then call Render.
type Report struct {
	// Metadata
	Title       string    // e.g. "等保 2.0 合规报告"
	Framework   Framework // e.g. FrameworkDJCP
	Period      string    // e.g. "2026-06-01 ~ 2026-06-30"
	TenantID    string
	GeneratedAt time.Time
	TemplateVer string

	// Findings
	Summary  string     // executive summary, 2-3 sentences
	Evidence []Evidence // per-control evidence
}

// Framework is the regulatory standard the report conforms to.
type Framework string

const (
	FrameworkDJCP Framework = "djcp_2.0" // 等保 2.0 (信息安全等级保护)
	FrameworkGDPR Framework = "gdpr"     // GDPR (EU)
)

// ValidFrameworks is the set of supported frameworks (typo guard).
var ValidFrameworks = map[Framework]bool{
	FrameworkDJCP: true,
	FrameworkGDPR: true,
}

// Status is the compliance verdict for one control.
type Status string

const (
	StatusPass     Status = "pass"    // control satisfied
	StatusFail     Status = "fail"    // control violated
	StatusPartial  Status = "partial" // control partially met
	StatusNotApply Status = "n/a"     // control not applicable
)

// Evidence is one control's finding within a report.
type Evidence struct {
	ControlID string // e.g. "8.1.2" (等保), "Art.32" (GDPR)
	Title     string // human-readable control name
	Status    Status
	Detail    string // supporting evidence (audit refs, counts, dates)
}

// Render renders the report to Markdown using the framework's template.
// Returns an error if the report is structurally invalid (unknown framework,
// missing title). The output is always non-empty on success.
func Render(r Report) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	if r.GeneratedAt.IsZero() {
		r.GeneratedAt = time.Now().UTC()
	}
	if r.TemplateVer == "" {
		r.TemplateVer = TemplateVersion
	}

	tmplText, ok := templates[r.Framework]
	if !ok {
		return "", fmt.Errorf("compliance: no template for framework %q", r.Framework)
	}

	tmpl, err := template.New("report").Funcs(template.FuncMap{
		"statusIcon":    statusIcon,
		"lower":         strings.ToLower,
		"countByStatus": CountByStatus,
	}).Parse(tmplText)
	if err != nil {
		return "", fmt.Errorf("compliance: parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, r); err != nil {
		return "", fmt.Errorf("compliance: execute template: %w", err)
	}
	return buf.String(), nil
}

func (r Report) validate() error {
	if strings.TrimSpace(r.Title) == "" {
		return errors.New("compliance: Report.Title is required")
	}
	if !ValidFrameworks[r.Framework] {
		return fmt.Errorf("compliance: unknown framework %q", r.Framework)
	}
	return nil
}

func statusIcon(s Status) string {
	switch s {
	case StatusPass:
		return "✅"
	case StatusFail:
		return "❌"
	case StatusPartial:
		return "⚠️"
	case StatusNotApply:
		return "➖"
	default:
		return "?"
	}
}

// CountByStatus tallies evidence by status (for the summary header).
func CountByStatus(ev []Evidence) map[Status]int {
	out := map[Status]int{}
	for _, e := range ev {
		out[e.Status]++
	}
	return out
}
