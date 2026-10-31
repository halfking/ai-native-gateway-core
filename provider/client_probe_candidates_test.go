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
	// ★ 2026-10-28 夹具对齐现网 schema：providers 表没有 name 列（只有
	// code + display_name，01-schema.sql / 211 现网实测一致）。此前夹具写
	// `INSERT INTO providers (…, name, …)` ⇒ 列不存在 ⇒ 整个测试在真库上
	// 恒红——这就是批判式审计 §2 记的两条「与本轮无关的既有失败」。
	// 本轮按仓内其它真库夹具的形态（admin/*_integration_test.go）改列。
	err = tx.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, base_url, protocol, enabled)
		VALUES ($1, $2, $2, 'https://api.example.com', 'openai', TRUE)
		RETURNING id
	`, tt.tenant, "probe-test-"+tt.tenant).Scan(tt.providerID)
		if err != nil {
			t.Fatalf("failed to create provider for %s: %v", tt.tenant, err)
		}
		// credentials.label NOT NULL（本仓与 211 现网一致）——夹具必须给值。
		err = tx.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, label, lifecycle_status, availability_state, status)
			VALUES ($1, $2, 'active', 'ready', 'active')
			RETURNING id
		`, *tt.providerID, "probe-test-"+tt.tenant).Scan(tt.credID)
		if err != nil {
			t.Fatalf("failed to create credential for %s: %v", tt.tenant, err)
		}
		// ★ 2026-10-28：model_offers 是视图（credential_model_bindings ⋈
		// provider_models），不可 INSERT——夹具此前写视图必然 55000。改写
		// 两张底表（与 modality_mismatch_realdb_test.go 的真库夹具同形态）：
		// provider_models 挂 canonical_id（alias EXISTS 臂靠它匹配），binding
		// 挂 billing_mode。
		// ★ modality 不显式给：本地 llm_gateway_test 的 provider_models
		// 尚无该列（落后于 01-schema/211），列存在处 DEFAULT 'text' 兜住；
		// 显式写列会在旧库上 42703。
		var pmID int
		err = tx.QueryRow(ctx, `
			INSERT INTO provider_models (provider_id, raw_model_name, canonical_raw_name, standardized_name, canonical_id, available)
			VALUES ($1, 'probe-test-alias', 'probe-test-alias', 'probe-test-alias', $2, TRUE)
			RETURNING id
		`, *tt.providerID, canonicalID).Scan(&pmID)
		if err != nil {
			t.Fatalf("failed to create provider_model for %s: %v", tt.tenant, err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO credential_model_bindings (credential_id, provider_model_id, billing_mode)
			VALUES ($1, $2, 'per_token')
		`, *tt.credID, pmID)
		if err != nil {
			t.Fatalf("failed to create binding for %s: %v", tt.tenant, err)
		}
	}

	// Keep one otherwise-routable credential in a periodic quota state.
	// Probe candidates must not revive it while the reset window is open.
	var periodicProviderID, periodicCredID int
	// 同上：name → code + display_name（夹具对齐现网 schema）。
	if err = tx.QueryRow(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, base_url, protocol, enabled)
		VALUES ('default', 'probe-test-periodic', 'probe-test-periodic', 'https://api.example.com', 'openai', TRUE)
		RETURNING id
	`).Scan(&periodicProviderID); err != nil {
		t.Fatalf("failed to create periodic provider: %v", err)
	}
	if err = tx.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, label, lifecycle_status, availability_state, status, quota_state)
		VALUES ($1, 'probe-test-periodic', 'active', 'ready', 'active', 'periodic_exhausted')
		RETURNING id
	`, periodicProviderID).Scan(&periodicCredID); err != nil {
		t.Fatalf("failed to create periodic credential: %v", err)
	}
	var periodicPMID int
	if err = tx.QueryRow(ctx, `
		INSERT INTO provider_models (provider_id, raw_model_name, canonical_raw_name, standardized_name, canonical_id, available)
		VALUES ($1, 'probe-test-alias', 'probe-test-alias', 'probe-test-alias', $2, TRUE)
		RETURNING id
	`, periodicProviderID, canonicalID).Scan(&periodicPMID); err != nil {
		t.Fatalf("failed to create periodic provider_model: %v", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO credential_model_bindings (credential_id, provider_model_id, billing_mode)
		VALUES ($1, $2, 'per_token')
	`, periodicCredID, periodicPMID)
	if err != nil {
		t.Fatalf("failed to create periodic binding: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("failed to commit test data: %v", err)
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 清理按底表（model_offers 是视图）：先 binding 后 provider_models，
		// 再凭据/供应商/别名/canonical。
		_, _ = pool.Exec(cctx, `DELETE FROM credential_model_bindings WHERE provider_model_id IN (SELECT id FROM provider_models WHERE raw_model_name = 'probe-test-alias')`)
		_, _ = pool.Exec(cctx, `DELETE FROM provider_models WHERE raw_model_name = 'probe-test-alias'`)
		_, _ = pool.Exec(cctx, `DELETE FROM credentials WHERE id IN ($1, $2, $3)`, defCredID, otherCredID, periodicCredID)
		_, _ = pool.Exec(cctx, `DELETE FROM providers WHERE id IN ($1, $2, $3)`, defProviderID, otherProviderID, periodicProviderID)
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

	// (3) tenant isolation and quota gating: other-tenant and periodic-quota
	// credentials must not be returned for the default tenant.
	for _, c := range cands {
		if c.CredentialID == otherCredID {
			t.Errorf("probe leaked other-tenant credential %d into default tenant", otherCredID)
		}
		if c.CredentialID == periodicCredID {
			t.Errorf("probe returned periodic-exhausted credential %d", periodicCredID)
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

	// (5) normal request candidate loading must use the same canonical-id path,
	// not only GetProbeCandidates. This is the GLM-5.2 regression: an offer can
	// retain a provider-facing raw name while canonical_id points at glm-5.2.
	runtimeCands, err := client.loadCandidatesDB(ctx, "probe-test-model", "default")
	if err != nil {
		t.Fatalf("loadCandidatesDB by canonical_name errored: %v", err)
	}
	foundRuntime := false
	for _, c := range runtimeCands {
		if c.CredentialID == defCredID && c.OfferRawModel == "probe-test-alias" {
			foundRuntime = true
			break
		}
	}
	if !foundRuntime {
		t.Errorf("loadCandidatesDB by canonical_name did not return provider-facing offer, got=%v", runtimeCands)
	}

	// (6) empty model short-circuits to nil,nil (no DB roundtrip).
	if got, err := client.GetProbeCandidates(ctx, "   ", "", "default"); err != nil || got != nil {
		t.Errorf("empty model = (%v,%v), want (nil,nil)", got, err)
	}
}
