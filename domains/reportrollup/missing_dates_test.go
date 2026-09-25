package reportrollup

import (
	"context"
	"strings"
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

// TestInternalPersonScopeKeyRoundTrip —— 长度前缀编码往返（含 person 含
// 冒号/分隔符歧义场景）与历史裸键回退（2026-09-25 审计轮：R65 的 NUL
// 分隔符是 PG 非法编码，真库 INSERT 全量 22021，换长度前缀）。
func TestInternalPersonScopeKeyRoundTrip(t *testing.T) {
	cases := []struct{ tenant, person string }{
		{"tenantA", "alice"},
		{"default", "unknown"},
		{"t:1", "per:son"}, // person 含冒号
		{"", "orphan"},     // 空租户
		{"tenant", ""},     // 空人员
	}
	for _, c := range cases {
		key := internalPersonScopeKey(c.tenant, c.person)
		if strings.ContainsRune(key, 0) {
			t.Errorf("key %q contains NUL — PG TEXT rejects it", key)
			continue
		}
		tBack, pBack := splitInternalPersonScopeKey(key)
		if tBack != c.tenant || pBack != c.person {
			t.Errorf("roundtrip (%q,%q) → %q → (%q,%q)", c.tenant, c.person, key, tBack, pBack)
		}
	}
	// 历史裸键（编码格式之前）原样回落。
	if ten, per := splitInternalPersonScopeKey("alice"); ten != "" || per != "alice" {
		t.Errorf("legacy bare key roundtrip = (%q,%q), want empty-tenant/alice", ten, per)
	}
}
