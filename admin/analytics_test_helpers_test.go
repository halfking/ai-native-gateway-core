package admin

// Test helpers for session analytics handlers. Compiled only under `go test`.
// These were referenced by session_analytics_breakdown_test.go (Task T1.2) and
// other analytics tests but had not been defined, which broke `go test ./admin`.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setTestTenantContext attaches a super_admin AuthContext scoped to the given
// tenant, mirroring how production middleware populates the request context.
// Tests use this so handlers calling EffectiveTenantID/EffectiveTenantIDAll
// resolve the intended tenant without a real DB / RLS round-trip.
func setTestTenantContext(r *http.Request, tenantID string) *http.Request {
	return SetAuthContext(r, &AuthContext{
		TenantID: tenantID,
		Username: "test-admin",
		Role:     "super_admin",
	})
}

// parseJSONResponse decodes the JSON body of a httptest.ResponseRecorder into v.
// Returns an error if the status code is not 2xx or decoding fails.
func parseJSONResponse(w *httptest.ResponseRecorder, v interface{}) error {
	if w.Code < 200 || w.Code >= 300 {
		return &unexpectedStatusError{code: w.Code, body: w.Body.String()}
	}
	return json.Unmarshal(w.Body.Bytes(), v)
}

type unexpectedStatusError struct {
	code int
	body string
}

func (e *unexpectedStatusError) Error() string {
	return "unexpected status " + http.StatusText(e.code) + " (" + e.body + ")"
}

// setupTestDB opens a connection pool to the test database declared by the
// TEST_DATABASE_URL env var. It follows the same convention used elsewhere in
// this package (see credential_monitor_decisions_test.go,
// data_lifecycle_storage_test.go): when the var is unset, or when running with
// -short, the test is skipped rather than failing. This keeps `go test ./admin`
// green in CI environments without a database while still running the
// integration tests when a DB is available.
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	return pool
}

