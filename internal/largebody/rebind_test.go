package largebody

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRebindCommitsDestinationBeforeRemovingSourceAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "temporary", "request"}, strings.NewReader(`{"session_id":"actual","model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := s.Rebind(context.Background(), source, "actual")
	if err != nil {
		t.Fatal(err)
	}
	if actual.SessionID != "actual" || actual.TenantID != source.TenantID || actual.RequestID != source.RequestID || actual.SHA256 != source.SHA256 || actual.Size != source.Size {
		t.Fatalf("identity/content changed: %+v", actual)
	}
	if _, err = s.Lookup(context.Background(), actual.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lookup(context.Background(), source.Identity); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still committed: %v", err)
	}
	if _, err = os.Stat(filepath.Join(s.RootPath(), source.RelativePath())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source RAW remains: %v", err)
	}
	again, err := s.Rebind(context.Background(), source, "actual")
	if err != nil || again != actual {
		t.Fatalf("retry is not idempotent: %+v %v", again, err)
	}
	if same, err := s.Rebind(context.Background(), actual, "actual"); err != nil || same != actual {
		t.Fatalf("same-session: %+v %v", same, err)
	}
}

func TestRebindCollisionPreservesBothArchives(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.Capture(context.Background(), Identity{"tenant", "new", "r"}, strings.NewReader("target"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Rebind(context.Background(), source, "new"); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrote target: %v", err)
	}
	for _, ref := range []Ref{source, target} {
		if err = s.Verify(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRebindRejectsTamperAndUnsafeSession(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"", "../outside", "nested/path", "CON"} {
		if _, err = s.Rebind(context.Background(), source, session); err == nil {
			t.Fatalf("accepted session %q", session)
		}
		if err = s.Verify(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(s.RootPath(), source.RelativePath()), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Rebind(context.Background(), source, "new"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("accepted corrupt source: %v", err)
	}
}

func TestRebindCancellationAndDestinationFailurePreserveSource(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Rebind(ctx, source, "new"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	blocker := filepath.Join(s.RootPath(), "tenant", "ne", "new")
	if err = os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(blocker, []byte("not directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Rebind(context.Background(), source, "new"); err == nil {
		t.Fatal("ignored destination failure")
	}
	if _, err = s.Lookup(context.Background(), source.Identity); err != nil {
		t.Fatalf("failed move lost source: %v", err)
	}
}

func TestRebindRejectsDestinationSymlink(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	dest := filepath.Join(s.RootPath(), "tenant", "ne", "new")
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, dest); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err = s.Rebind(context.Background(), source, "new"); err == nil {
		t.Fatal("followed destination symlink")
	}
	if _, err = s.Lookup(context.Background(), source.Identity); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside write: %v %v", entries, err)
	}
}

func TestRebindConcurrentRetriesAcrossStoreHandles(t *testing.T) {
	s := newTestStore(t)
	other, err := NewStore(s.RootPath())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 32 {
		wg.Go(func() {
			store := s
			if i%2 == 1 {
				store = other
			}
			ref, err := store.Rebind(context.Background(), source, "new")
			if err == nil && (ref.SessionID != "new" || ref.SHA256 != source.SHA256) {
				err = errors.New("unexpected destination")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent retry: %v", err)
		}
	}
	target := source
	target.SessionID = "new"
	if _, err = s.Lookup(context.Background(), target.Identity); err != nil {
		t.Fatal(err)
	}
}

func TestRebindRejectsForgedReferenceAndCleansMatchingDuplicate(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	forged := source
	forged.SHA256 = strings.Repeat("0", 64)
	if _, err = s.Rebind(context.Background(), forged, "new"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("accepted forged ref: %v", err)
	}
	target, err := s.Capture(context.Background(), Identity{"tenant", "new", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Rebind(context.Background(), source, "new")
	if err != nil || got != target {
		t.Fatalf("matching duplicate: %+v %v", got, err)
	}
	if _, err = s.Lookup(context.Background(), source.Identity); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("left source reference: %v", err)
	}
}

func TestRebindMetadataCommitCollisionPreservesSource(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	target := source
	target.SessionID = "new"
	if err = s.ensureDirs(requestDir(target.Identity)); err != nil {
		t.Fatal(err)
	}
	if err = s.writeRef(requestDir(target.Identity), target); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Rebind(context.Background(), source, "new"); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrote commit record: %v", err)
	}
	if _, err = s.Lookup(context.Background(), source.Identity); err != nil {
		t.Fatalf("lost source after commit failure: %v", err)
	}
	if _, err = os.Stat(filepath.Join(s.RootPath(), target.RelativePath())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("left newly linked RAW: %v", err)
	}
}

func TestRebindRetryReportsCorruptDestination(t *testing.T) {
	s := newTestStore(t)
	source, err := s.Capture(context.Background(), Identity{"tenant", "old", "r"}, strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.Rebind(context.Background(), source, "new")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.RootPath(), target.RelativePath()), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Rebind(context.Background(), source, "new"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("hid corrupt destination: %v", err)
	}
}
