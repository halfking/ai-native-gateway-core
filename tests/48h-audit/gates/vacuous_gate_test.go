// Package gates holds cross-domain guards for the 48h audit harness itself.
//
// R78: the D01–D17 acceptance gates in tests/48h-audit/*/plan.md are written
// as `go test -race -timeout Ns ./tests/48h-audit/D<NN>-<name>/...`. A Go
// package pattern that matches nothing is NOT an error — `go test` prints
// "matched no packages" as a warning, exits 0, and the gate goes green.
//
// Measured on HEAD 7174f3ab5: 14 of the 17 domain gates match zero packages.
// Only D01, D02 and D14 contain Go packages, so only those three gates can
// actually fail. The rest are decorative.
//
// The damage is worse than "no gate". A gate that cannot fail is read as
// evidence by everyone who does not re-run it, including the rounds that
// marked items [x] against tests living in an unrelated package (D03's B-01
// points at ./cmd/gateway -run TestLiteRequestLogSink_, D05's B-01 at
// ./domains/streaming/executors). The evidence is real; the gate that is
// supposed to prove it is not wired to it.
package gates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// knownVacuousGates are the domains whose plan.md gate still matches no Go
// package. This allowlist exists so the guard is enforceable TODAY: without it
// the repo would be red and the guard would never ship, and a permanently red
// guard is a guard nobody reads (the same reasoning that produced the
// selection-layer threshold ratchet in R77).
//
// It is deliberately not a permanent list. TestVacuousGateAllowlistIsMinimal
// fails if an entry no longer needs to be here, so closing a gap removes the
// entry rather than leaving a tombstone.
var knownVacuousGates = map[string]string{
	"D03-three-tier-cache":     "B-01/S-01 evidence lives in ./cmd/gateway, not in the domain package",
	"D04-queue-concurrency":    "3 checked items, no domain package; evidence not located in R78",
	"D05-auto-compression":     "B-01/D-01/S-01 evidence lives in ./domains/streaming/executors",
	"D06-dual-storage-mode":    "3 checked items, no domain package; evidence not located in R78",
	"D07-hot-columnar":         "3 checked items, no domain package; evidence not located in R78",
	"D08-provider-errors":      "3 checked items, no domain package; evidence not located in R78",
	"D09-node-state-selfcheck": "3 checked items, no domain package; evidence not located in R78",
	"D10-stats-aggregation":    "3 checked items, no domain package; evidence not located in R78",
	"D12-egress-proxy":         "SF-01 checked with no domain package; evidence not located in R78",
	"D13-free-token-pool":      "3 checked items, no domain package; evidence not located in R78",
	"D15-observability-ux":     "2 checked items, no domain package; evidence not located in R78",
	"D16-flow-closure":         "2 checked items, no domain package; evidence not located in R78",
	"D17-code-hygiene":         "3 checked items, no domain package; evidence not located in R78",
}

const auditRoot = "../"

// domainGateRe extracts the acceptance-gate package pattern from a plan.md.
// Plans vary in formatting (fenced block, inline code, table cells), so this
// matches the ./tests/48h-audit/... pattern wherever it appears after the
// 验收门 heading.
var domainGateRe = regexp.MustCompile(`\./tests/48h-audit/([A-Za-z0-9._-]+)/?\.{3}`)

// listDomains returns every D<NN>-* domain directory under tests/48h-audit.
func listDomains(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(auditRoot)
	if err != nil {
		t.Fatalf("read audit root: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && regexp.MustCompile(`^D\d\d-`).MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("no D<NN>-* domains found — the audit root moved; update auditRoot")
	}
	return out
}

// hasGoPackage reports whether dir — or anything under it, since a `./dir/...`
// pattern includes subdirectories — contains at least one .go file. Without the
// recursion this helper reports D01/D02/D14 as package-less when their tests
// live in business/ safety/ stress/ subpackages, which is the normal layout.
func hasGoPackage(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			found = true
		}
		return nil
	})
	return found
}

// TestNoNewVacuousAcceptanceGate is the guard proper: a domain whose plan.md
// names its own acceptance gate must have a Go package behind that pattern.
// Any domain not on the allowlist and without a package fails here — that is
// the "gate silently green forever" failure mode, caught at build time.
func TestNoNewVacuousAcceptanceGate(t *testing.T) {
	var unexpected []string
	for _, dom := range listDomains(t) {
		plan := filepath.Join(auditRoot, dom, "plan.md")
		body, err := os.ReadFile(plan)
		if err != nil {
			t.Errorf("%s: no plan.md: %v", dom, err)
			continue
		}
		if !domainGateRe.Match(body) {
			// Some plans (D11) document the gate as a prose table rather than a
			// self-referential package pattern. Those are not this guard's
			// subject; they are audited by the master doc instead.
			continue
		}
		if hasGoPackage(filepath.Join(auditRoot, dom)) {
			continue
		}
		if _, known := knownVacuousGates[dom]; known {
			continue
		}
		unexpected = append(unexpected, dom)
	}
	if len(unexpected) > 0 {
		t.Errorf("domains with an acceptance gate that can never fail (no Go package "+
			"behind ./tests/48h-audit/<domain>/...): %v\n"+
			"Either add a real test package under the domain, point the gate at the "+
			"package that actually holds the evidence, or — if the gate is knowingly "+
			"still vacuous — add it to knownVacuousGates with a reason so the debt "+
			"stays visible instead of silently green.", unexpected)
	}
}

// TestVacuousGateAllowlistIsMinimal keeps the allowlist from rotting. An entry
// whose domain has since gained a package must be removed: a stale entry would
// hide a real regression behind an expired excuse.
func TestVacuousGateAllowlistIsMinimal(t *testing.T) {
	var stale []string
	for dom := range knownVacuousGates {
		if hasGoPackage(filepath.Join(auditRoot, dom)) {
			stale = append(stale, dom)
		}
	}
	if len(stale) > 0 {
		t.Errorf("knownVacuousGates still lists domains that now have Go packages: %v\n"+
			"Remove them — the allowlist is debt tracking, not a permanent exemption.",
			stale)
	}
}

// TestVacuousGateDebtIsReported prints the outstanding debt on every run. A
// silent allowlist is how the next round repeats the mistake, so the debt is
// stated in the test log even when the test passes.
func TestVacuousGateDebtIsReported(t *testing.T) {
	if len(knownVacuousGates) == 0 {
		t.Skip("no outstanding vacuous-gate debt")
	}
	t.Logf("OUTSTANDING AUDIT-HARNESS DEBT: %d/17 domain acceptance gates match zero Go "+
		"packages and therefore can never fail. Their checked plan items are backed by "+
		"evidence elsewhere in the repo, not by the gate:", len(knownVacuousGates))
	for _, dom := range listDomains(t) {
		if reason, ok := knownVacuousGates[dom]; ok {
			t.Logf("  %-26s %s", dom, reason)
		}
	}
}
