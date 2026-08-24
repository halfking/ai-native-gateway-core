package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRoutingCandidateBindingUpdate_InputValidation covers the input-side
// guards added in 2026-07-24 for the candidate-binding PATCH endpoint:
// - wrong method
// - bad credential_id
// - missing raw_model
// - empty body (no fields to update)
// - out-of-range field values
//
// Database-touching paths require a live pgxpool; those are covered by
// integration tests, not by this handler-level guard test.
func TestRoutingCandidateBindingUpdate_InputValidation(t *testing.T) {
	h := &Handler{}

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantSubstr string
	}{
		{
			name:       "wrong method",
			method:     http.MethodGet,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
			wantSubstr: "method not allowed",
		},
		{
			name:       "bad credential id",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/not-a-number?raw_model=gpt-4",
			body:       `{"manual_priority": 1}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "invalid credential_id",
		},
		{
			name:       "missing raw_model",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123",
			body:       `{"manual_priority": 1}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "raw_model query parameter required",
		},
		{
			name:       "empty body",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "at least one of",
		},
		{
			name:       "routing_tier out of range",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{"routing_tier": 12}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "routing_tier must be in",
		},
		{
			name:       "weight out of range",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{"weight": -1}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "weight must be in",
		},
		{
			name:       "manual_priority out of range",
			method:     http.MethodPatch,
			path:       "/api/routing/candidate-binding/123?raw_model=gpt-4",
			body:       `{"manual_priority": 150}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "manual_priority must be in",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			h.handleRoutingCandidateBindingUpdate(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantSubstr)
			}
		})

	}
}

func TestValidateRoutingCandidateReorder(t *testing.T) {
	base := routingCandidateReorderRequest{
		CanonicalID: 42,
		Items: []routingCandidateReorderItem{
			{CredentialID: 10, RawModel: "gpt-4", ManualPriority: 1},
			{CredentialID: 20, RawModel: "gpt-4", ManualPriority: 2},
		},
	}
	cases := []struct {
		name       string
		mutate     func(*routingCandidateReorderRequest)
		wantSubstr string
	}{
		{name: "valid", mutate: func(*routingCandidateReorderRequest) {}, wantSubstr: ""},
		{name: "missing canonical", mutate: func(r *routingCandidateReorderRequest) {
			r.CanonicalID = 0
			r.RawModel = ""
		}, wantSubstr: "canonical_id or raw_model is required"},
		{name: "raw_model fallback valid", mutate: func(r *routingCandidateReorderRequest) {
			r.CanonicalID = 0
			r.RawModel = "gpt-4"
		}, wantSubstr: ""},
		{name: "empty items", mutate: func(r *routingCandidateReorderRequest) { r.Items = nil }, wantSubstr: "items must not be empty"},
		{name: "non-positive credential", mutate: func(r *routingCandidateReorderRequest) { r.Items[0].CredentialID = 0 }, wantSubstr: "credential_id must be positive"},
		{name: "duplicate binding", mutate: func(r *routingCandidateReorderRequest) {
			r.Items[1].CredentialID = r.Items[0].CredentialID
			r.Items[1].RawModel = r.Items[0].RawModel
		}, wantSubstr: "credential_id must be unique"},
		{name: "duplicate priority", mutate: func(r *routingCandidateReorderRequest) { r.Items[1].ManualPriority = 1 }, wantSubstr: "manual_priority must be unique"},
		{name: "priority below range", mutate: func(r *routingCandidateReorderRequest) { r.Items[0].ManualPriority = 0 }, wantSubstr: "manual_priority must be in"},
		{name: "priority above range", mutate: func(r *routingCandidateReorderRequest) { r.Items[1].ManualPriority = 100 }, wantSubstr: "manual_priority must be in"},
		{name: "spaced priorities allowed", mutate: func(r *routingCandidateReorderRequest) {
			r.Items[0].ManualPriority = 5
			r.Items[1].ManualPriority = 10
		}, wantSubstr: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			req.Items = append([]routingCandidateReorderItem(nil), base.Items...)
			tc.mutate(&req)
			got := validateRoutingCandidateReorder(req)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Fatalf("validation error = %q, want nil", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("validation error = %q, want substring %q", got, tc.wantSubstr)
			}
		})
	}
}

// TestHandleRoutingCandidateBindingReorder_InputValidation covers the
// pre-DB guards added in the optimistic-revision reorder rewrite:
// wrong method, bad JSON, missing canonical_id / expected_revision,
// and the existing structural checks.
// Cases that need a real pgxpool (BeginTx and beyond) live in the
// LLM_GATEWAY_PG_URL integration suite below.
func TestHandleRoutingCandidateBindingReorder_InputValidation(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name       string
		method     string
		body       string
		wantStatus int
		wantSubstr string
	}{
		{
			name:       "wrong method",
			method:     http.MethodGet,
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
			wantSubstr: "method not allowed",
		},
		{
			name:       "bad json",
			method:     http.MethodPatch,
			body:       `{`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "invalid body",
		},
		{
			name:       "missing canonical_id and raw_model",
			method:     http.MethodPatch,
			body:       `{"expected_revision":"abc","items":[{"credential_id":1,"manual_priority":1}]}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "canonical_id or raw_model is required",
		},
		{
			name:       "missing expected_revision",
			method:     http.MethodPatch,
			body:       `{"canonical_id":42,"items":[{"credential_id":1,"manual_priority":1}]}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "expected_revision is required",
		},
		{
			name:       "duplicate credential",
			method:     http.MethodPatch,
			body:       `{"canonical_id":42,"expected_revision":"abc","items":[{"credential_id":1,"raw_model":"gpt-4","manual_priority":1},{"credential_id":1,"raw_model":"claude-3","manual_priority":2}]}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "credential_id must be unique",
		},
		{
			name:       "empty items",
			method:     http.MethodPatch,
			body:       `{"canonical_id":42,"expected_revision":"abc","items":[]}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "items must not be empty",
		},
		{
			name:       "duplicate priority",
			method:     http.MethodPatch,
			body:       `{"canonical_id":42,"expected_revision":"abc","items":[{"credential_id":1,"manual_priority":1},{"credential_id":2,"manual_priority":1}]}`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "manual_priority must be unique",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/api/routing/candidate-bindings/reorder", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:1234"
			h.handleRoutingCandidateBindingReorder(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tc.wantSubstr)
			}
		})
	}
}

// TestCandidateReorderRevision_HashStable pins the deterministic shape of
// the reorder revision: equal scopes produce equal hashes regardless of
// fetch order, and any drift on ID / priority / updated_at produces a
// different hash. The hash is opaque to clients; this test guards against
// accidental sort or formatting regressions that would invalidate every
// outstanding client revision.
func TestCandidateReorderRevision_HashStable(t *testing.T) {
	t0 := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	rowsA := []reorderScopeRow{
		{ID: 1, CredentialID: 10, ManualPriority: 2, UpdatedAt: t0},
		{ID: 2, CredentialID: 20, ManualPriority: 1, UpdatedAt: t0},
	}
	rowsB := []reorderScopeRow{
		{ID: 2, CredentialID: 20, ManualPriority: 1, UpdatedAt: t0},
		{ID: 1, CredentialID: 10, ManualPriority: 2, UpdatedAt: t0},
	}
	hashA, err := candidateReorderRevision(rowsA)
	if err != nil {
		t.Fatalf("hash A failed: %v", err)
	}
	hashB, err := candidateReorderRevision(rowsB)
	if err != nil {
		t.Fatalf("hash B failed: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("reorder revision must be order-independent: %s vs %s", hashA, hashB)
	}

	rowsC := append([]reorderScopeRow{}, rowsA...)
	rowsC[0].ManualPriority = 3
	hashC, err := candidateReorderRevision(rowsC)
	if err != nil {
		t.Fatalf("hash C failed: %v", err)
	}
	if hashC == hashA {
		t.Fatalf("reorder revision must change when priorities drift")
	}
}

// reorderTestPool dials the integration Postgres used by the reorder suite.
// The suite is skipped entirely when LLM_GATEWAY_PG_URL is not set so the
// handler-level tests remain runnable in lightweight CI.
func reorderTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("LLM_GATEWAY_PG_URL"))
	if dsn == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set; skipping reorder integration test")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse LLM_GATEWAY_PG_URL: %v", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// reorderTestFixture creates a fresh provider/credentials/provider_models
// triple plus the requested binding rows. Each call uses a unique
// raw_model name so concurrent test runs and stale rows from previous
// failures cannot collide.
type reorderTestFixture struct {
	pool        *pgxpool.Pool
	providerID  int64
	credIDs     []int64
	bindingIDs  []int64
	providerIDs []int64
	rawModel    string
	canonicalID int64
}

func newReorderTestFixture(t *testing.T, pool *pgxpool.Pool, creds int) *reorderTestFixture {
	t.Helper()
	ctx := context.Background()
	uniq := time.Now().UnixNano()
	rawModel := "test-reorder-model-" + reorderItoa(uniq)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("fixture begin: %v", err)
	}
	defer tx.Rollback(ctx)

	var providerID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO providers (code, display_name, protocol, enabled) VALUES ($1, $1, 'openai-completions', true) RETURNING id`,
		"test-reorder-prov-"+reorderItoxa(uniq),
	).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	var canonicalID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO models_canonical (canonical_name, status, source) VALUES ($1, 'active', 'test') RETURNING id`,
		rawModel,
	).Scan(&canonicalID); err != nil {
		t.Fatalf("insert canonical model: %v", err)
	}
	var modelID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name, available) VALUES ($1, $2, $3, $2, true) RETURNING id`,
		providerID, rawModel, canonicalID,
	).Scan(&modelID); err != nil {
		t.Fatalf("insert provider_model: %v", err)
	}

	f := &reorderTestFixture{pool: pool, providerID: providerID, rawModel: rawModel, canonicalID: canonicalID}
	for i := 0; i < creds; i++ {
		var credID, bindingID int64
		if err := tx.QueryRow(ctx,
			`INSERT INTO credentials (provider_id, label, status, lifecycle_status) VALUES ($1, $2, 'active', 'active') RETURNING id`,
			providerID, rawModel+"-cred-"+reorderItoxa(uniq+int64(i)),
		).Scan(&credID); err != nil {
			t.Fatalf("insert credential: %v", err)
		}
		priority := i + 1
		if err := tx.QueryRow(ctx,
			`INSERT INTO credential_model_bindings (credential_id, provider_model_id, manual_priority) VALUES ($1, $2, $3) RETURNING id`,
			credID, modelID, priority,
		).Scan(&bindingID); err != nil {
			t.Fatalf("insert binding: %v", err)
		}
		f.credIDs = append(f.credIDs, credID)
		f.bindingIDs = append(f.bindingIDs, bindingID)
		f.providerIDs = append(f.providerIDs, providerID)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("fixture commit: %v", err)
	}
	t.Cleanup(func() {
		f.cleanup(context.Background())
	})
	return f
}

func (f *reorderTestFixture) cleanup(ctx context.Context) {
	if f == nil {
		return
	}
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	if len(f.bindingIDs) > 0 {
		_, _ = conn.Exec(ctx, `DELETE FROM credential_model_bindings WHERE id = ANY($1)`, f.bindingIDs)
	}
	_, _ = conn.Exec(ctx, `DELETE FROM provider_models WHERE provider_id = $1`, f.providerID)
	_, _ = conn.Exec(ctx, `DELETE FROM credentials WHERE provider_id = $1`, f.providerID)
	_, _ = conn.Exec(ctx, `DELETE FROM providers WHERE id = $1`, f.providerID)
	if f.canonicalID > 0 {
		_, _ = conn.Exec(ctx, `DELETE FROM models_canonical WHERE id = $1`, f.canonicalID)
	}
}

func (f *reorderTestFixture) priorities(t *testing.T) map[int64]int {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT credential_id, manual_priority FROM credential_model_bindings WHERE id = ANY($1) ORDER BY id`,
		f.bindingIDs,
	)
	if err != nil {
		t.Fatalf("priority query: %v", err)
	}
	defer rows.Close()
	out := make(map[int64]int, len(f.bindingIDs))
	for rows.Next() {
		var credID int64
		var prio int
		if err := rows.Scan(&credID, &prio); err != nil {
			t.Fatalf("priority scan: %v", err)
		}
		out[credID] = prio
	}
	return out
}

func (f *reorderTestFixture) reorderRevision(t *testing.T, h *Handler) string {
	t.Helper()
	rev, err := loadCanonicalScopeRevision(context.Background(), h.db, f.canonicalID)
	if err != nil {
		t.Fatalf("loadCanonicalScopeRevision: %v", err)
	}
	return rev.Raw
}

func reorderItoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 24)
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

func reorderItoxa(n int64) string {
	const hexDigits = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 16)
	for n > 0 {
		buf = append([]byte{hexDigits[n%16]}, buf...)
		n /= 16
	}
	return string(buf)
}

func mustEncode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func doReorder(t *testing.T, h *Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/routing/candidate-bindings/reorder", strings.NewReader(string(mustEncode(t, body))))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:9999"
	h.handleRoutingCandidateBindingReorder(rec, req)
	return rec
}

// TestRoutingCandidateBindingReorder_IntegrationHappy flips two binding
// priorities inside one transaction, asserts the new ordering, and
// confirms the audit row landed.
func TestRoutingCandidateBindingReorder_IntegrationHappy(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)
	rev := f.reorderRevision(t, h)

	body := routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: rev,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[1]), ManualPriority: 1},
			{CredentialID: int(f.credIDs[0]), ManualPriority: 2},
		},
	}
	rec := doReorder(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got := f.priorities(t)
	if got[f.credIDs[0]] != 2 || got[f.credIDs[1]] != 1 {
		t.Fatalf("priorities did not flip: %+v", got)
	}

	var auditCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM routing_audit_log WHERE action = 'routing_candidate_binding_reorder' AND after_json ? $1`,
		f.rawModel,
	).Scan(&auditCount); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if auditCount < 1 {
		t.Fatalf("expected at least one audit row for %s", f.rawModel)
	}
}

// TestRoutingCandidateBindingReorder_StaleRevision replays a request
// with the originally observed revision after a successful first write
// and expects HTTP 409.
func TestRoutingCandidateBindingReorder_StaleRevision(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)
	staleRev := f.reorderRevision(t, h)

	first := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: staleRev,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[1]), ManualPriority: 1},
			{CredentialID: int(f.credIDs[0]), ManualPriority: 2},
		},
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first reorder status = %d, body = %s", first.Code, first.Body.String())
	}

	second := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: staleRev,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[0]), ManualPriority: 1},
			{CredentialID: int(f.credIDs[1]), ManualPriority: 2},
		},
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("stale reorder status = %d, body = %s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "stale candidate binding set") {
		t.Fatalf("expected stale detail, got %s", second.Body.String())
	}
}

// TestRoutingCandidateBindingReorder_IncompleteSet submits one credential
// when the scope holds two and expects HTTP 409.
func TestRoutingCandidateBindingReorder_IncompleteSet(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)
	rev := f.reorderRevision(t, h)

	rec := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: rev,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[0]), ManualPriority: 1},
		},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("incomplete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "incomplete candidate set") {
		t.Fatalf("expected incomplete detail, got %s", rec.Body.String())
	}
}

// TestRoutingCandidateBindingReorder_ConcurrentConflict submits two
// opposite-order reorders against the same scope and asserts that one
// commits while the other receives HTTP 409. The SERIALIZABLE isolation
// combined with FOR UPDATE OF cmb makes the conflict deterministic: the
// loser always sees the post-commit revision and fails the equality check.
func TestRoutingCandidateBindingReorder_ConcurrentConflict(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)
	rev := f.reorderRevision(t, h)

	barrier := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-barrier
			items := []routingCandidateReorderItem{
				{CredentialID: int(f.credIDs[idx]), ManualPriority: 1},
				{CredentialID: int(f.credIDs[1-idx]), ManualPriority: 2},
			}
			rec := doReorder(t, h, routingCandidateReorderRequest{
				CanonicalID:      f.canonicalID,
				ExpectedRevision: rev,
				Items:            items,
			})
			codes[idx] = rec.Code
			bodies[idx] = rec.Body.String()
		}(i)
	}
	close(barrier)
	wg.Wait()

	successes, conflicts := 0, 0
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("goroutine %d unexpected status %d body=%s", i, code, bodies[i])
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one 200 and one 409, got successes=%d conflicts=%d (codes=%v)", successes, conflicts, codes)
	}

	priorities := f.priorities(t)
	sum := priorities[f.credIDs[0]] + priorities[f.credIDs[1]]
	if sum != 3 {
		t.Fatalf("final priorities should sum to 3, got %+v", priorities)
	}
}

// reorderDisabledProviderFixture mirrors newReorderTestFixture but adds a
// SECOND provider under the same canonical_id / raw_model_name with
// providers.enabled = FALSE, then inserts a binding under that disabled
// provider. The dashboard's resolve endpoint (admin/routing.go) hides the
// second binding's row because of `AND p.enabled IS TRUE`, but the pre-fix
// reorder scope SQL counted it — leading to a scope=15 vs submitted=11
// drift that produced a phantom 409 on every drag PATCH until the operator
// refetched.
//
// Tests below use this fixture to assert the post-fix scope SQL drops the
// disabled-provider binding BEFORE it reaches the writer, and that the
// persisted scope_hash (now also filtered by migration 574) lines up
// with the dashboard's filtered view.
type reorderDisabledProviderFixture struct {
	*reorderTestFixture
	disabledProviderID  int64
	disabledCredID      int64
	disabledBindingID   int64
}

func newReorderDisabledProviderFixture(t *testing.T, pool *pgxpool.Pool, visibleCreds int) *reorderDisabledProviderFixture {
	t.Helper()
	ctx := context.Background()

	base := newReorderTestFixture(t, pool, visibleCreds)

	uniq := time.Now().UnixNano()
	disabledProviderCode := "test-reorder-prov-disabled-" + reorderItoxa(uniq)
	disabledCredLabel := base.rawModel + "-cred-disabled-" + reorderItoxa(uniq+99)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("fixture begin: %v", err)
	}
	defer tx.Rollback(ctx)

	var disabledProviderID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO providers (code, display_name, protocol, enabled) VALUES ($1, $1, 'openai-completions', false) RETURNING id`,
		disabledProviderCode,
	).Scan(&disabledProviderID); err != nil {
		t.Fatalf("insert disabled provider: %v", err)
	}

	// Reuse the same canonical_id + raw_model_name so the binding lives
	// under the SAME scope the operator sees. We add a second
	// provider_models row pointing at the disabled provider.
	var disabledModelID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name, available)
		 VALUES ($1, $2, $3, $2, true) RETURNING id`,
		disabledProviderID, base.rawModel, base.canonicalID,
	).Scan(&disabledModelID); err != nil {
		t.Fatalf("insert disabled provider_model: %v", err)
	}

	var disabledCredID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO credentials (provider_id, label, status, lifecycle_status) VALUES ($1, $2, 'active', 'active') RETURNING id`,
		disabledProviderID, disabledCredLabel,
	).Scan(&disabledCredID); err != nil {
		t.Fatalf("insert disabled credential: %v", err)
	}

	var disabledBindingID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO credential_model_bindings (credential_id, provider_model_id, manual_priority)
		 VALUES ($1, $2, 99) RETURNING id`,
		disabledCredID, disabledModelID,
	).Scan(&disabledBindingID); err != nil {
		t.Fatalf("insert disabled binding: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("fixture commit: %v", err)
	}

	out := &reorderDisabledProviderFixture{
		reorderTestFixture: base,
		disabledProviderID: disabledProviderID,
		disabledCredID:     disabledCredID,
		disabledBindingID:  disabledBindingID,
	}
	t.Cleanup(func() {
		conn, err := pool.Acquire(context.Background())
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(context.Background(), `DELETE FROM credential_model_bindings WHERE id = $1`, disabledBindingID)
		_, _ = conn.Exec(context.Background(), `DELETE FROM credentials WHERE id = $1`, disabledCredID)
		_, _ = conn.Exec(context.Background(), `DELETE FROM provider_models WHERE id = $1`, disabledModelID)
		_, _ = conn.Exec(context.Background(), `DELETE FROM providers WHERE id = $1`, disabledProviderID)
	})
	return out
}

// TestReorderScopeSQL_ExcludesDisabledProviderBinding covers the
// partial-submission regression: the reorder scope SQL must skip
// bindings under providers.enabled = FALSE so the writer's scope count
// matches the dashboard's visible count, allowing a complete PATCH to
// commit on the first try instead of returning 409 with
// "scope=15 submitted=11".
func TestReorderScopeSQL_ExcludesDisabledProviderBinding(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderDisabledProviderFixture(t, pool, 2) // 2 visible + 1 disabled-provider

	scope, _, err := fetchReorderScopeByCanonical(context.Background(), h.db, f.canonicalID, false)
	if err != nil {
		t.Fatalf("fetchReorderScopeByCanonical: %v", err)
	}
	// Expect 2 — the visible bindings only. The disabled-provider binding
	// must NOT appear.
	if len(scope) != 2 {
		t.Fatalf("fetchReorderScopeByCanonical returned %d rows, want 2 (disabled-provider binding must be excluded)", len(scope))
	}
	for _, r := range scope {
		if r.CredentialID == f.disabledCredID {
			t.Fatalf("scope includes credential_id=%d (under disabled provider); must be excluded", r.CredentialID)
		}
	}

	// Same check on the raw_model variant — the dashboard's legacy
	// raw_model scope (migration 541 fallback path) must drop the disabled
	// binding too.
	scopeRaw, _, err := fetchReorderScope(context.Background(), h.db, f.rawModel, false)
	if err != nil {
		t.Fatalf("fetchReorderScope: %v", err)
	}
	if len(scopeRaw) != 2 {
		t.Fatalf("fetchReorderScope returned %d rows, want 2 (disabled-provider binding must be excluded)", len(scopeRaw))
	}
	for _, r := range scopeRaw {
		if r.CredentialID == f.disabledCredID {
			t.Fatalf("raw_model scope includes credential_id=%d (under disabled provider); must be excluded", r.CredentialID)
		}
	}

	// End-to-end: a PATCH that covers the 2 visible credentials must
	// commit. Before the fix this returned 409 ("scope=3 submitted=2"),
	// the phantom-drift symptom this commit closes.
	rev := f.reorderRevision(t, h)
	rec := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: rev,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[0]), ManualPriority: 2},
			{CredentialID: int(f.credIDs[1]), ManualPriority: 1},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// reorderManualDisabledFixture mirrors newReorderTestFixture but sets
// credentials.manual_disabled = TRUE on one binding's credential. The
// dashboard's resolve endpoint (admin/routing.go) hides that credential,
// but the pre-fix reorder scope SQL counted it — same drift symptom as
// the disabled-provider case.
type reorderManualDisabledFixture struct {
	*reorderTestFixture
	manualDisabledCredID int64
}

func newReorderManualDisabledFixture(t *testing.T, pool *pgxpool.Pool, visibleCreds int) *reorderManualDisabledFixture {
	t.Helper()
	base := newReorderTestFixture(t, pool, visibleCreds)

	if len(base.credIDs) == 0 {
		t.Fatal("base fixture has no credentials to flip")
	}
	// Flip the FIRST credential to manual_disabled = TRUE so the visible
	// set shrinks by exactly one. The remaining (visibleCreds - 1)
	// credentials stay enabled and must remain in scope.
	target := base.credIDs[0]
	if _, err := pool.Exec(context.Background(),
		`UPDATE credentials SET manual_disabled = TRUE WHERE id = $1`, target,
	); err != nil {
		t.Fatalf("flip manual_disabled: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`UPDATE credentials SET manual_disabled = FALSE WHERE id = $1`, target)
	})
	return &reorderManualDisabledFixture{
		reorderTestFixture:   base,
		manualDisabledCredID: target,
	}
}

// TestReorderScopeSQL_ExcludesManualDisabledCredential covers the second
// half of the partial-submission regression: a credential flagged with
// manual_disabled = TRUE must not appear in the reorder scope, so a PATCH
// covering the remaining visible credentials can commit on the first try.
func TestReorderScopeSQL_ExcludesManualDisabledCredential(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderManualDisabledFixture(t, pool, 3) // 1 disabled + 2 visible

	scope, _, err := fetchReorderScopeByCanonical(context.Background(), h.db, f.canonicalID, false)
	if err != nil {
		t.Fatalf("fetchReorderScopeByCanonical: %v", err)
	}
	// Expect 2 — the visible bindings only. The manual_disabled binding
	// must NOT appear.
	if len(scope) != 2 {
		t.Fatalf("fetchReorderScopeByCanonical returned %d rows, want 2 (manual_disabled binding must be excluded)", len(scope))
	}
	for _, r := range scope {
		if r.CredentialID == f.manualDisabledCredID {
			t.Fatalf("scope includes credential_id=%d (manual_disabled=TRUE); must be excluded", r.CredentialID)
		}
	}

	// End-to-end PATCH covering only the visible credentials must commit
	// without 409.
	rev := f.reorderRevision(t, h)
	visible := make([]routingCandidateReorderItem, 0, len(f.credIDs)-1)
	for _, id := range f.credIDs {
		if id == f.manualDisabledCredID {
			continue
		}
		visible = append(visible, routingCandidateReorderItem{
			CredentialID:   int(id),
			ManualPriority: len(visible) + 1,
		})
	}
	rec := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: rev,
		Items:            visible,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestEnsureScopeRevision_FilterConvergentHash covers the second-half of
// the fix: the ensureScopeRevision / ensureCanonicalScopeRevision seed
// queries must hash only the filter-conformant rows so the persisted
// scope_hash converges with the dashboard's filtered scope. Without this,
// the persisted hash includes disabled-provider rows, the dashboard
// computes a different hash, and every drag PATCH receives a 409.
func TestEnsureScopeRevision_FilterConvergentHash(t *testing.T) {
	pool := reorderTestPool(t)
	_ = newReorderDisabledProviderFixture(t, pool, 2)

	// ensureScopeRevision / ensureCanonicalScopeRevision should
	// recompute the hash from the filter-conformant rows only. We can't
	// observe the internal SQL directly, but we can drive a reorder
	// through the canonical endpoint using the revision seeded by
	// ensureCanonicalScopeRevision and assert the visible-binding PATCH
	// commits — a 409 here would mean the hash drifted back to include
	// the hidden row, the regression this test exists to detect.
	h := &Handler{db: pool}

	// Rebuild a fixture so we know exactly how many credentials are
	// visible to the resolve endpoint. (The previous fixture has been
	// committed to the DB but is also cleaned up at test end, so reading
	// its credential count requires a fresh build.)
	f := newReorderDisabledProviderFixture(t, pool, 2)
	rev := f.reorderRevision(t, h)
	visible := make([]routingCandidateReorderItem, 0, len(f.credIDs))
	for i, id := range f.credIDs {
		visible = append(visible, routingCandidateReorderItem{
			CredentialID:   int(id),
			ManualPriority: i + 1,
		})
	}
	rec := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: rev,
		Items:            visible,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ensureScopeRevision hash drift: reorder status = %d, body = %s",
			rec.Code, rec.Body.String())
	}
}
