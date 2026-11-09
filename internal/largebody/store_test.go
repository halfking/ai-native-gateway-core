package largebody

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCaptureRecoverAndRange(t *testing.T) {
	s := newTestStore(t)
	payload := []byte(`{"messages":[{"role":"user","content":"原文😀"}],"model":"m"}`)
	id := Identity{TenantID: "tenant", SessionID: "session"}
	ref, err := s.Capture(context.Background(), id, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if ref.Size != int64(len(payload)) || ref.SHA256 != hex.EncodeToString(sum[:]) || ref.RequestID == "" {
		t.Fatalf("bad ref: %+v", ref)
	}
	f, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	_ = f.Close()
	got, err := s.ReadRange(context.Background(), ref, Span{Offset: 2, Length: 9}, 16)
	if err != nil || !bytes.Equal(got, payload[2:11]) {
		t.Fatalf("range %q %v", got, err)
	}
	_, err = s.ReadRange(context.Background(), ref, Span{Offset: 0, Length: 17}, 16)
	if !errors.Is(err, ErrRange) {
		t.Fatalf("oversized range: %v", err)
	}
	recovered, err := s.Recover(context.Background(), ref.Identity)
	if err != nil || recovered != ref {
		t.Fatalf("recover %+v %v", recovered, err)
	}
	other, err := NewStore(s.RootPath())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Recover(context.Background(), ref.Identity); err != nil {
		t.Fatal(err)
	}
	_, err = s.Capture(context.Background(), ref.Identity, strings.NewReader("overwrite"))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("expected immutable collision, got %v", err)
	}
	if err = s.Verify(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
}

type fillReader struct{ remaining int64 }

func (r *fillReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.remaining -= n
	return int(n), nil
}

func TestCaptureExact100MBAndRejectExtraByte(t *testing.T) {
	s := newTestStore(t)
	ref, err := s.Capture(context.Background(), Identity{"tenant", "session", "exact"}, &fillReader{remaining: LimitBytes})
	if err != nil || ref.Size != LimitBytes {
		t.Fatalf("exact boundary %+v %v", ref, err)
	}
	if err = s.Verify(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadRange(context.Background(), ref, Span{Offset: 0, Length: 9 << 20}, LimitBytes); !errors.Is(err, ErrRange) {
		t.Fatalf("unbounded materialization allowed: %v", err)
	}
	_, err = s.Capture(context.Background(), Identity{"tenant", "session", "over"}, &fillReader{remaining: LimitBytes + 1})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.RootPath(), "tenant", "se", "session", "large-v1", "requests", "over"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial files left: %v %v", entries, err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCaptureCancellationIOErrorAndTamper(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Capture(ctx, Identity{"tenant", "session", "cancel"}, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Capture(context.Background(), Identity{"tenant", "session", "broken"}, brokenReader{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	ref, err := s.Capture(context.Background(), Identity{"tenant", "session", "tamper"}, strings.NewReader("same-size"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.RootPath(), ref.RelativePath()), []byte("bad!-size"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Verify(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tamper: %v", err)
	}
	if _, err = s.Recover(context.Background(), ref.Identity); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("recovery accepted tamper: %v", err)
	}
}

func TestStoreRejectsTraversalAndSymlinks(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []Identity{{"../escape", "s", "r"}, {"t", "../s", "r"}, {"t", "s", "../r"}, {"t", "s", "."}, {"t", "s", "CON"}} {
		if _, err := s.Capture(context.Background(), id, strings.NewReader("x")); err == nil {
			t.Fatalf("accepted %+v", id)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.RootPath(), "linked")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := s.Capture(context.Background(), Identity{"linked", "session", "r"}, strings.NewReader("x")); err == nil {
		t.Fatal("followed symlink")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote outside store")
	}
	inside := filepath.Join(s.RootPath(), "inside")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(s.RootPath(), "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(context.Background(), Identity{"alias", "session", "r"}, strings.NewReader("x")); err == nil {
		t.Fatal("followed internal tenant alias")
	}
}

func TestRecoverDoesNotPromoteUncommittedRAW(t *testing.T) {
	s := newTestStore(t)
	ref, err := s.Capture(context.Background(), Identity{"tenant", "session", "orphan"}, strings.NewReader("original"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.RootPath(), requestDir(ref.Identity), "ref.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lookup(context.Background(), ref.Identity); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("promoted uncommitted RAW: %v", err)
	}
	if _, err = s.Capture(context.Background(), ref.Identity, strings.NewReader("overwrite")); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrote orphan: %v", err)
	}
}
