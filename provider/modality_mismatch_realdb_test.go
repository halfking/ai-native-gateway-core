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

	// ── alias-only resolution (rolled back before return) ───────────────────
	//
	// ★ The critical audit (2026-10-28) found the first version of the
	// diagnosis SQL selected mo.canonical_id instead of the COALESCE with
	// the alias join, so an offer resolvable ONLY through model_aliases read
	// as "unresolved" and the diagnosis stayed silent for it. Production
	// data happened to contain no such offer at audit time — the defect was
	// LATENT, not active — which is exactly why it must be exercised with a
	// constructed row instead of assumed covered by the queries above.
	//
	// Everything runs in a transaction so the fixture cannot leak into the
	// catalog even if the test panics.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin alias fixture tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A text-only canonical reachable only via an alias, bound to a
	// routable credential copied from a live text-only reference (keeps the
	// routable side of the diagnosis true on real data).
	var refCred int
	var refPM int
	if err := tx.QueryRow(ctx, `
		SELECT v.credential_id, pm.id
		  FROM models_canonical mc
		  JOIN provider_models pm ON pm.canonical_id = mc.id
		  JOIN v_routable_credential_models v ON v.provider_model_id = pm.id
		 WHERE v.is_routable = TRUE AND COALESCE(mc.modality,'text') = 'text'
		 LIMIT 1`).Scan(&refCred, &refPM); err != nil {
		t.Skipf("no routable text-only binding to clone for the alias fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO models_canonical (canonical_name, family, status, modality, modality_source)
		VALUES ('zz-diag-alias-only', 'diag', 'active', 'text', 'manual')`); err != nil {
		t.Fatalf("insert fixture canonical: %v", err)
	}
	var fixtureCanonicalID int
	if err := tx.QueryRow(ctx,
		`SELECT id FROM models_canonical WHERE canonical_name = 'zz-diag-alias-only'`).Scan(&fixtureCanonicalID); err != nil {
		t.Fatalf("read fixture canonical id: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO model_aliases (raw_name, canonical_id, status)
		VALUES ('zz-diag-alias-name', $1, 'active')`, fixtureCanonicalID); err != nil {
		t.Fatalf("insert fixture alias: %v", err)
	}
	// An offer whose canonical_id is NULL — resolvable ONLY through the
	// alias by raw name. This is the shape the first SQL version missed.
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_models (provider_id, tenant_id, raw_model_name, canonical_raw_name, standardized_name, canonical_id, modality, available)
		SELECT pm.provider_id, pm.tenant_id, 'zz-diag-alias-name', 'zz-diag-alias-name', 'zz-diag-alias-name', NULL, 'text', true
		FROM provider_models pm WHERE pm.id = $1`, refPM); err != nil {
		t.Fatalf("insert fixture provider_model: %v", err)
	}
	var fixturePM int
	if err := tx.QueryRow(ctx,
		`SELECT id FROM provider_models WHERE raw_model_name = 'zz-diag-alias-name' LIMIT 1`).Scan(&fixturePM); err != nil {
		t.Fatalf("read fixture provider_model id: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO credential_model_bindings (credential_id, provider_model_id, available)
		VALUES ($1, $2, true)`, refCred, fixturePM); err != nil {
		t.Fatalf("insert fixture binding: %v", err)
	}

	// The diagnosis is asked about the ALIAS name, which only the alias arm
	// of the SQL can resolve to the fixture canonical.
	//
	// ★ Run the diagnosis SQL on the TX's connection, not through the
	// Client: ModelModalityMismatch takes a *pgxpool.Pool, and a pool hands
	// out a DIFFERENT connection — which cannot see this transaction's
	// uncommitted fixture rows. The first run of this test proved it the
	// hard way: the SQL was right (replayed manually inside the tx it
	// matched), the test still failed, because the diagnosis looked at the
	// catalog from another connection where the fixture does not exist.
	var registered string
	var canonical string
	var routable bool
	if err := tx.QueryRow(ctx, modalityMismatchSQL, "zz-diag-alias-name", "default").Scan(
		&registered, &canonical, &routable); err != nil {
		t.Fatalf("alias-only diagnosis on tx connection: %v", err)
	}
	if registered != "text" || !routable {
		t.Fatalf("alias-only fixture: diagnosis read modality=%q routable=%v, want text/true — "+
			"the diagnosis SQL has lost the alias arm (the 2026-10-28 audit regression: "+
			"selecting mo.canonical_id instead of COALESCE(mo.canonical_id, ma.canonical_id) "+
			"produced exactly this signature)",
			registered, routable)
	}
	// And the Go-side verdict on those facts must be the mismatch.
	if ModalityAccepts(registered, "vision") {
		t.Fatal("text registration must not accept vision — verdict layer drifted")
	}

	// An unknown model must never be reported as a modality mismatch: it is
	// an invalid_model case, and the handler's ordering depends on it.
	gotUnknown, err := c.ModelModalityMismatch(ctx, "definitely-not-a-real-model-xyz", "", "default", "vision")
	if err != nil {
		t.Fatalf("unknown model: %v", err)
	}
	if !gotUnknown.IsZero() {
		t.Errorf("unknown model must not be a modality mismatch, got %+v", gotUnknown)
	}
}
