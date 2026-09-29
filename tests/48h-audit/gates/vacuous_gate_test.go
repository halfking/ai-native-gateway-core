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
var knownVacuousGates = map[string]string{}

// auditRoot is tests/48h-audit, the parent of this package.
const auditRoot = "../"

// repoRoot is the module root. This test's CWD is the package directory
// tests/48h-audit/gates, so the root is THREE levels up (gates -> 48h-audit ->
// tests -> repo). Getting this wrong is silent and catastrophic: with two
// levels, every path resolved to <repo>/tests/<pattern> and hasGoFileWithSuffix
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

// hasGoFileWithSuffix reports whether dir contains a file with the given
// suffix — and, when recursive, anything under it, since a `./dir/...`
// pattern includes subdirectories. Without the recursion this helper
// reported D01/D02/D14 as package-less when their tests live in business/
// safety/ stress/ subpackages, which is the normal layout. When !recursive,
// subdirectories are excluded: `go test ./pkg` runs exactly that package,
// so a test file below it proves nothing (N20-2).
//
// N20-2 (2026-09-29 audit round 21) is embodied in the callers: `go test`
// on a package without test files prints "no test files" and exits 0 — a
// gate naming such a package still cannot fail — so gateResolves demands a
// *_test.go behind test lines and only a plain .go behind build/vet lines.
func hasGoFileWithSuffix(dir, suffix string, recursive bool) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipAll
		}
		if !d.IsDir() {
			if strings.HasSuffix(d.Name(), suffix) {
				found = true
				return filepath.SkipAll
			}
			return nil
		}
		if !recursive && path != dir {
			return filepath.SkipDir
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

// patternRef binds an extracted package pattern to the go subcommand that
// invokes it. The distinction decides what "resolves" means (N20-2): a
// `go test ./pkg` line whose package holds only non-test .go files prints
// "no test files" and exits 0 — vacuous green — so it needs a *_test.go
// behind the pattern. `go build`/`go vet` already act on plain .go source,
// so for those any .go file makes the line meaningful.
type patternRef struct {
	cmd     string
	pattern string
	// recursive mirrors a `/...` suffix: only then do subdirectories count.
	// A single-package pattern like `./cmd/gateway` must not be satisfied by
	// a _test.go in one of its children — `go test ./cmd/gateway` runs
	// exactly that package and nothing under it.
	recursive bool
}

// packagePatterns extracts the go invocations from a gate block as
// (subcommand, pattern) pairs, de-duplicated on the pair. The regex is
// anchored on a full path segment so it cannot degenerate to a bare "./"
// prefix — an earlier non-greedy version matched only "./" and made this
// function return junk, which silently made the guard skip every domain and
// pass while testing nothing.
func packagePatterns(gate string) []patternRef {
	seen := make(map[string]struct{})
	var out []patternRef
	for _, line := range strings.Split(gate, "\n") {
		// Only lines that actually invoke a go tool count. A gate block often
		// contains prose explaining where the evidence lives ("证据在 ./proxy
		// 的健康探测回归"), and a path there proves nothing — `go test` never
		// runs it. Accepting prose paths is the same category of bug as
		// scanning the whole document: the gate can stop naming anything
		// runnable while the guard still reports it sound. It bit this guard
		// for real — D12/D15/D16 all carry a prose path that kept a mutated,
		// non-runnable gate green.
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "go ") {
			continue
		}
		fields := strings.Fields(trimmed)
		cmd := ""
		if len(fields) > 1 {
			cmd = fields[1]
		}
		for _, m := range packagePatternRe.FindAllString(line, -1) {
			ref := patternRef{cmd: cmd, pattern: m, recursive: strings.HasSuffix(m, "/...")}
			key := ref.cmd + "\x00" + ref.pattern
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ref)
		}
	}
	return out
}

// gateResolves reports whether any pattern in the gate block names a
// directory backed by the kind of Go file its command consumes: *_test.go
// for `go test` lines (N20-2 — a test-less package exits 0 without running
// anything), any .go file for build/vet lines.
func gateResolves(gate string) bool {
	for _, ref := range packagePatterns(gate) {
		dir := strings.TrimSuffix(ref.pattern, "/...")
		dir = strings.TrimSuffix(dir, "...")
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" || dir == "." {
			continue
		}
		// Gate patterns are repo-root relative (they are what you would type
		// from the repo root), not relative to this test's CWD.
		suffix := ".go"
		if ref.cmd == "test" {
			suffix = "_test.go"
		}
		if hasGoFileWithSuffix(filepath.Join(repoRoot, dir), suffix, ref.recursive) {
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

// TestPackagePatternsExtractsSubcommand pins the (cmd, pattern) extraction the
// N20-2 kind-awareness depends on: the subcommand comes from the second word
// of the invoking line, recursiveness from the `/...` suffix, and dedup is on
// the pair (the same pattern under two commands is two different claims).
func TestPackagePatternsExtractsSubcommand(t *testing.T) {
	gate := strings.Join([]string{
		"go build ./...",
		"go vet ./...",
		"go test -race -timeout 120s ./dom/business/... -count=1",
		"go test ./cmd/single -run 'TestX' -count=1",
		"prose mentioning ./dom/business is not a go invocation",
	}, "\n")
	got := packagePatterns(gate)
	want := []patternRef{
		{cmd: "build", pattern: "./...", recursive: true},
		{cmd: "vet", pattern: "./...", recursive: true},
		{cmd: "test", pattern: "./dom/business/...", recursive: true},
		{cmd: "test", pattern: "./cmd/single", recursive: false},
	}
	if len(got) != len(want) {
		t.Fatalf("packagePatterns = %+v, want %+v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("packagePatterns[%d] = %+v, want %+v", i, g, want[i])
		}
	}
}

// TestHasGoFileWithSuffixScopesAndRecursion covers the walker semantics that
// gateResolves delegates to: suffix filtering (_test.go vs plain .go), and
// subdirectory exclusion for single-package patterns (N20-2 — `go test ./pkg`
// runs exactly that package, so a test file below it proves nothing).
func TestHasGoFileWithSuffixScopesAndRecursion(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "impl.go"), "package tmp\n")
	write(filepath.Join(sub, "impl_test.go"), "package tmp\n")

	if hasGoFileWithSuffix(dir, "_test.go", false) {
		t.Error("non-recursive: _test.go found although it only exists in a subdirectory")
	}
	if !hasGoFileWithSuffix(dir, "_test.go", true) {
		t.Error("recursive: _test.go in subdirectory not found")
	}
	if !hasGoFileWithSuffix(dir, ".go", false) {
		t.Error("plain .go in the directory itself not found")
	}
}

// TestGateResolvesKindAwareness is the N20-2 regression proper: a `go test`
// line behind a test-less package is vacuous green (`go test` prints "no
// test files" and exits 0) and must not count as resolving, while the same
// package behind `go build` does — build consumes plain .go source.
//
// The negative cases are pinned to a real test-less package directory. If
// cmd/env-injector ever grows a _test.go, repoint this test at whichever
// production package is then test-less; do not delete the case.
func TestGateResolvesKindAwareness(t *testing.T) {
	const testLess = "./cmd/env-injector"
	if hasGoFileWithSuffix(filepath.Join(repoRoot, testLess), "_test.go", false) {
		t.Fatalf("%s gained a _test.go — repoint TestGateResolvesKindAwareness at a test-less package", testLess)
	}
	if !hasGoFileWithSuffix(filepath.Join(repoRoot, testLess), ".go", false) {
		t.Fatalf("%s lost its .go files — repoint TestGateResolvesKindAwareness at a test-less package", testLess)
	}

	if gateResolves("go test -race -timeout 60s "+testLess+"\n") {
		t.Errorf("go test line behind test-less package %s resolved: gate can fail only by accident", testLess)
	}
	if !gateResolves("go build "+testLess+"\n") {
		t.Errorf("go build line behind plain-source package %s did not resolve: build gates need only .go", testLess)
	}
	// The composed shape every domain plan uses: the test line is what can
	// make the gate fail, not the repo-wide build/vet (./... is skipped).
	if gateResolves(strings.Join([]string{
		"go build ./...",
		"go vet ./...",
		"go test -race -timeout 60s " + testLess + " -count=1",
	}, "\n")) {
		t.Error("gate resolved via ./... build/vet lines although its only test pattern is test-less")
	}
}
