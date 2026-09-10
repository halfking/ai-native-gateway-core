package store

import (
	"context"
	"testing"
	"time"
)

func TestProbeHealthEvidenceSchemaModesAndStrictHealth(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		mode KeySchemaMode
	}{
		{"legacy", KeySchemaModeLegacy},
		{"dual", KeySchemaModeDual},
		{"canonical", KeySchemaModeCanonical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mr := newTestStore(t)
			defer mr.Close()
			s.SetKeySchemaMode(tc.mode)
			prefix, tenant, model := "evidence:"+tc.name+":", "tenant:one", "m:1"
			legacy := NodeKeyForTenant(prefix, tenant, 7, model)
			k2, err := K2KeySetForTenant(prefix, tenant, 7, model)
			if err != nil {
				t.Fatal(err)
			}
			key := legacy
			if tc.mode != KeySchemaModeLegacy {
				key = k2.Node
			}
			mr.HSet(key, "available", "1")
			evidence, err := s.ProbeHealthEvidence(ctx, prefix, tenant, 7, []string{model, "missing"})
			if err != nil {
				t.Fatal(err)
			}
			if !evidence[0].Known || !evidence[0].Healthy {
				t.Fatalf("fresh explicit healthy seed = %+v", evidence[0])
			}
			if evidence[1].Known || evidence[1].Healthy {
				t.Fatalf("missing evidence = %+v", evidence[1])
			}

			mr.HSet(key, "disabled", "1", "cool_until_ms", "1")
			evidence, err = s.ProbeHealthEvidence(ctx, prefix, tenant, 7, []string{model})
			if err != nil || !evidence[0].Known || evidence[0].Healthy {
				t.Fatalf("expired cool must not half-open: %+v err=%v", evidence[0], err)
			}
		})
	}
}

func TestProbeHealthEvidenceDualFallbackAndCanonicalIsolation(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestStore(t)
	defer mr.Close()
	prefix, tenant, model := "evidence:isolation:", "t", "m"
	legacy := NodeKeyForTenant(prefix, tenant, 9, model)
	mr.HSet(legacy, "available", "1")

	s.SetKeySchemaMode(KeySchemaModeDual)
	got, err := s.ProbeHealthEvidence(ctx, prefix, tenant, 9, []string{model})
	if err != nil || !got[0].Known || !got[0].Healthy {
		t.Fatalf("dual legacy fallback=%+v err=%v", got, err)
	}
	s.SetKeySchemaMode(KeySchemaModeCanonical)
	got, err = s.ProbeHealthEvidence(ctx, prefix, tenant, 9, []string{model})
	if err != nil || got[0].Known {
		t.Fatalf("canonical must not read legacy=%+v err=%v", got, err)
	}
}

func TestProbeHealthEvidenceRejectsMalformedAndLegacyRequestState(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestStore(t)
	defer mr.Close()
	key := NodeKeyForTenant("evidence:bad:", "t", 1, "m")
	for _, fields := range []map[string]string{
		{"available": "yes"},
		{"available": "1", "fail_streak": "bad"},
		{"available": "1", "health": "bogus"},
		{"available": "1", "event_seq": "1"},
		{"available": "1", "last_request_at_ms": "bad"},
		{"available": "1", "last_request_at_ms": "100", "last_request_failed": "wat"},
	} {
		mr.Del(key)
		for k, v := range fields {
			mr.HSet(key, k, v)
		}
		got, err := s.ProbeHealthEvidence(ctx, "evidence:bad:", "t", 1, []string{"m"})
		if err != nil || got[0].Known {
			t.Fatalf("fields=%v got=%+v err=%v", fields, got, err)
		}
	}
}

func TestProbeHealthEvidenceRequestWatermarks(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestStore(t)
	defer mr.Close()
	key := NodeKeyForTenant("evidence:watermark:", "t", 1, "m")
	mr.HSet(key, "available", "1", "last_request_at_ms", "101", "last_request_failed", "0", "last_request_error_at_ms", "100")
	got, err := s.ProbeHealthEvidence(ctx, "evidence:watermark:", "t", 1, []string{"m"})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Known || got[0].LastRequestFailed || !got[0].LastRequestAt.Equal(time.UnixMilli(101)) || !got[0].LastRequestErrorAt.Equal(time.UnixMilli(100)) {
		t.Fatalf("watermarks=%+v", got[0])
	}
}
