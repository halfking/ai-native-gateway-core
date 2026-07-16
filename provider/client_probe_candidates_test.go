package provider

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestGetProbeCandidates covers the 2026-07-17 audit P0 regression: the
// previous SQL referenced model_aliases.canonical_name, a column that does
// not exist on that table (only raw_name + canonical_id). PostgreSQL
// rejected the entire query, so GetProbeCandidates always errored and the
// executor silently fell back to the already-filtered params.Candidates —
// the no-candidate full-set probe never fired in production.
//
// This is an integration test (TEST_DATABASE_URL gated) because the bug is a
// SQL/column-reference error that a stub DB cannot catch — exactly why it
// slipped through the first time.
func TestGetProbeCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	dbURL := getTestDatabaseURL()
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	client := NewClient()
	client.SetDB(pool, "test-secret-key", "test-credential-key")

	// Seed: one canonical model, one provider/credential in the calling
	// tenant, one in a different tenant, and an alias row so the alias
	// EXISTS match path is exercised.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var canonicalID int
	err = tx.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, family, source, status)
		VALUES ('probe-test-model', 'test', 'manual', 'active')
		RETURNING id
	`).Scan(&canonicalID)
	if err != nil {
		t.Fatalf("failed to create canonical model: %v", err)
	}

	// alias: raw_name differs from canonical_name so the alias EXISTS path
	// is the only way to match the offer by canonical_id.
	_, err = tx.Exec(ctx, `
		INSERT INTO model_aliases (canonical_id, raw_name, status)
		VALUES ($1, 'probe-test-alias', 'active')
	`, canonicalID)
	if err != nil {
		t.Fatalf("failed to create alias: %v", err)
	}

	var defProviderID, otherProviderID, defCredID, otherCredID int
	for _, tt := range []struct {
		tenant     string
		providerID *int
		credID     *int
	}{
		{"default", &defProviderID, &defCredID},
		{"other-tenant", &otherProviderID, &otherCredID},
	} {
		err = tx.QueryRow(ctx, `
			INSERT INTO providers (tenant_id, name, base_url, protocol, enabled)
			VALUES ($1, $2, 'https://api.example.com', 'openai', TRUE)
			RETURNING id
		`, tt.tenant, "probe-test-"+tt.tenant).Scan(tt.providerID)
		if err != nil {
			t.Fatalf("failed to create provider for %s: %v", tt.tenant, err)
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, lifecycle_status, availability_state, status)
			VALUES ($1, 'active', 'ready', 'active')
			RETURNING id
		`, *tt.providerID).Scan(tt.credID)
		if err != nil {
			t.Fatalf("failed to create credential for %s: %v", tt.tenant, err)
		}
		// Offer keyed by canonical_raw_name; canonical_id wired so the alias
		// EXISTS path (ma.canonical_id = mo.canonical_id) can match.
		_, err = tx.Exec(ctx, `
			INSERT INTO model_offers (credential_id, raw_model_name, canonical_raw_name, canonical_id, billing_mode)
			VALUES ($1, 'probe-test-alias', 'probe-test-alias', $2, 'per_token')
		`, *tt.credID, canonicalID)
		if err != nil {
			t.Fatalf("failed to create offer for %s: %v", tt.tenant, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("failed to commit test data: %v", err)
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, `DELETE FROM model_offers WHERE raw_model_name = 'probe-test-alias'`)
		_, _ = pool.Exec(cctx, `DELETE FROM credentials WHERE id IN ($1, $2)`, defCredID, otherCredID)
		_, _ = pool.Exec(cctx, `DELETE FROM providers WHERE id IN ($1, $2)`, defProviderID, otherProviderID)
		_, _ = pool.Exec(cctx, `DELETE FROM model_aliases WHERE canonical_id = $1`, canonicalID)
		_, _ = pool.Exec(cctx, `DELETE FROM models_canonical WHERE id = $1`, canonicalID)
	}
	defer cleanup()

	// (1) P0: the query must not error on the bad column reference.
	cands, err := client.GetProbeCandidates(ctx, "probe-test-alias", "", "default")
	if err != nil {
		t.Fatalf("GetProbeCandidates returned error (P0 regression): %v", err)
	}

	// (2) alias path: the default-tenant offer matches via ma.canonical_id.
	found := false
	for _, c := range cands {
		if c.CredentialID == defCredID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("default tenant probe missing own credential (alias match path). got=%v", cands)
	}

	// (3) tenant isolation: the other-tenant offer must NOT leak in.
	for _, c := range cands {
		if c.CredentialID == otherCredID {
			t.Errorf("probe leaked other-tenant credential %d into default tenant", otherCredID)
		}
	}

	// (4) canonical_name match path: lookup by the models_canonical name
	// must resolve via mo.canonical_id → mc.canonical_name.
	candsCanon, err := client.GetProbeCandidates(ctx, "probe-test-model", "", "default")
	if err != nil {
		t.Fatalf("GetProbeCandidates by canonical_name errored: %v", err)
	}
	foundCanon := false
	for _, c := range candsCanon {
		if c.CredentialID == defCredID {
			foundCanon = true
			break
		}
	}
	if !foundCanon {
		t.Errorf("GetProbeCandidates by canonical_name did not match the offer, got=%v", candsCanon)
	}

	// (5) empty model short-circuits to nil,nil (no DB roundtrip).
	if got, err := client.GetProbeCandidates(ctx, "   ", "", "default"); err != nil || got != nil {
		t.Errorf("empty model = (%v,%v), want (nil,nil)", got, err)
	}
}
