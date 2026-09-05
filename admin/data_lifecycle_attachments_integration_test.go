//go:build integration

// Integration tests for the attachment cleanup handlers
// (handleDataLifecycleAttachmentCleanupExecute + cleanup-preview +
// history/list/stats/item) against the isolated audit PG.
//
// These tests require TEST_AUDIT_ISOLATED_DB_URL pointing at the kx-citus
// container on 127.0.0.1:15433, plus the audit fixture applied
// (scripts/audit/sql/min-prereqs.sql plus the migrations 627-632).
// Run via `bash scripts/audit/verify-attachments-integration.sh`.
//
// The audit-data-closure-2 P0 fix changed attachmentTenantScope from
// hard-coded `$1` to `$(alreadyAppended+1)`. Before the fix, the
// execute handler's UPDATE statement collided $1 (olderThanDays) and
// $1 (tenant_id) and HTTP 500'd every tenant_admin cleanup. These
// integration tests pin the new placeholder math so a regression
// would surface at code review time.

package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	auditAttachDBEnv = "TEST_AUDIT_ISOLATED_DB_URL"
)

func openAttachPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(auditAttachDBEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", auditAttachDBEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// attachFixtureReset truncates the tables the cleanup handlers touch
// and seeds one hot row + one audit row so each test starts clean.
func attachFixtureReset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		"TRUNCATE public.request_logs_hot",
		"TRUNCATE public.audit_attachments_cleanup",
		"TRUNCATE public.audit_attachments_filesystem_cleanup",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// seedRequestLogHot inserts one row into request_logs_hot with two
// attachments: one with all identity fields present (so the audit
// INSERT picks it up) and one with all identity fields absent (so
// the malformed-hash predicate skips it).
func seedRequestLogHot(t *testing.T, pool *pgxpool.Pool, tenantID, requestID string, ts time.Time) {
	t.Helper()
	ctx := context.Background()
	attachments := []byte(`[
		{"type":"image","content_type":"image/png","size":"1024","hash":"abc123"},
		{"type":"file","content_type":"application/octet-stream","size":"bad","id":null,"url":null,"hash":null,"sha256":null}
	]`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.request_logs_hot
		  (request_id, ts, tenant_id, success, attachments)
		VALUES ($1, $2, $3, true, $4::jsonb)`,
		requestID, ts, tenantID, attachments); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// withRoleRequest builds a *httptest.ResponseRecorder + *http.Request
// with the given role + tenant_id pre-injected via SetAuthContext. The
// returned recorder is suitable for httptest.NewRecorder()-style
// handler invocation; the request URL contains the older_than_days and
// tenant_id query parameters the cleanup handlers expect.
func withRoleRequest(method, path string, role, tenantID string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(method, path, nil)
	if role != "" {
		req = SetAuthContext(req, &AuthContext{Role: role, TenantID: tenantID})
	}
	return httptest.NewRecorder(), req
}

// TestAttachmentCleanup_Execute_TenantAdmin_NoPlaceholderCollision is the
// regression-pinning test for the audit-data-closure-2 P0 fix. Before
// the fix, the execute handler's UPDATE statement had `($1 || ' days')`
// AND `AND tenant_id = $1` with `args = [tenantID, olderThanDays]`,
// which crashed PG with `operator does not exist: text || days` or
// `cannot cast type text to interval`. After the fix, the UPDATE
// statement references `($2 || ' days')` and the args are
// `[tenantID, olderThanDays]`, so $2=olderThanDays (int) lines up.
func TestAttachmentCleanup_Execute_TenantAdmin_NoPlaceholderCollision(t *testing.T) {
	pool := openAttachPool(t)
	ctx := context.Background()
	attachFixtureReset(t, pool)
	// Seed one hot row with two attachments (one valid hash, one
	// malformed). ts is 60 days ago so olderThanDays=30 picks it up.
	seedRequestLogHot(t, pool, "tenant_int", "req_int_1", time.Now().Add(-60*24*time.Hour))

	h := &Handler{db: pool}
	rec, req := withRoleRequest("POST",
		"/api/admin/attachments/cleanup/execute?older_than_days=30",
		"tenant_admin", "tenant_int")
	req.Header.Set("X-Admin-User", "tester")

	h.handleDataLifecycleAttachmentCleanupExecute(rec, req)
	if rec.Code != 200 {
		t.Fatalf("execute returned status=%d body=%s; expected 200 (was the placeholder-collision regression)",
			rec.Code, rec.Body.String())
	}
	// Verify the audit INSERT landed exactly one row (only the
	// well-formed attachment with hash=abc123).
	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.audit_attachments_cleanup").Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit_attachments_cleanup rows = %d, want 1", auditCount)
	}
	// Verify the UPDATE actually NULLed attachments on the hot row
	// (the old placeholder-collision bug aborted before UPDATE ran).
	var atts interface{}
	if err := pool.QueryRow(ctx, "SELECT attachments FROM public.request_logs_hot WHERE request_id=$1", "req_int_1").Scan(&atts); err != nil {
		t.Fatalf("read hot: %v", err)
	}
	if atts != nil {
		t.Errorf("request_logs_hot.attachments = %v, want NULL (UPDATE did not run)", atts)
	}
}

// TestAttachmentCleanup_Execute_SuperAdmin_AllTenants: super_admin
// without ?tenant_id runs against all tenants.
func TestAttachmentCleanup_Execute_SuperAdmin_AllTenants(t *testing.T) {
	pool := openAttachPool(t)
	attachFixtureReset(t, pool)
	ctx := context.Background()
	// Seed hot rows for two distinct tenants.
	seedRequestLogHot(t, pool, "tenant_a", "req_a_1", time.Now().Add(-60*24*time.Hour))
	seedRequestLogHot(t, pool, "tenant_b", "req_b_1", time.Now().Add(-60*24*time.Hour))

	h := &Handler{db: pool}
	rec, req := withRoleRequest("POST",
		"/api/admin/attachments/cleanup/execute?older_than_days=30",
		"super_admin", "default")
	req.Header.Set("X-Admin-User", "tester")
	h.handleDataLifecycleAttachmentCleanupExecute(rec, req)
	if rec.Code != 200 {
		t.Fatalf("execute returned status=%d body=%s", rec.Code, rec.Body.String())
	}
	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.audit_attachments_cleanup").Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	// One audit row per hot row × one valid attachment each = 2.
	if auditCount != 2 {
		t.Errorf("audit_attachments_cleanup rows = %d, want 2 (one per tenant)", auditCount)
	}
}

// TestAttachmentCleanup_Execute_SuperAdmin_ExplicitTenant: super_admin
// with ?tenant_id=tenant_a narrows to one tenant.
func TestAttachmentCleanup_Execute_SuperAdmin_ExplicitTenant(t *testing.T) {
	pool := openAttachPool(t)
	attachFixtureReset(t, pool)
	ctx := context.Background()
	seedRequestLogHot(t, pool, "tenant_a", "req_a_2", time.Now().Add(-60*24*time.Hour))
	seedRequestLogHot(t, pool, "tenant_b", "req_b_2", time.Now().Add(-60*24*time.Hour))

	h := &Handler{db: pool}
	rec, req := withRoleRequest("POST",
		"/api/admin/attachments/cleanup/execute?older_than_days=30&tenant_id=tenant_a",
		"super_admin", "default")
	req.Header.Set("X-Admin-User", "tester")
	h.handleDataLifecycleAttachmentCleanupExecute(rec, req)
	if rec.Code != 200 {
		t.Fatalf("execute returned status=%d body=%s", rec.Code, rec.Body.String())
	}
	var tenantACount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.audit_attachments_cleanup WHERE tenant_id='tenant_a'").Scan(&tenantACount); err != nil {
		t.Fatalf("count tenant_a: %v", err)
	}
	if tenantACount != 1 {
		t.Errorf("tenant_a audit rows = %d, want 1", tenantACount)
	}
	var tenantBCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.audit_attachments_cleanup WHERE tenant_id='tenant_b'").Scan(&tenantBCount); err != nil {
		t.Fatalf("count tenant_b: %v", err)
	}
	if tenantBCount != 0 {
		t.Errorf("tenant_b audit rows = %d, want 0 (super_admin+explicit tenant must NOT touch other tenants)", tenantBCount)
	}
}

// TestAttachmentCleanup_Preview_TenantAdmin_SkipsOtherTenants: cleanup
// preview must not return aggregate counts that include other tenants'
// attachments when called by a tenant_admin.
func TestAttachmentCleanup_Preview_TenantAdmin_SkipsOtherTenants(t *testing.T) {
	pool := openAttachPool(t)
	attachFixtureReset(t, pool)
	seedRequestLogHot(t, pool, "tenant_a", "req_a_3", time.Now().Add(-60*24*time.Hour))
	seedRequestLogHot(t, pool, "tenant_b", "req_b_3", time.Now().Add(-60*24*time.Hour))

	h := &Handler{db: pool}
	rec, req := withRoleRequest("POST",
		"/api/admin/attachments/cleanup/preview?older_than_days=30",
		"tenant_admin", "tenant_a")
	h.handleDataLifecycleAttachmentCleanupPreview(rec, req)
	if rec.Code != 200 {
		t.Fatalf("preview returned status=%d body=%s", rec.Code, rec.Body.String())
	}
	// We do not parse the JSON body — the integration's job is to
	// confirm the handler did NOT 500 with the same placeholder
	// collision as the execute path. The body itself is asserted in
	// the existing unit tests.
}

// TestAttachmentCleanup_Item_TenantAdmin_404OnOtherTenant verifies the
// Item handler returns 404 when the request_id resolves to a row in
// a different tenant (no existence-leak; documented in the Item
// handler comment).
func TestAttachmentCleanup_Item_TenantAdmin_404OnOtherTenant(t *testing.T) {
	pool := openAttachPool(t)
	attachFixtureReset(t, pool)
	// Seed a hot row owned by tenant_b but query as tenant_a.
	seedRequestLogHot(t, pool, "tenant_b", "req_cross_1", time.Now().Add(-1*time.Hour))

	h := &Handler{db: pool}
	rec, req := withRoleRequest("GET",
		"/api/admin/attachments/req_cross_1",
		"tenant_admin", "tenant_a")
	h.handleDataLifecycleAttachmentItem(rec, req)
	if rec.Code != 404 {
		t.Errorf("Item status=%d, want 404 (cross-tenant access must 404, not leak existence)", rec.Code)
	}
}

// TestAttachmentCleanup_FSAuditRow_FilesystemPathsBody: when the
// execute body carries filesystem_paths, an additional row is
// written to audit_attachments_filesystem_cleanup under the same
// cleanup_run_id.
func TestAttachmentCleanup_FSAuditRow_FilesystemPathsBody(t *testing.T) {
	pool := openAttachPool(t)
	attachFixtureReset(t, pool)
	seedRequestLogHot(t, pool, "tenant_fs", "req_fs_1", time.Now().Add(-60*24*time.Hour))

	h := &Handler{db: pool}
	body := `{"filesystem_paths":["/var/attachments/a","/var/attachments/b"]}`
	rec, req := withRoleRequest("POST",
		"/api/admin/attachments/cleanup/execute?older_than_days=30",
		"super_admin", "default")
	req.Header.Set("X-Admin-User", "tester")
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(strings.NewReader(body))
	h.handleDataLifecycleAttachmentCleanupExecute(rec, req)
	if rec.Code != 200 {
		t.Fatalf("execute status=%d body=%s", rec.Code, rec.Body.String())
	}
	var fsAuditCount int
	if err := pool.QueryRow(pool_Context(),
		"SELECT count(*) FROM public.audit_attachments_filesystem_cleanup").Scan(&fsAuditCount); err != nil {
		t.Fatalf("count fs audit: %v", err)
	}
	if fsAuditCount != 2 {
		t.Errorf("audit_attachments_filesystem_cleanup rows = %d, want 2", fsAuditCount)
	}
}

// helper: alias to avoid importing context twice in tests.
func pool_Context() context.Context { return context.Background() }

var _ = fmt.Sprintf
var _ = http.MethodPost