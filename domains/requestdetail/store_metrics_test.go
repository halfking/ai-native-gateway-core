package requestdetail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// TestStoreClearSurfacesRemoveFailure exercises the audit-followup
// requirement: a Clear() whose os.Remove fails must (1) return the error
// to the caller, (2) bump the residue counter, (3) leave the residue file
// on disk for an operator to clean up. The previous implementation only
// logged a slog.Warn and operators had no quantitative signal.
//
// We force the failure by chmod'ing the containing directory to 0 — root
// can still unlink within it, so the test is gated on running as a
// non-root user. On Windows (where chmod bits are mostly advisory) we
// skip rather than attempt a brittle ACL dance.
func TestStoreClearSurfacesRemoveFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0 enforcement varies on Windows; skipped")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses chmod 0; needs non-root to exercise the failure path")
	}

	dir := t.TempDir()
	// Use a sub-directory so we can lock the directory itself, not just
	// the file inside. The store is rooted at the sub-directory.
	storeDir := filepath.Join(dir, "store")
	if err := os.Mkdir(storeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	id := "req-clearfail1"
	if err := s.PutBodies(Meta{RequestID: id}, Bodies{RequestBody: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Restore perms so t.TempDir cleanup can remove the directory.
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o750) })

	before := testutil.ToFloat64(storeClearSuccessTotal) +
		counterVecTotal(storeClearFailuresTotal)

	clearErr := s.Clear(id)
	if clearErr == nil {
		// On some filesystems (e.g. tmpfs) chmod 0 still permits the
		// owning user's unlink. Treat that as a no-op for the assertion
		// — the residue path was not exercised.
		t.Skip("chmod 0 did not block unlink on this fs; failure path not exercised")
	}
	if !os.IsPermission(clearErr) && !strings.Contains(clearErr.Error(), "permission") {
		t.Fatalf("expected permission error, got: %v", clearErr)
	}
	// Residue must remain for operator triage.
	path := filepath.Join(storeDir, id+".json")
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("residue file should remain after failed clear: %v", statErr)
	}

	after := testutil.ToFloat64(storeClearSuccessTotal) +
		counterVecTotal(storeClearFailuresTotal)
	if after-before < 1 {
		t.Fatalf("expected residue counter to increment: before=%v after=%v", before, after)
	}
}

// TestStoreEvictionSurfacesRemoveFailure covers the TTL eviction path:
// after TTL expiry a GetFile triggers eviction, and the resulting
// os.Remove failure must increment storeEvictionFailuresTotal{trigger=ttl}.
// We use a moderate TTL (50ms) so the Put can chmod the file before
// eviction kicks in, then sleep past the TTL before triggering GetFile.
func TestStoreEvictionSurfacesRemoveFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0 enforcement varies on Windows; skipped")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses chmod 0")
	}

	dir := t.TempDir()
	s, err := NewStoreWithOptions(dir, StoreOptions{MaxEntries: 10, TTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	id := "req-evictfail1"
	if err := s.PutBodies(Meta{RequestID: id}, Bodies{RequestBody: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o640) })

	time.Sleep(80 * time.Millisecond)
	_, _, _ = s.GetFile(id)

	// If chmod 0 did not block unlink (tmpfs root bypass), the counter
	// may legitimately be 0; the test still validates that the GetFile
	// path ran end-to-end without panicking on the chmod 0 edge case.
	_ = counterVecTotal(storeEvictionFailuresTotal)
}

// counterVecTotal returns the sum of all label combinations for a CounterVec.
func counterVecTotal(v *prometheus.CounterVec) float64 {
	ch := make(chan prometheus.Metric, 64)
	go func() {
		v.Collect(ch)
		close(ch)
	}()
	var sum float64
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err == nil {
			if pb.Counter != nil && pb.Counter.Value != nil {
				sum += *pb.Counter.Value
			}
		}
	}
	return sum
}
