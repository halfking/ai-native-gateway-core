package reportrollup

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// missing_dates_test.go —— MissingRollupDates（追赶探测）的 pgxmock 回归：
// SQL 形状（generate_series EXCEPT daily_total）与参数绑定。真库语义由
// realdb E2E 兜底。
func TestMissingRollupDates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := day(t, "2026-09-25")
	rows := pgxmock.NewRows([]string{"d"}).
		AddRow(day(t, "2026-09-20")).
		AddRow(day(t, "2026-09-23"))
	mock.ExpectQuery(`generate_series`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(rows)

	missing, err := MissingRollupDates(context.Background(), mock, 7, now)
	if err != nil {
		t.Fatalf("MissingRollupDates: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if len(missing) != 2 || missing[0].Format("2006-01-02") != "2026-09-20" ||
		missing[1].Format("2006-01-02") != "2026-09-23" {
		t.Errorf("missing = %v, want 09-20 + 09-23", missing)
	}
}

func TestMissingRollupDates_ZeroLookback(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`generate_series`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"d"}))

	// lookback<=0 钳位为 1（至少检查昨天），不会把整张表当追赶范围。
	missing, err := MissingRollupDates(context.Background(), mock, 0, day(t, "2026-09-25"))
	if err != nil {
		t.Fatalf("MissingRollupDates: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}
