package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestModelModalityMismatch_OnRealData exercises the diagnosis against real
// catalog data.
//
// ★ Why this test exists and what it protects:
//
//	The diagnosis has one load-bearing distinction:
//
//		routable + wrong modality  → "modality_mismatch" (change the request)
//		not routable               → no mismatch       (retry later)
//		routable + right modality  → no mismatch       (something else)
//
//	Every unit test of it goes through a stub resolver, so it cannot catch a
//	mistake in the SQL: a query that returns the wrong columns, or one whose
//	joins make "routable" always false, would make the diagnosis silently
//	inert — every real zero-candidate exit would fall back to the old 503
//	and the incident would recur with a green build.
//
//	The assertions below are on the DATA, not on the code path: they pin the
//	diagnosis's answer for models whose registration the test can look up
//	independently.
//
//	Requires TEST_DATABASE_URL; skipped otherwise (same convention as
//	provider/modality_gate_alignment_test.go).
func TestModelModalityMismatch_OnRealData(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — the modality diagnosis check needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	c := &Client{}
	c.dbPool = pool

	// ── Pick reference models from the catalog itself ──────────────────────
	// The expectations are derived from models_canonical.modality, which the
	// diagnosis is supposed to report — but the ROUTABILITY side is checked
	// against the router's own view, independently. That is the pair the
	// diagnosis hinges on.

	type ref struct {
		name         string
		wantMismatch bool
	}
	var refs []ref

	// A model registered as multimodal: a vision request is fine for it.
	rows, err := pool.Query(ctx, `
		SELECT mc.canonical_name
		  FROM models_canonical mc
		  JOIN provider_models pm ON pm.canonical_id = mc.id
		  JOIN v_routable_credential_models v ON v.provider_model_id = pm.id
		 WHERE v.is_routable = TRUE
		   AND COALESCE(mc.modality,'text') = 'multimodal'
		   AND COALESCE(mc.status,'active') = 'active'
		 GROUP BY mc.canonical_name
		 LIMIT 3`)
	if err != nil {
		t.Fatalf("query multimodal refs: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		refs = append(refs, ref{name: n, wantMismatch: false})
	}
	rows.Close()

	// A model registered as text but still routable: a vision request against
	// it IS a modality mismatch.
	rows, err = pool.Query(ctx, `
		SELECT mc.canonical_name
		  FROM models_canonical mc
		  JOIN provider_models pm ON pm.canonical_id = mc.id
		  JOIN v_routable_credential_models v ON v.provider_model_id = pm.id
		 WHERE v.is_routable = TRUE
		   AND COALESCE(mc.modality,'text') = 'text'
		   AND COALESCE(mc.status,'active') = 'active'
		 GROUP BY mc.canonical_name
		 LIMIT 3`)
	if err != nil {
		t.Fatalf("query text refs: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		refs = append(refs, ref{name: n, wantMismatch: true})
	}
	rows.Close()

	if len(refs) == 0 {
		t.Skip("no routable models in this catalog to exercise the diagnosis against")
	}

	for _, rf := range refs {
		got, err := c.ModelModalityMismatch(ctx, rf.name, "", "default", "vision")
		if err != nil {
			t.Errorf("%s: %v", rf.name, err)
			continue
		}
		isMismatch := !got.IsZero()
		if isMismatch != rf.wantMismatch {
			t.Errorf("%s: mismatch=%v, want %v (detail: %+v)",
				rf.name, isMismatch, rf.wantMismatch, got)
			continue
		}
		if isMismatch && got.RegisteredModality != "text" {
			t.Errorf("%s: reported registered modality %q, want %q",
				rf.name, got.RegisteredModality, "text")
		}
	}

	// An unknown model must never be reported as a modality mismatch: it is
	// an invalid_model case, and the handler's ordering depends on it.
	got, err := c.ModelModalityMismatch(ctx, "definitely-not-a-real-model-xyz", "", "default", "vision")
	if err != nil {
		t.Fatalf("unknown model: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("unknown model must not be a modality mismatch, got %+v", got)
	}
}
