// Unified Auto-Orchestration Plugin — Workflow C (Observability & Audit Log)
// Audit log tests. TDD: these tests describe the contract that audit.go must
// satisfy. They exercise the in-memory sink, payload hashing and the Auditor
// façade, plus a reflective guard against accidental raw-payload fields
// (design contract §9.2: never persist credential / token / raw confirmation
// — only a redacted payload hash).
package observ

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMemorySink_AppendAndOrder verifies append preserves insertion order.
func TestMemorySink_AppendAndOrder(t *testing.T) {
	s := NewMemorySink()
	ctx := context.Background()
	now := time.Now()
	first := AuditEvent{EventID: "e1", Type: EventDecision, Timestamp: now, Tenant: "t1"}
	second := AuditEvent{EventID: "e2", Type: EventAction, Timestamp: now.Add(time.Millisecond), Tenant: "t1"}
	if err := s.Append(ctx, first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := s.Append(ctx, second); err != nil {
		t.Fatalf("append second: %v", err)
	}
	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len=%d want 2", len(snap))
	}
	if snap[0].EventID != "e1" || snap[1].EventID != "e2" {
		t.Errorf("order broken: %s then %s", snap[0].EventID, snap[1].EventID)
	}
}

// TestMemorySink_Concurrent verifies the sink is safe under concurrent appends.
func TestMemorySink_Concurrent(t *testing.T) {
	s := NewMemorySink()
	ctx := context.Background()
	const n = 256
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			e := AuditEvent{
				EventID:   fmt.Sprintf("evt-%04d", i),
				Type:      EventDecision,
				Timestamp: time.Now(),
				Tenant:    "tenant-A",
			}
			if err := s.Append(ctx, e); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(s.Snapshot()); got != n {
		t.Errorf("snapshot len=%d want %d", got, n)
	}
}

// TestHashPayload_DeterministicAndHex verifies hashing is sha256 hex, stable,
// and collision-free for distinct inputs.
func TestHashPayload_DeterministicAndHex(t *testing.T) {
	a := HashPayload([]byte("hello"))
	b := HashPayload([]byte("hello"))
	if a != b {
		t.Fatalf("non-deterministic hash: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("hex length=%d want 64 (sha256)", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("not valid hex: %v", err)
	}
	c := HashPayload([]byte("world"))
	if a == c {
		t.Errorf("collision between hello and world: %s", a)
	}
	wantSum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(wantSum[:])
	if a != want {
		t.Errorf("hash=%s want %s", a, want)
	}
}

// TestAuditor_RecordWritesToSink verifies the Auditor façade forwards events
// to the configured sink with all §9.2 fields preserved.
func TestAuditor_RecordWritesToSink(t *testing.T) {
	s := NewMemorySink()
	a := NewAuditor(s)
	ctx := context.Background()
	payload := []byte("internal-decision-body-do-not-store")
	evt := AuditEvent{
		EventID:       "evt-42",
		Type:          EventDecision,
		Timestamp:     time.Now().UTC(),
		Tenant:        "tenant-A",
		GoalRun:       "gr-1",
		Session:       "s-1",
		Request:       "r-1",
		Task:          "task-1",
		CausationID:   "cause-1",
		CorrelationID: "corr-1",
		TraceID:       "trace-1",
		Attempt:       3,
		FencingToken:  "ft-1",
		PolicyVersion: "v1",
		ReasonCode:    "manual_required",
		PayloadHash:   HashPayload(payload),
	}
	if err := a.Record(ctx, evt); err != nil {
		t.Fatalf("record: %v", err)
	}
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len=%d want 1", len(snap))
	}
	got := snap[0]
	if got.EventID != evt.EventID || got.Tenant != evt.Tenant ||
		got.GoalRun != evt.GoalRun || got.Session != evt.Session ||
		got.Request != evt.Request || got.Task != evt.Task ||
		got.CausationID != evt.CausationID || got.CorrelationID != evt.CorrelationID ||
		got.TraceID != evt.TraceID || got.Attempt != evt.Attempt ||
		got.FencingToken != evt.FencingToken || got.PolicyVersion != evt.PolicyVersion ||
		got.ReasonCode != evt.ReasonCode || got.PayloadHash != evt.PayloadHash {
		t.Errorf("fields lost or altered:\n got=%+v\nwant=%+v", got, evt)
	}
}

// TestAuditEvent_NoRawPayloadField is a reflective guard against future
// contributors accidentally adding fields like names / token / rawPayload /
// secret. Per design §9.2 the audit log MUST NOT persist credentials,
// confirmation tokens or raw payloads — only the redacted payload hash.
func TestAuditEvent_NoRawPayloadField(t *testing.T) {
	typ := reflect.TypeOf(AuditEvent{})
	forbiddenPrefixes := []string{"Raw", "Secret", "Token", "Credential", "Payload"}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := f.Name
		if name == "PayloadHash" {
			continue // allowed: hash, not raw
		}
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("forbidden raw-payload field on AuditEvent: %s", name)
			}
		}
	}
}

// TestNewEventID_UniqueAndNonEmpty verifies generated IDs are non-empty and
// unique across many calls.
func TestNewEventID_UniqueAndNonEmpty(t *testing.T) {
	const n = 1024
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewEventID()
		if id == "" {
			t.Fatal("empty event id")
		}
		if len(id) < 8 {
			t.Errorf("event id too short (%d): %q", len(id), id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate event id after %d generations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestAuditor_PropagatesSinkError verifies a failing sink surfaces an error
// to the caller (we never silently swallow audit-write failures — see design
// §9.2 audit degraded handling).
func TestAuditor_PropagatesSinkError(t *testing.T) {
	failing := &errSink{err: fmt.Errorf("sink down")}
	a := NewAuditor(failing)
	err := a.Record(context.Background(), AuditEvent{EventID: "x", Type: EventAction})
	if err == nil {
		t.Fatal("expected error to propagate from failing sink")
	}
	if !strings.Contains(err.Error(), "sink down") {
		t.Errorf("unexpected error: %v", err)
	}
}

// errSink is a minimal Sink stub that always returns the configured error.
type errSink struct{ err error }

func (e *errSink) Append(_ context.Context, _ AuditEvent) error { return e.err }
