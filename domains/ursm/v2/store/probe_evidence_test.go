package store

import (
	"context"
	"fmt"
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

func TestProbeHealthEvidenceBatchedMixedKeySources(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestStore(t)
	defer mr.Close()
	prefix, tenant := "evidence:batch:", "tenant:one"
	models := []string{"m:k2only", "m:legacyonly", "m:both", "m:missing", "m:errored"}
	k2nodes := make(map[string]string, len(models))
	for _, m := range models {
		ks, err := K2KeySetForTenant(prefix, tenant, 7, m)
		if err != nil {
			t.Fatal(err)
		}
		k2nodes[m] = ks.Node
	}
	// k2-only: dual must read the canonical key.
	mr.HSet(k2nodes["m:k2only"], "available", "1")
	// legacy-only: dual must fall back to the legacy key on the k2 miss.
	mr.HSet(NodeKeyForTenant(prefix, tenant, 7, "m:legacyonly"), "available", "1")
	// both: the non-empty k2 hash must win even though the legacy hash says
	// the node is down.
	mr.HSet(k2nodes["m:both"], "available", "1")
	mr.HSet(NodeKeyForTenant(prefix, tenant, 7, "m:both"), "available", "0")
	// k2 errored: known but not healthy.
	mr.HSet(k2nodes["m:errored"], "available", "0")

	s.SetKeySchemaMode(KeySchemaModeDual)
	got, err := s.ProbeHealthEvidence(ctx, prefix, tenant, 7, models)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct{ known, healthy bool }{
		{true, true},  // k2-only
		{true, true},  // legacy-only via fallback
		{true, true},  // both → k2 wins
		{false, false}, // missing everywhere
		{true, false}, // k2 errored
	} {
		if got[i].RawModel != models[i] {
			t.Fatalf("order: [%d]=%q want %q", i, got[i].RawModel, models[i])
		}
		if got[i].Known != want.known || got[i].Healthy != want.healthy {
			t.Fatalf("%s = %+v, want known=%v healthy=%v", models[i], got[i], want.known, want.healthy)
		}
	}
}

func TestProbeHealthEvidenceLegacyTenantTupleBatch(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestStore(t)
	defer mr.Close()
	// Empty tenant: the canonical grammar cannot represent the tuple, so the
	// read source is decided purely by the schema mode.
	prefix := "evidence:batch-legacy:"
	mr.HSet(NodeKeyForTenant(prefix, "", 7, "m1"), "available", "1")

	s.SetKeySchemaMode(KeySchemaModeLegacy)
	got, err := s.ProbeHealthEvidence(ctx, prefix, "", 7, []string{"m1", "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Known || !got[0].Healthy || got[1].Known {
		t.Fatalf("legacy mode = %+v", got)
	}

	s.SetKeySchemaMode(KeySchemaModeCanonical)
	got, err = s.ProbeHealthEvidence(ctx, prefix, "", 7, []string{"m1", "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Known || got[1].Known {
		t.Fatalf("canonical mode must not read legacy for unrepresentable tuples = %+v", got)
	}
}

func TestProbeHealthEvidenceBatchLargeCredential(t *testing.T) {
	ctx := context.Background()
	s, mr := newTestStore(t)
	defer mr.Close()
	// The sibling limit is 500; exercise the batched path at that scale with
	// every model present only in the legacy hash (maximum dual fallback).
	prefix, tenant := "evidence:batch-large:", "tenant:one"
	models := make([]string, 0, probeNecessitySiblingLimitForStoreTest)
	for i := 0; i < probeNecessitySiblingLimitForStoreTest; i++ {
		m := fmt.Sprintf("m:%03d", i)
		models = append(models, m)
		mr.HSet(NodeKeyForTenant(prefix, tenant, 7, m), "available", "1")
	}
	s.SetKeySchemaMode(KeySchemaModeDual)
	got, err := s.ProbeHealthEvidence(ctx, prefix, tenant, 7, models)
	if err != nil {
		t.Fatal(err)
	}
	for i, ev := range got {
		if ev.RawModel != models[i] || !ev.Known || !ev.Healthy {
			t.Fatalf("[%d] = %+v", i, ev)
		}
	}
}

// probeNecessitySiblingLimitForStoreTest mirrors bg.probeNecessitySiblingLimit
// without importing the bg package.
const probeNecessitySiblingLimitForStoreTest = 500

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
