package store

import (
	"context"
	"testing"
	"time"
)

func TestRecordRequestTracksRequestEvidenceAndPreservesErrorWatermark(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		mode KeySchemaMode
	}{
		{"legacy", KeySchemaModeLegacy}, {"dual", KeySchemaModeDual}, {"canonical", KeySchemaModeCanonical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mr := newTestStore(t)
			defer mr.Close()
			s.SetKeySchemaMode(tc.mode)
			ks := NodeKeySetForTenant("request:evidence:"+tc.name+":", "t", 1, "m")
			record := func(success bool, now int64, dedup string) RecordResult {
				r, err := s.RecordRequestKeySet(ctx, ks, RecordOutcome{Success: success, ErrorKind: "timeout", NowMs: now, RequestID: dedup, DedupKey: dedup, NodeTTL: time.Hour, Window5mTTL: time.Hour, Window30mTTL: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			if r := record(false, 100, "failure"); r.Status != "applied" {
				t.Fatalf("failure=%+v", r)
			}
			if r := record(true, 101, "success"); r.Status != "applied" {
				t.Fatalf("success=%+v", r)
			}
			if r := record(false, 101, "same-ms-failure"); r.Status != "applied" {
				t.Fatalf("same-ms=%+v", r)
			}
			if r := record(true, 102, "success-after-error"); r.Status != "applied" {
				t.Fatalf("after=%+v", r)
			}
			if r := record(false, 103, "duplicate"); r.Status != "applied" {
				t.Fatalf("first dup=%+v", r)
			}
			if r := record(true, 104, "duplicate"); r.Status != "duplicate" {
				t.Fatalf("duplicate=%+v", r)
			}

			keys := []string{ks.Node}
			if tc.mode != KeySchemaModeLegacy {
				k2, err := K2KeySetForTenant(ks.Prefix, ks.TenantID, ks.CredentialID, ks.RawModel)
				if err != nil {
					t.Fatal(err)
				}
				keys = append(keys, k2.Node)
			}
			for _, key := range keys {
				if got := mr.HGet(key, "last_request_at_ms"); got != "103" {
					t.Fatalf("%s last_request_at_ms=%q want 103", key, got)
				}
				if got := mr.HGet(key, "last_request_failed"); got != "1" {
					t.Fatalf("%s last_request_failed=%q want 1", key, got)
				}
				if got := mr.HGet(key, "last_request_error_at_ms"); got != "103" {
					t.Fatalf("%s error watermark=%q want 103", key, got)
				}
			}
		})
	}
}

func TestRecordRequestEvidenceRunsBeforePriorityGuard(t *testing.T) {
	s, mr := newTestStore(t)
	defer mr.Close()
	ctx := context.Background()
	ks := NodeKeySetForTenant("request:evidence:priority:", "t", 1, "m")
	mr.HSet(ks.Node, "source_priority", "20", "available", "1")
	_, err := s.RecordRequestKeySet(ctx, ks, RecordOutcome{Success: false, ErrorKind: "timeout", NowMs: 100, RequestID: "r", NodeTTL: time.Hour, Window5mTTL: time.Hour, Window30mTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if got := mr.HGet(ks.Node, "last_request_error_at_ms"); got != "100" {
		t.Fatalf("error watermark=%q", got)
	}
}
