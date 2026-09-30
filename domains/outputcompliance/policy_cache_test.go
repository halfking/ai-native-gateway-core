package outputcompliance

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRequestPolicyCacheUsesOneLookupPerCheckerAndTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 3 {
		mock.ExpectQuery("SELECT tenant_id, enabled, enforcement_mode").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	}
	checker := &Checker{db: db}
	shared := WithRequestPolicyCache(context.Background())
	for _, tc := range []struct {
		ctx    context.Context
		tenant string
	}{
		{shared, "tenant-a"},
		{shared, "tenant-a"}, // same request: cache hit
		{shared, "tenant-b"}, // tenant isolation
		{WithRequestPolicyCache(context.Background()), "tenant-a"}, // next request: fresh policy
	} {
		result, err := checker.Check(tc.ctx, tc.tenant, "ordinary output")
		if err != nil || result == nil {
			t.Fatalf("Check(%s): result=%+v err=%v", tc.tenant, result, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
