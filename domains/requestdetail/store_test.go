package requestdetail

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
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

func (f *fakeBodies) ReadRequestLogsBodies(_ context.Context, requestID string) (Bodies, Meta, error) {
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

func (f *fakeBodies) ReadSessionTurnsBodies(_ context.Context, requestID string) (Bodies, Meta, error) {
	if f.sessionTurns == nil {
		return Bodies{}, Meta{}, ErrNotFound
	}
	b, ok := f.sessionTurns[requestID]
	if !ok {
		return Bodies{}, Meta{}, ErrNotFound
	}
	return b, Meta{RequestID: requestID}, nil
}
