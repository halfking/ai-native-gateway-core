package admin

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestAssertSessionOwnerAccessInTx_AdminTiersSkipCheck(t *testing.T) {
	for _, role := range []string{"super_admin", "admin_key", "tenant_admin"} {
		t.Run(role, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			r := httptest.NewRequest("GET", "/", nil)
			r = SetAuthContext(r, &AuthContext{Role: role, TenantID: "acme"})
			ok, err := assertSessionOwnerAccessInTx(context.Background(), mock, r, "sess-1")
			if err != nil || !ok {
				t.Fatalf("admin tier should bypass check: ok=%v err=%v", ok, err)
			}
			if mock.ExpectationsWereMet() != nil {
				t.Fatalf("admin tier should not issue SQL: %v", mock.ExpectationsWereMet())
			}
		})
	}
}

func TestAssertSessionOwnerAccessInTx_RegularBindsUsername(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "alice", IsJWT: true})

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("sess-1", "acme", "alice").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	ok, err := assertSessionOwnerAccessInTx(context.Background(), mock, r, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("regular owner match should succeed")
	}
	if mock.ExpectationsWereMet() != nil {
		t.Fatalf("expectations not met: %v", mock.ExpectationsWereMet())
	}
}

func TestAssertSessionOwnerAccessInTx_EmptyUsernameDenied(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	r := httptest.NewRequest("GET", "/", nil)
	r = SetAuthContext(r, &AuthContext{Role: "user", TenantID: "acme", Username: "", IsJWT: true})
	ok, err := assertSessionOwnerAccessInTx(context.Background(), mock, r, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("empty username should be denied without SQL")
	}
	if mock.ExpectationsWereMet() != nil {
		t.Fatalf("empty username should not query: %v", mock.ExpectationsWereMet())
	}
}

// TestBuildSessionAnalysisInTx_PropagatesError asserts that compliance query
// failures surface rather than being silently swallowed.
func TestBuildSessionAnalysisInTx_PropagatesError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT request_id, detected_at").
		WillReturnError(pgx.ErrNoRows)
	h := &Handler{}
	if _, err := h.buildSessionAnalysisInTx(context.Background(), mock, "acme", "sess-1", nil); err == nil {
		t.Fatal("expected error from compliance query")
	}
}
