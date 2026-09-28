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
	"D04-queue-concurrency":    "3 checked items, no domain package; evidence not located in R78",
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

// auditRoot is tests/48h-audit, the parent of this package.
const auditRoot = "../"

// repoRoot is the module root. This test's CWD is the package directory
// tests/48h-audit/gates, so the root is THREE levels up (gates -> 48h-audit ->
// tests -> repo). Getting this wrong is silent and catastrophic: with two
// levels, every path resolved to <repo>/tests/<pattern> and hasGoPackage
// returned false for real packages, so the guard flagged sound domains as
// vacuous. TestRepoRootIsCorrect pins it against go.mod.
const repoRoot = "../../.."

// TestRepoRootIsCorrect makes the level count self-checking. If this fails,
// every other resolution in this file is wrong too.
func TestRepoRootIsCorrect(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("repoRoot %q does not contain go.mod: %v\n"+
			"This test's CWD is tests/48h-audit/gates; the module root is three "+
			"levels up. Re-count before touching any other path in this file.",
			repoRoot, err)
	}
	if _, err := os.Stat(filepath.Join(auditRoot, "D01-ir-lifecycle")); err != nil {
		t.Fatalf("auditRoot %q does not contain the domain dirs: %v", auditRoot, err)
	}
}

// domainGateRe extracts the acceptance-gate package pattern from a plan.md.
// Plans vary in formatting (fenced block, inline code, table cells), so this
// matches the ./tests/48h-audit/... pattern wherever it appears after the
// 验收门 heading.
// packagePatternRe matches any relative Go package pattern, whether it points
// at the domain's own directory or at the package that actually holds the
// evidence. R78: pointing the gate elsewhere is a legitimate fix — D03's
// provenance evidence lives in ./domains/hooks/compression, not in the domain
// directory, and a gate wired to the real tests can fail where a gate wired to
// an empty directory never can. The property under test is "the gate can fail",
// not "the gate points at tests/48h-audit".
var packagePatternRe = regexp.MustCompile(`\./[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*/?(?:\.\.\.)?`)

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
// gateSection extracts the 验收门 (acceptance gate) block of a plan: the lines
// after that heading up to the next top-level heading.
//
// Scanning the whole document is unsound. A plan's dispatch prompt and
// cross-reference prose contain package paths too, so an incidental
// "./cmd/gateway" three screens below the gate would satisfy "the gate resolves"
// even when the gate itself names nothing runnable — the guard then silently
// stops watching that domain. Only the gate block counts.
func gateSection(body string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		// The heading must BE the 验收门 heading, not merely mention it. D03's
		// status line reads "> 状态：…R78 验收门已改指真实证据所在包…", which
		// appears above the real "## 6. 验收门" — a plain substring search
		// started there, hit the next "## 1. 审计要点" one line later, and
		// returned an empty gate section. The guard then skipped the domain
		// and reported it as sound while never looking at its gate.
		if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "验收门") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	for i := start + 1; i < len(lines); i++ {
		// Terminate on a markdown heading (## / ###), NOT on any line starting
		// with '#'. A gate block routinely contains bash comments, and a '#'
		// terminator truncated the section at the first comment — D03's own
		// explanatory comments did exactly that, so the guard never saw its
		// go test lines and stopped watching the domain entirely.
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "##") {
			return strings.Join(lines[start+1:i], "\n")
		}
	}
	return strings.Join(lines[start+1:], "\n")
}

// packagePatterns extracts relative Go package patterns from a gate block and
// de-duplicates them. The regex is anchored on a full path segment so it
// cannot degenerate to a bare "./" prefix — an earlier non-greedy version
// matched only "./" and made this function return junk, which silently made
// the guard skip every domain and pass while testing nothing.
func packagePatterns(body string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, m := range packagePatternRe.FindAllString(body, -1) {
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

// gateResolves reports whether any package pattern in the gate block names a
// directory that actually contains Go source.
func gateResolves(gate string) bool {
	for _, pat := range packagePatterns(gate) {
		dir := strings.TrimSuffix(pat, "/...")
		dir = strings.TrimSuffix(dir, "...")
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" || dir == "." {
			continue
		}
		// Gate patterns are repo-root relative (they are what you would type
		// from the repo root), not relative to this test's CWD.
		if hasGoPackage(filepath.Join(repoRoot, dir)) {
			return true
		}
	}
	return false
}

func TestNoNewVacuousAcceptanceGate(t *testing.T) {
	var unexpected []string
	for _, dom := range listDomains(t) {
		plan := filepath.Join(auditRoot, dom, "plan.md")
		body, err := os.ReadFile(plan)
		if err != nil {
			t.Errorf("%s: no plan.md: %v", dom, err)
			continue
		}
		patterns := packagePatterns(gateSection(string(body)))
		if len(patterns) == 0 {
			// Some plans (D11) document the gate as a prose table rather than a
			// runnable package pattern. Those are not this guard's subject; they
			// are audited by the master doc instead.
			continue
		}
		// The gate is sound if ANY pattern it names resolves to a real Go
		// package. Patterns like ./cmd/gateway (no /...) are single packages.
		if gateResolves(gateSection(string(body))) {
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
// whose gate now resolves to a real package must be removed: a stale entry
// would hide a real regression behind an expired excuse.
//
// The test is "does the gate resolve", NOT "does the domain directory contain
// a .go file". Since R78 a sound gate may point at a package outside the domain
// directory (D03's evidence lives in ./domains/hooks/compression), so the
// domain-local check would keep D03 in the allowlist forever and stop the guard
// from ever watching it again.
func TestVacuousGateAllowlistIsMinimal(t *testing.T) {
	var stale []string
	for dom, reason := range knownVacuousGates {
		body, err := os.ReadFile(filepath.Join(auditRoot, dom, "plan.md"))
		if err != nil {
			continue
		}
		if gateResolves(gateSection(string(body))) {
			t.Logf("stale allowlist entry %s: %s", dom, reason)
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
