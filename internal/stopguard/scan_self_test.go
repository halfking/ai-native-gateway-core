package stopguard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScannerHasTeeth is the scanner's own self-test.
//
// A gate that cannot fail is not a gate: the scanner must demonstrably
// report the unsafe shape AND accept the safe ones, otherwise a green run
// proves nothing. The fixtures below are written to a temp directory so the
// scanner is exercised on real Go source, not on a mock.
//
// The unsafe fixture is the shape that actually hung the gateway's
// shutdown on 2026-10-21 (R71): a worker that was constructed but never
// started blocks in Stop() forever.
func TestScannerHasTeeth(t *testing.T) {
	dir := t.TempDir()

	unsafeSrc := `package fixture

import "context"

type Unsafe struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *Unsafe) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}
`
	safeSrc := `package fixture

import (
	"context"
	"sync/atomic"
)

type SafeStartedFlag struct {
	started atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func (w *SafeStartedFlag) Start() { w.started.Store(true) }

func (w *SafeStartedFlag) Stop() {
	if !w.started.Load() {
		return
	}
	<-w.done
}

type SafeCancelNil struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *SafeCancelNil) Stop() {
	if w.cancel == nil {
		return
	}
	<-w.done
}

type SafeConditionalWait struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *SafeConditionalWait) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
	}
}

type SafeLocalAlias struct {
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func (w *SafeLocalAlias) Stop() {
	started := w.started
	if w.cancel != nil {
		w.cancel()
	}
	if started {
		<-w.done
	}
}

type NotAStopMethod struct{ done chan struct{} }

func (w *NotAStopMethod) Close() { <-w.done }
`
	if err := os.WriteFile(filepath.Join(dir, "unsafe.go"), []byte(unsafeSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "safe.go"), []byte(safeSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := ScanDir(dir)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if len(findings) != 1 {
		for _, f := range findings {
			t.Logf("finding: %s", f)
		}
		t.Fatalf("scanner reported %d findings, want exactly 1 (the unsafe fixture)", len(findings))
	}
	got := findings[0]
	if got.Type != "Unsafe" {
		t.Errorf("scanner flagged %q, want %q", got.Type, "Unsafe")
	}
	if got.File != "unsafe.go" {
		t.Errorf("scanner flagged file %q, want %q", got.File, "unsafe.go")
	}
	if got.Wait != "done" {
		t.Errorf("scanner reported wait %q, want %q", got.Wait, "done")
	}
}

// TestScannerIgnoresNonStopMethods pins the scope: only Stop() methods are
// inspected, so an unrelated Close() that waits on a channel is not a
// finding.
func TestScannerIgnoresNonStopMethods(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

type T struct{ done chan struct{} }

func (t *T) Close() { <-t.done }
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := ScanDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("scanner reported %d findings for a non-Stop method, want 0", len(findings))
	}
}
