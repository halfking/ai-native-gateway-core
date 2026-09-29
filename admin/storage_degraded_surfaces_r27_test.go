package admin

import (
	"context"
	"os"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// R27-T3 (round 27, test debt C24-2 from 96fd8cde2): the no-topic,
// task/project-costs and summarize-title read surfaces gained the
// storage-degraded 503 contract on 2026-09-29 but shipped with zero
// coverage — storage_degraded_test.go only pinned detail/list/turns.
// These tests drive each surface against a pool whose queries fail at the
// connection layer (dead port) so IsStorageUnavailable classifies and the
// handler must answer 503 + storage_status instead of a plain 500.
func newDeadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(),
		"host=127.0.0.1 port=1 user=audit dbname=audit sslmode=disable connect_timeout=1 pool_max_conns=1")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestNoTopicSessionMessages_DegradedContract(t *testing.T) {
	h := &Handler{db: newDeadPool(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/system/no-topic-session/messages?prefix=sk-audit&hours=2", nil)
	h.handleNoTopicSessionRoutes(rec, req)
	assertDegraded(t, rec, "list")
}

func TestTaskFlow_DegradedContract(t *testing.T) {
	h := &Handler{db: newDeadPool(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/task-flow/task-audit", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "t1", Role: "admin", IsJWT: true})
	start := time.Now()
	h.handleTaskFlow(rec, req)
	// The dead-port connect budget must not turn this into a slow 500.
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("degraded path took %s; connect_timeout must bound it", elapsed)
	}
	assertDegraded(t, rec, "list")
}

func TestProjectCosts_DegradedContract(t *testing.T) {
	h := &Handler{db: newDeadPool(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/project-costs/proj-audit", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "t1", Role: "admin", IsJWT: true})
	h.handleProjectCosts(rec, req)
	assertDegraded(t, rec, "list")
}

func TestSessionSummarizeTitle_DegradedContract(t *testing.T) {
	h := &Handler{db: newDeadPool(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/task-audit/summarize-title", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "t1", Role: "admin", IsJWT: true})
	h.handleSessionSummarizeTitle(rec, req, "task-audit")
	assertDegraded(t, rec, "summary")
}

// R32-P-1 (round 32): a nonexistent task/project id must 404, not 500 —
// MIN/MAX over an empty set returns NULL and the old non-nullable
// time.Time scan errored before the SessionCount==0 branch could fire.
// Runs against TEST_DATABASE_URL when set (real-DB gate, sentinel id).
func TestTaskFlowUnknownIDReturns404RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-DB 404 regression")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := &Handler{db: pool}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/task-flow/r32-no-such-task", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "t1", Role: "admin", IsJWT: true})
	h.handleTaskFlow(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown task = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/project-costs/r32-no-such-project", nil)
	req = SetAuthContext(req, &AuthContext{TenantID: "t1", Role: "admin", IsJWT: true})
	h.handleProjectCosts(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}
