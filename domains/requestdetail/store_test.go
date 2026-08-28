package requestdetail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePutGetClear(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reqID := "req-test-abcdef01"
	status := "in_progress"
	meta := Meta{RequestID: reqID, TenantID: "default", Status: &status}
	body := Bodies{RequestBody: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`)}
	if err := s.Put(meta, &body); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetMeta(reqID)
	if !ok || got.TenantID != "default" {
		t.Fatalf("meta miss: %+v ok=%v", got, ok)
	}
	file, ok, err := s.GetFile(reqID)
	if err != nil || !ok {
		t.Fatalf("file miss: ok=%v err=%v", ok, err)
	}
	if string(file.Bodies.RequestBody) == "" {
		t.Fatal("empty request body on disk")
	}
	if filepath.Base(s.filePath(reqID)) != reqID+".json" {
		t.Fatalf("unexpected path %s", s.filePath(reqID))
	}
	if err := s.Clear(reqID); err != nil {
		t.Fatal(err)
	}
	if s.HasLocal(reqID) {
		t.Fatal("expected cleared")
	}
}

func TestStoreRejectsUnsafeID(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = s.PutMeta(Meta{RequestID: "../etc/passwd"})
	if err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestLocatorMemoryThenDB(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reqID := "req-locatortest01"
	_ = s.PutMeta(Meta{RequestID: reqID, TenantID: "t1"})
	reader := &fakeBodies{requestLogs: map[string]Bodies{
		"other": {RequestBody: json.RawMessage(`{}`)},
	}}
	loc := &Locator{Store: s, Bodies: reader}
	d, err := loc.Get(context.Background(), reqID, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != SourceMemory || d.Persistence != PersistenceInFlight {
		t.Fatalf("got source=%s persistence=%s", d.Source, d.Persistence)
	}

	_ = s.Clear(reqID)
	dbID := "req-dbfallback001"
	if reader.meta == nil {
		reader.meta = map[string]Meta{}
	}
	reader.requestLogs[dbID] = Bodies{RequestBody: json.RawMessage(`{"a":1}`)}
	reader.meta[dbID] = Meta{RequestID: dbID, TenantID: "t2"}
	d, err = loc.Get(context.Background(), dbID, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != SourceRequestLogs || d.Bodies == nil {
		t.Fatalf("expected request_logs with bodies, got %+v", d)
	}

	_, err = loc.Get(context.Background(), "req-missing00001", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

type fakeBodies struct {
	requestLogs  map[string]Bodies
	sessionTurns map[string]Bodies
	meta         map[string]Meta
}

func (f *fakeBodies) ReadRequestLogsBodies(_ context.Context, requestID string, omitBody bool) (Bodies, Meta, error) {
	if f.meta == nil {
		f.meta = map[string]Meta{}
	}
	b, ok := f.requestLogs[requestID]
	if !ok {
		return Bodies{}, Meta{}, ErrNotFound
	}
	m := f.meta[requestID]
	if m.RequestID == "" {
		m.RequestID = requestID
	}
	return b, m, nil
}

func (f *fakeBodies) ReadSessionTurnsBodies(_ context.Context, requestID string, omitBody bool) (Bodies, Meta, error) {
	if f.sessionTurns == nil {
		return Bodies{}, Meta{}, ErrNotFound
	}
	b, ok := f.sessionTurns[requestID]
	if !ok {
		return Bodies{}, Meta{}, ErrNotFound
	}
	return b, Meta{RequestID: requestID}, nil
}

// partialMetaBodies simulates the DB returning request_logs metadata when the
// body row is missing (TTL purge, partial persistence, historical migration).
// requestLogsMeta records which ids still have metadata even though their
// body row was dropped; requestLogs keeps ids that still have bodies.
type partialMetaBodies struct {
	requestLogs      map[string]Bodies
	requestLogsMeta  map[string]Meta
	sessionTurns     map[string]Bodies
}

func (f *partialMetaBodies) ReadRequestLogsBodies(_ context.Context, requestID string, omitBody bool) (Bodies, Meta, error) {
	if b, ok := f.requestLogs[requestID]; ok {
		m := f.requestLogsMeta[requestID]
		if m.RequestID == "" {
			m.RequestID = requestID
		}
		return b, m, nil
	}
	if m, ok := f.requestLogsMeta[requestID]; ok {
		// Mirror pgBodyReader: keep the metadata and surface ErrNotFound so
		// the locator can fall back to session_turns / metadata-only.
		return Bodies{}, m, ErrNotFound
	}
	return Bodies{}, Meta{}, ErrNotFound
}

func (f *partialMetaBodies) ReadSessionTurnsBodies(_ context.Context, requestID string, omitBody bool) (Bodies, Meta, error) {
	if f.sessionTurns == nil {
		return Bodies{}, Meta{}, ErrNotFound
	}
	b, ok := f.sessionTurns[requestID]
	if !ok {
		return Bodies{}, Meta{}, ErrNotFound
	}
	return b, Meta{RequestID: requestID}, nil
}

// TestLocatorRequestLogsMetadataFallback covers the case where request_logs
// still has the metadata row but its body row is gone. The locator should:
// 1) fall back to session_turns bodies if available, merging the metadata,
// 2) otherwise return a metadata-only detail (not 404) with a Warning,
// 3) still return ErrNotFound when neither store has any signal at all.
func TestLocatorRequestLogsMetadataFallback(t *testing.T) {
	t.Run("session_turns_supplies_body_when_request_logs_body_missing", func(t *testing.T) {
		reqID := "req-meta-fb-001"
		reader := &partialMetaBodies{
			requestLogsMeta: map[string]Meta{
				reqID: {RequestID: reqID, TenantID: "tenant-a"},
			},
			sessionTurns: map[string]Bodies{
				reqID: {RequestBody: json.RawMessage(`{"prompt":"hi"}`)},
			},
		}
		loc := &Locator{Bodies: reader}
		d, err := loc.Get(context.Background(), reqID, false)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if d.Source != SourceSessionTurns {
			t.Fatalf("expected session_turns source, got %s", d.Source)
		}
		if d.Persistence != PersistencePersisted {
			t.Fatalf("expected persisted, got %s", d.Persistence)
		}
		if d.Meta.TenantID != "tenant-a" {
			t.Fatalf("merged meta lost tenant: %+v", d.Meta)
		}
		if d.Bodies == nil || string(d.Bodies.RequestBody) == "" {
			t.Fatalf("expected session_turns body, got %+v", d.Bodies)
		}
		if d.Warning != "" {
			t.Fatalf("did not expect warning, got %q", d.Warning)
		}
	})

	t.Run("metadata_only_when_both_bodies_missing", func(t *testing.T) {
		reqID := "req-meta-fb-002"
		reader := &partialMetaBodies{
			requestLogsMeta: map[string]Meta{
				reqID: {RequestID: reqID, TenantID: "tenant-b", Status: ptrStr("success")},
			},
		}
		loc := &Locator{Bodies: reader}
		d, err := loc.Get(context.Background(), reqID, false)
		if err != nil {
			t.Fatalf("metadata-only should not error, got: %v", err)
		}
		if d == nil {
			t.Fatal("expected metadata-only detail, got nil")
		}
		if d.Source != SourceRequestLogs {
			t.Fatalf("expected request_logs source, got %s", d.Source)
		}
		if d.Persistence != PersistencePersisted {
			t.Fatalf("expected persisted, got %s", d.Persistence)
		}
		if d.Meta.TenantID != "tenant-b" {
			t.Fatalf("metadata not returned: %+v", d.Meta)
		}
		if d.Bodies != nil {
			t.Fatalf("expected nil bodies, got %+v", d.Bodies)
		}
		if d.Warning == "" {
			t.Fatal("expected warning for metadata-only fallback")
		}
	})

	t.Run("not_found_when_neither_store_has_signal", func(t *testing.T) {
		reader := &partialMetaBodies{}
		loc := &Locator{Bodies: reader}
		_, err := loc.Get(context.Background(), "req-meta-fb-missing", false)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("omit_body_returns_metadata_only_without_querying_bodies", func(t *testing.T) {
		reqID := "req-meta-fb-003"
		// Session turns has bodies, but omitBody=true must skip loading them.
		// We assert this by leaving the session_turns map nil — if the locator
		// tries to read bodies the call would still succeed (no rows) but the
		// stronger guarantee is that the returned Detail has no Bodies.
		reader := &partialMetaBodies{
			requestLogs: map[string]Bodies{
				reqID: {RequestBody: json.RawMessage(`{"x":1}`), ResponseBody: json.RawMessage(`{"y":2}`)},
			},
			requestLogsMeta: map[string]Meta{
				reqID: {RequestID: reqID, TenantID: "tenant-c"},
			},
		}
		loc := &Locator{Bodies: reader}
		d, err := loc.Get(context.Background(), reqID, true)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if d.Source != SourceRequestLogs {
			t.Fatalf("expected request_logs, got %s", d.Source)
		}
		if d.Bodies != nil {
			t.Fatalf("omit_body=true must not populate bodies, got %+v", d.Bodies)
		}
		if d.Warning != "" {
			t.Fatalf("did not expect warning when bodies exist, got %q", d.Warning)
		}
	})
}

func TestStoreRetentionCapacityEvictsOldest(t *testing.T) {
	s, err := NewStoreWithOptions(t.TempDir(), StoreOptions{MaxEntries: 1, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta(Meta{RequestID: "req-oldest1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMeta(Meta{RequestID: "req-newest1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.GetMeta("req-oldest1"); ok {
		t.Fatal("oldest entry should be evicted")
	}
	if _, ok := s.GetMeta("req-newest1"); !ok {
		t.Fatal("newest entry should remain")
	}
}

func TestStoreRetentionTTLRemovesMemoryAndFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStoreWithOptions(dir, StoreOptions{MaxEntries: 10, TTL: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	id := "req-expiring1"
	if err := s.PutBodies(Meta{RequestID: id}, Bodies{RequestBody: json.RawMessage(`{"x":1}`)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, ok := s.GetMeta(id); ok {
		t.Fatal("expired metadata should be absent")
	}
	if _, ok, err := s.GetFile(id); err != nil || ok {
		t.Fatalf("expired file should be absent: ok=%v err=%v", ok, err)
	}
}

func TestStoreStartupCleanupRemovesExpiredSnapshots(t *testing.T) {
	dir := t.TempDir()
	id := "req-startup1"
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, []byte(`{"meta":{"request_id":"`+id+`"},"bodies":{}}`), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoreWithOptions(dir, StoreOptions{TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired startup snapshot remains: %v", err)
	}
}

func ptrStr(s string) *string { return &s }

type sessionErrorBodies struct{}

func (sessionErrorBodies) ReadRequestLogsBodies(_ context.Context, requestID string, _ bool) (Bodies, Meta, error) {
	return Bodies{}, Meta{RequestID: requestID, TenantID: "tenant-a"}, ErrNotFound
}

func (sessionErrorBodies) ReadSessionTurnsBodies(context.Context, string, bool) (Bodies, Meta, error) {
	return Bodies{}, Meta{}, context.DeadlineExceeded
}

func TestLocatorPropagatesSessionTurnErrors(t *testing.T) {
	_, err := (&Locator{Bodies: sessionErrorBodies{}}).Get(context.Background(), "req-session-error", false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected session error to propagate, got %v", err)
	}
}

func TestGetFileRejectsOversizedBody(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a body file slightly over 10MB limit
	reqID := "req-large-body"
	// Build a valid JSON array that exceeds MaxBodyFileSize
	largeArray := make([]string, 0, 200000)
	for i := 0; i < 200000; i++ {
		largeArray = append(largeArray, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx") // 50 chars each
	}
	largeBodyJSON, _ := json.Marshal(largeArray)
	
	meta := Meta{RequestID: reqID, TenantID: "default"}
	bodies := Bodies{RequestBody: json.RawMessage(largeBodyJSON)}

	// Write directly to disk to bypass any Put validation
	payload := filePayload{Meta: meta, Bodies: bodies}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(s.filePath(reqID), raw, 0o640); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Verify file is actually over limit
	info, _ := os.Stat(s.filePath(reqID))
	if info.Size() <= MaxBodyFileSize {
		t.Fatalf("test file size %d is not over limit %d", info.Size(), MaxBodyFileSize)
	}

	// Attempt to read should fail with size limit error
	_, ok, err := s.GetFile(reqID)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for oversized body")
	}
	if err.Error() != "requestdetail: body file exceeds 10485760 bytes" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGetFileAcceptsBodyAtLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a body file just under 10MB limit with valid JSON
	reqID := "req-at-limit"
	// Build a valid JSON array that is close to but under MaxBodyFileSize
	largeArray := make([]string, 0, 150000)
	for i := 0; i < 150000; i++ {
		largeArray = append(largeArray, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx") // 50 chars each
	}
	largeBodyJSON, _ := json.Marshal(largeArray)

	meta := Meta{RequestID: reqID, TenantID: "default"}
	bodies := Bodies{RequestBody: json.RawMessage(largeBodyJSON)}

	if err := s.Put(meta, &bodies); err != nil {
		t.Fatalf("failed to put body at limit: %v", err)
	}

	// Verify file is under limit
	info, _ := os.Stat(s.filePath(reqID))
	if info.Size() > MaxBodyFileSize {
		t.Fatalf("test file size %d exceeds limit %d", info.Size(), MaxBodyFileSize)
	}

	// Should succeed
	payload, ok, err := s.GetFile(reqID)
	if err != nil {
		t.Fatalf("expected no error for body under limit, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for body under limit")
	}
	if len(payload.Bodies.RequestBody) != len(largeBodyJSON) {
		t.Fatalf("expected body size %d, got %d", len(largeBodyJSON), len(payload.Bodies.RequestBody))
	}
}

