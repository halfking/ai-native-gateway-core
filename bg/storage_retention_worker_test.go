package bg

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestStorageRetentionWorkerStopBeforeStartDoesNotBlock(t *testing.T) {
	w := NewStorageRetentionWorker(nil, "", nil)
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked before Start")
	}
}

func TestStorageRetentionWorkerStartAndStopAreIdempotent(t *testing.T) {
	w := NewStorageRetentionWorker(nil, "", nil)
	w.CheckInterval = time.Hour
	w.Start(context.Background())
	w.Start(context.Background())
	w.Stop()
	w.Stop()
}

func TestStorageRetentionWorkerStopsAfterParentContextCancellation(t *testing.T) {
	w := NewStorageRetentionWorker(nil, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	cancel()
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked after context cancellation")
	}
}

// TestWalkDirSafe_AbortsAfterRepeatedIdenticalErrors (2026-08-31, P2-7
// audit-data-closure) pins that walkDirSafe aborts the traversal once the
// same error has been reported for walkDirSafeRepeatedErrorThreshold
// consecutive entries. We simulate this by injecting errors from inside
// the callback: returning the same error repeatedly for at least
// walkDirSafeRepeatedErrorThreshold entries must short-circuit the walk.
func TestWalkDirSafe_AbortsAfterRepeatedIdenticalErrors(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(root, "keep-"+strconv.Itoa(i)), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed file %d: %v", i, err)
		}
	}

	repeatedErr := errors.New("simulated repeated walk error")
	var visited int
	err := walkDirSafe(root, func(_ string, _ fs.DirEntry, _ error) error {
		visited++
		return repeatedErr
	})
	if err == nil {
		t.Fatalf("walkDirSafe must surface the simulated error after threshold hits, got nil")
	}
	if visited > walkDirSafeRepeatedErrorThreshold+2 {
		t.Fatalf("walkDirSafe must abort shortly after threshold (%d); visited=%d",
			walkDirSafeRepeatedErrorThreshold, visited)
	}
}

// TestWalkDirSafe_ToleratesIsolatedErrors (2026-08-31, P2-7) ensures that
// walkDirSafe continues past errors interleaved with successful entries, as
// long as the consecutive-count threshold is not reached.
func TestWalkDirSafe_ToleratesIsolatedErrors(t *testing.T) {
	root := t.TempDir()
	// Plant a symlink to a non-existent target so the walk itself
	// surfaces an "lstat ... no such file or directory" once for that
	// entry. That's a single isolated error and must not abort the
	// traversal.
	if err := os.Symlink("/nonexistent/llm-gateway-test-target", filepath.Join(root, "bad-link")); err != nil {
		t.Fatalf("seed bad symlink: %v", err)
	}
	for i := 0; i < 30; i++ {
		if err := os.WriteFile(filepath.Join(root, "f-"+strconv.Itoa(i)), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed file %d: %v", i, err)
		}
	}
	var visited int
	err := walkDirSafe(root, func(_ string, _ fs.DirEntry, _ error) error {
		visited++
		return nil
	})
	if err != nil {
		t.Fatalf("walkDirSafe returned err on isolated bad-symlink: %v", err)
	}
	if visited < 30 {
		t.Fatalf("walkDirSafe should have visited all regular files plus the symlink, got %d", visited)
	}
}

// TestWalkDirSafe_EmptyRootIsNoop (2026-08-31, P2-7) pins the safety check
// so a missing root directory never causes the cleanup helpers to error
// out the whole sweep.
func TestWalkDirSafe_EmptyRootIsNoop(t *testing.T) {
	if err := walkDirSafe("", func(_ string, _ fs.DirEntry, _ error) error {
		t.Fatal("callback should not be invoked for empty root")
		return nil
	}); err != nil {
		t.Fatalf("walkDirSafe(\"\") should be no-op, got %v", err)
	}
	if err := walkDirSafe("/llmgw/no/such/path/at/all", func(_ string, _ fs.DirEntry, _ error) error {
		t.Fatal("callback should not be invoked for missing root")
		return nil
	}); err != nil {
		t.Fatalf("walkDirSafe on missing root should be no-op, got %v", err)
	}
}
