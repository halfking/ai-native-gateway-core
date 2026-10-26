package clockguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root from this package's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestNoClockAssertionHasAZeroMarginLowerBound is the gate.
//
// A clock lower bound that equals the very duration its own test waits for
// has **zero margin by construction**: the two numbers are the same literal,
// so the measured value has to land within the scheduling jitter of that
// exact figure. Measured on admin/distlock (2026-10-21): the natural value
// was [200.03ms, 201.64ms] for a 200ms bound ⇒ worst-case margin 0.03ms
// (0.015%) ⇒ a false red waiting for any perturbation of the clock's
// resolution.
//
// ⇒ The rule: the lower bound must be a **coarse** expression of the intent
// ("not instant"), not a restatement of the natural value. The *discrimination*
// belongs to a behavioural assertion (e.g. `errors.Is(err, ErrTTLExpired)`),
// which cannot be faked by a slow machine.
func TestNoClockAssertionHasAZeroMarginLowerBound(t *testing.T) {
	root := repoRoot(t)
	bounds, err := FindZeroMarginLowerBounds(root, filepath.Join("internal", "clockguard"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, b := range bounds {
		t.Errorf("%s:%d (%s): clock lower bound %q on %q equals the duration this "+
			"same test waits for (%s) ⇒ margin is ZERO by construction ⇒ any "+
			"perturbation of the clock's resolution turns it into a false red.\n"+
			"  修法：下界表达**粗判意图**（如「不是瞬间」⇒ 取自然值的 1/4），"+
			"判别职责交给行为断言；上界只做宽松兜底。",
			b.File, b.Line, b.Func, b.BoundText, b.Variable, b.WaitKey)
	}
}

// TestScannerSkipsItsOwnPackage keeps the scanner from flagging the fixtures
// that prove the scanner works (they necessarily contain the pattern).
func TestScannerSkipsItsOwnPackage(t *testing.T) {
	root := repoRoot(t)
	bounds, err := FindZeroMarginLowerBounds(root, filepath.Join("internal", "clockguard"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, b := range bounds {
		if strings.Contains(b.File, "internal/clockguard") {
			t.Errorf("scanner flagged its own package: %s:%d", b.File, b.Line)
		}
	}
}

// TestScannerIsNotSilent guards the gate against the "found nothing because
// the scan did nothing" failure mode — the same shape as the
// "0 命中的第一嫌疑是量具坏了" lesson.
func TestScannerIsNotSilent(t *testing.T) {
	root := repoRoot(t)
	entries := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			entries++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if entries < 50 {
		t.Fatalf("scanned only %d test files — the scan found almost nothing, "+
			"so a green gate would prove nothing", entries)
	}
}
