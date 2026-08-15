package durable

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func TestStore_ActiveTaskCounts(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(`WHERE status NOT IN`).
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "count"}).
			AddRow("tenant-a", 3).
			AddRow("tenant-b", 1))

	counts, err := store.ActiveTaskCounts(context.Background())
	if err != nil {
		t.Fatalf("ActiveTaskCounts: %v", err)
	}
	if counts["tenant-a"] != 3 || counts["tenant-b"] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %v", counts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
