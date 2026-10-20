package routemount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree materialises a fake module tree and returns its root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return root
}

const goodAdmin = `package admin

import "net/http"

type Handler struct{}

func (h *Handler) handleThing(w http.ResponseWriter, r *http.Request) {}

// RegisterThingRoutes is called from main.go ⇒ mounted.
func (h *Handler) RegisterThingRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/thing", wrap(h.handleThing))
}
`

const orphanAdmin = `package admin

import "net/http"

type Handler struct{}

// RegisterGhostRoutes is never called ⇒ the endpoint would answer 404.
func (h *Handler) RegisterGhostRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/ghost", wrap(h.handleGhost))
}

func (h *Handler) handleGhost(w http.ResponseWriter, r *http.Request) {}
`

const mainCalls = `package main

import "net/http"

func main() {
	mux := http.NewServeMux()
	var h interface{ RegisterThingRoutes(*http.ServeMux, func(http.HandlerFunc) http.HandlerFunc) }
	h.RegisterThingRoutes(mux, nil)
}
`

// TestScannerReportsOrphanedRegistration is the scanner self-test: a
// route-registering function with no call site must be reported, and one that
// is called must not be.
func TestScannerReportsOrphanedRegistration(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":              "module example.com/x\n",
		"admin/good.go":       goodAdmin,
		"admin/orphan.go":     orphanAdmin,
		"cmd/gateway/main.go": mainCalls,
	})

	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 finding (the ghost registration), got %d: %v", len(findings), findings)
	}
	f := findings[0]
	if f.Func != "RegisterGhostRoutes" {
		t.Fatalf("finding func = %q, want RegisterGhostRoutes", f.Func)
	}
	if f.Pkg != "admin" {
		t.Fatalf("finding pkg = %q, want admin", f.Pkg)
	}
	if len(f.Routes) != 1 || f.Routes[0] != "/api/admin/ghost" {
		t.Fatalf("finding routes = %v, want [/api/admin/ghost]", f.Routes)
	}
	if !strings.Contains(f.String(), "no non-test call site") {
		t.Fatalf("finding string missing the reason: %s", f.String())
	}
}

// TestScannerIgnoresTestOnlyCallSites pins the rule: a registration that only
// happens under `go test` is not mounted in production, so it must be
// reported just like a never-called one.
func TestScannerIgnoresTestOnlyCallSites(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":              "module example.com/x\n",
		"admin/good.go":       goodAdmin,
		"admin/orphan.go":     orphanAdmin,
		"admin/admin_test.go": "package admin\n\nfunc TestThing(t *testing.T) { _ = t }\n",
	})

	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// RegisterThingRoutes has no call site at all here ⇒ also reported.
	if len(findings) != 2 {
		t.Fatalf("want 2 findings, got %d: %v", len(findings), findings)
	}
}

// TestScannerIgnoresNonRouteFunctions keeps the scanner from reporting plain
// helpers that happen to live next to route code.
func TestScannerIgnoresNonRouteFunctions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod": "module example.com/x\n",
		"admin/admin.go": `package admin

import "net/http"

func helper(x int) int { return x }

func usesMux(mux *http.ServeMux) {
	// not a route registration at all ⇒ must not become a candidate
	mux.HandleFunc("/api/admin/thing", nil)
	muxOther(mux)
}

func muxOther(mux *http.ServeMux) {}
`,
	})

	findings, disabledList, err := ScanWithDisabled(root)
	if err != nil {
		t.Fatalf("ScanWithDisabled: %v", err)
	}
	if len(findings) != 1 || findings[0].Func != "usesMux" {
		t.Fatalf("want exactly 1 finding (usesMux, never called), got %d: %v", len(findings), findings)
	}
	if len(disabledList) != 0 {
		t.Fatalf("want 0 disabled entries, got %d: %v", len(disabledList), disabledList)
	}
}

// TestScannerSeesDynamicRoutePatterns is the **blind-spot regression test**.
//
// The first version of this scanner only collected *literal* route patterns.
// A registration whose pattern is a variable (or a `+` concatenation) was
// therefore dropped silently — and a function registering such a route was
// never even a candidate, so the gate stayed green while the endpoint was
// never mounted. That is the "恒绿盲区" family: the scan leg reads as
// working while it quietly skips exactly the shape it must catch.
//
// This repository has four such registrations today
// (admin/sanitize_rules_handler.go, admin/journal_handlers.go), so the
// scanner must report them as DynamicRouteMarker instead of dropping them.
func TestScannerSeesDynamicRoutePatterns(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod": "module example.com/x\n",
		"admin/admin.go": `package admin

import "net/http"

type Handler struct{}

const somePath = "/api/admin/dynamic"

func (h *Handler) RegisterDynamicRoutes(mux *http.ServeMux) {
	mux.HandleFunc(somePath, nil)
}
`,
	})

	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding (the dynamic-pattern registration), got %d: %v", len(findings), findings)
	}
	if len(findings[0].Routes) != 1 || findings[0].Routes[0] != DynamicRouteMarker {
		t.Fatalf("routes = %v, want [%s]", findings[0].Routes, DynamicRouteMarker)
	}
}

// TestScannerSeesGo122MethodPrefixedPatterns pins the second blind spot:
// Go 1.22's `mux.Handle("GET /path", h)` form. This repository has eleven
// such registrations in production code (plugin proxy/canon routes, the
// quota webhook, the plugin admin endpoints, the storage metrics endpoint).
func TestScannerSeesGo122MethodPrefixedPatterns(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod": "module example.com/x\n",
		"admin/admin.go": `package admin

import "net/http"

type Handler struct{}

func (h *Handler) RegisterMethodPatternRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/admin/thing", nil)
}
`,
	})

	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %v", len(findings), findings)
	}
	if len(findings[0].Routes) != 1 || findings[0].Routes[0] != "/api/admin/thing" {
		t.Fatalf("routes = %v, want [/api/admin/thing] (method prefix stripped)", findings[0].Routes)
	}
}

// TestScannerHonoursExplicitDisableMarker pins the "explicit decision" rule:
// a registration function with no call site is a finding **unless** the source
// carries the disable marker together with a reason.
func TestScannerHonoursExplicitDisableMarker(t *testing.T) {
	disabled := `package admin

import "net/http"

type Handler struct{}

// RegisterOffRoutes is deliberately not mounted.
//
// routemount: disabled — the backing table is only created by the fresh-install
// path, so mounting it on upgraded environments would turn 404 into 500.
func (h *Handler) RegisterOffRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/off", wrap(h.handleOff))
}

func (h *Handler) handleOff(w http.ResponseWriter, r *http.Request) {}
`
	markerNoReason := `package admin

import "net/http"

type Handler struct{}

// routemount: disabled
func (h *Handler) RegisterNoReasonRoutes(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/noreason", wrap(h.handleNoReason))
}

func (h *Handler) handleNoReason(w http.ResponseWriter, r *http.Request) {}
`
	root := writeTree(t, map[string]string{
		"go.mod":        "module example.com/x\n",
		"admin/off.go":  disabled,
		"admin/off2.go": markerNoReason,
	})

	findings, disabledList, err := ScanWithDisabled(root)
	if err != nil {
		t.Fatalf("ScanWithDisabled: %v", err)
	}
	if len(disabledList) != 1 || disabledList[0].Func != "RegisterOffRoutes" {
		t.Fatalf("want exactly 1 disabled entry (RegisterOffRoutes), got %d: %v", len(disabledList), disabledList)
	}
	if len(findings) != 1 || findings[0].Func != "RegisterNoReasonRoutes" {
		t.Fatalf("want exactly 1 finding (the marker without a reason), got %d: %v", len(findings), findings)
	}
}
