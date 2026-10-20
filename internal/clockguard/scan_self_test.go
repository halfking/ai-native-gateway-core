package clockguard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFixture materialises a test file inside a temporary tree.
func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

// TestScannerFindsZeroMarginLowerBound proves the scanner **has teeth**:
// a fixture whose clock lower bound equals the duration it waits for must be
// reported.
//
// ★ 量具也要被量：a gate whose scanner silently matches nothing is green
// forever. This is the scanner's own mutation check.
func TestScannerFindsZeroMarginLowerBound(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "bad_test.go", `package fixture

import (
	"context"
	"testing"
	"time"
)

func TestFixtureZeroMargin(t *testing.T) {
	opts := struct{ TTL time.Duration }{TTL: 200 * time.Millisecond}
	_ = opts
	start := time.Now()
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("too fast: %v", elapsed)
	}
	_ = context.Background()
}
`)
	bounds, err := FindZeroMarginLowerBounds(dir, "")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(bounds) != 1 {
		t.Fatalf("scanner reported %d zero-margin bounds, want 1: %+v", len(bounds), bounds)
	}
	b := bounds[0]
	if b.Func != "TestFixtureZeroMargin" {
		t.Errorf("func = %q, want TestFixtureZeroMargin", b.Func)
	}
	if b.Bound != 200*time.Millisecond {
		t.Errorf("bound = %v, want 200ms", b.Bound)
	}
	if b.WaitKey != "TTL" {
		t.Errorf("wait key = %q, want TTL", b.WaitKey)
	}
	if b.Variable != "elapsed" {
		t.Errorf("variable = %q, want elapsed", b.Variable)
	}
}

// TestScannerAcceptsACoarseLowerBound is the **negative** half: a lower bound
// that is a *coarse* expression of the intent (a quarter of the natural
// value) is exactly what the fix prescribes ⇒ it must NOT be reported.
func TestScannerAcceptsACoarseLowerBound(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "good_test.go", `package fixture

import (
	"testing"
	"time"
)

func TestFixtureCoarseBound(t *testing.T) {
	opts := struct{ TTL time.Duration }{TTL: 200 * time.Millisecond}
	_ = opts
	start := time.Now()
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Fatalf("too fast: %v", elapsed)
	}
}
`)
	bounds, err := FindZeroMarginLowerBounds(dir, "")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(bounds) != 0 {
		t.Fatalf("scanner reported %d bounds, want 0: %+v", len(bounds), bounds)
	}
}

// TestScannerIgnoresUnrelatedComparisons keeps the scanner from crying wolf:
// a clock compared against a *different* wait, and a non-clock comparison,
// are both clean.
func TestScannerIgnoresUnrelatedComparisons(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "clean_test.go", `package fixture

import (
	"testing"
	"time"
)

func TestFixtureDifferentWait(t *testing.T) {
	opts := struct{ TTL time.Duration }{TTL: 1 * time.Second}
	_ = opts
	start := time.Now()
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("too fast: %v", elapsed)
	}
}

func TestFixtureNotAClock(t *testing.T) {
	opts := struct{ TTL time.Duration }{TTL: 200 * time.Millisecond}
	_ = opts
	count := 3
	if count < 200 {
		t.Fatal("count")
	}
}
`)
	bounds, err := FindZeroMarginLowerBounds(dir, "")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(bounds) != 0 {
		t.Fatalf("scanner reported %d bounds, want 0: %+v", len(bounds), bounds)
	}
}
