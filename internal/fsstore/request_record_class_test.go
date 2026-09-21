package fsstore

import (
	"testing"
	"time"
)

// V6-W1.6 R8 / migration 610 parity: the FS persistence tier (requests
// temporarily saved as files when PG is unavailable) must round-trip the
// request class + due time exactly like request_logs_hot does.

func TestRequestRecordClassRoundTrip(t *testing.T) {
	s, _ := tempStore(t)
	defer s.Close()

	due := time.Unix(1800000123, 0).UTC()
	rec := RequestRecord{
		ID:           "cls-1",
		StartedAt:    time.Now().UTC(),
		TenantID:     "t1",
		Model:        "gpt4",
		RequestClass: "scheduled",
		DueAt:        &due,
	}
	if err := s.PutRequest(rec); err != nil {
		t.Fatalf("PutRequest: %v", err)
	}
	got, err := s.GetRequest("cls-1")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.RequestClass != "scheduled" {
		t.Fatalf("RequestClass = %q, want scheduled", got.RequestClass)
	}
	if got.DueAt == nil || !got.DueAt.Equal(due) {
		t.Fatalf("DueAt = %v, want %v", got.DueAt, due)
	}

	// Immediate requests: empty class + nil due must survive too (the DB
	// column default is 'immediate'; the FS tier mirrors that at read time).
	rec2 := RequestRecord{ID: "cls-2", StartedAt: time.Now().UTC()}
	if err := s.PutRequest(rec2); err != nil {
		t.Fatalf("PutRequest(2): %v", err)
	}
	got2, err := s.GetRequest("cls-2")
	if err != nil {
		t.Fatalf("GetRequest(2): %v", err)
	}
	if got2.RequestClass != "" || got2.DueAt != nil {
		t.Fatalf("immediate record mutated: %+v", got2)
	}
}

func TestRequestDocIndexesClass(t *testing.T) {
	// Empty class indexes as 'immediate' (DB NOT NULL DEFAULT parity).
	doc := requestDoc(RequestRecord{ID: "d1"})
	if doc["request_class"] != "immediate" {
		t.Fatalf("default class index = %v, want immediate", doc["request_class"])
	}
	doc = requestDoc(RequestRecord{ID: "d2", RequestClass: "scheduled"})
	if doc["request_class"] != "scheduled" {
		t.Fatalf("explicit class index = %v", doc["request_class"])
	}
}
