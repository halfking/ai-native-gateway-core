package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestVendorCredentialErrorDetailTenantAdminScopesCredentialLookup(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectQuery("SELECT id, label, provider_id, health_status").WithArgs(int64(42), "tenant-a").WillReturnError(errNoRowsForTenantTest)
	req := httptest.NewRequest(http.MethodGet, "/api/vendors/credentials/42/error-detail", nil)
	req.SetPathValue("id", "42")
	req = SetAuthContext(req, &AuthContext{TenantID: "tenant-a", Role: "tenant_admin", UserID: 9})
	rec := httptest.NewRecorder()
	(&vendorCredentialErrorHandlers{db: mock}).getVendorCredentialErrorDetail(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

var errNoRowsForTenantTest = pgx.ErrNoRows
