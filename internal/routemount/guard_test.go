package routemount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryRegisteredRouteHasACallSite is the repository-wide gate.
//
// It fails when a function that registers an HTTP route has no call site in
// non-test code — the shape that produced the measured 404s on
// /api/admin/model-status and /api/admin/feishubot/* (2026-10-21, R72).
//
// The failure message names the function, its file:line and the routes it
// would have registered, so the fix is mechanical.
func TestEveryRegisteredRouteHasACallSite(t *testing.T) {
	root, err := ModuleRoot(".")
	if err != nil {
		t.Fatalf("ModuleRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root %s has no go.mod: %v", root, err)
	}

	findings, disabledList, err := ScanWithDisabled(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, d := range disabledList {
		t.Logf("deliberately unmounted (explicit marker): %s.%s (%s) -> %v",
			d.Pkg, d.Func, d.Pos, d.Routes)
	}
	if len(findings) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, f := range findings {
		b.WriteString("\n  " + f.String())
	}
	b.WriteString("\n\nEach of these functions registers HTTP routes but is never called from non-test code,")
	b.WriteString("\nso the routes it registers answer 404 in production while its unit tests keep passing.")
	b.WriteString("\nEither call it from the production wiring, or delete the function and its handler.")
	b.WriteString("\n\nEvidence: 2026-10-21 full-endpoint sweep against build 2.5.8-d3b57b45-20261020-2792")
	b.WriteString("\nfound /api/admin/model-status and /api/admin/feishubot/* answering 404;")
	b.WriteString("\nthe first call site was deleted by merge d2cbaf88b, the second by build fix b6f8a69cb.\n")
	t.Fatal(b.String())
}
