package admin

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsMissingRelationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("boom"),
			want: false,
		},
		{
			name: "undefined table pg error",
			err: &pgconn.PgError{
				Code:      "42P01",
				Message:   `relation "usage_ledger_with_current_month" does not exist`,
				TableName: "usage_ledger_with_current_month",
			},
			want: true,
		},
		{
			name: "non 42P01 pg error",
			err: &pgconn.PgError{
				Code:    "42703",
				Message: `column "foo" does not exist`,
			},
			want: false,
		},
		{
			name: "wrapped undefined table",
			err: errors.Join(
				errors.New("outer wrapper"),
				&pgconn.PgError{
					Code:      "42P01",
					Message:   `relation "x" does not exist`,
					TableName: "x",
				},
			),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsMissingRelationError(tc.err)
			if got != tc.want {
				t.Fatalf("IsMissingRelationError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestReportMissingRelationReturnsViewName(t *testing.T) {
	view := ReportMissingRelation(nil, "test", &pgconn.PgError{
		Code:      "42P01",
		TableName: "usage_ledger_with_current_month",
	})
	if view != "usage_ledger_with_current_month" {
		t.Fatalf("expected view name, got %q", view)
	}
	if view := ReportMissingRelation(nil, "test", errors.New("other")); view != "" {
		t.Fatalf("expected empty view, got %q", view)
	}
	// PostgreSQL does not populate TableName for 42P01, so the helper
	// must fall back to parsing the message body.
	view = ReportMissingRelation(nil, "test", &pgconn.PgError{
		Code:    "42P01",
		Message: `relation "usage_ledger_with_current_month" does not exist`,
	})
	if view != "usage_ledger_with_current_month" {
		t.Fatalf("expected fallback to message, got %q", view)
	}
}
