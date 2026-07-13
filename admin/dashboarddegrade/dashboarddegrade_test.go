package dashboarddegrade

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsMissingRelationError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("boom"), false},
		{"pg 42P01", &pgconn.PgError{Code: "42P01", Message: `relation "session_module_executions_hot" does not exist`}, true},
		{"pg not 42P01", &pgconn.PgError{Code: "42703"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsMissingRelationError(c.err); got != c.want {
				t.Fatalf("IsMissingRelationError = %v, want %v", got, c.want)
			}
		})
	}
}

func TestExtractRelationName(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("boom"), ""},
		{
			"pg 42P01 with TableName",
			&pgconn.PgError{Code: "42P01", TableName: "session_module_executions_hot"},
			"session_module_executions_hot",
		},
		{
			"pg 42P01 from message body",
			&pgconn.PgError{Code: "42P01", Message: `relation "session_module_executions_hot" does not exist`},
			"session_module_executions_hot",
		},
		{
			"non-42P01 not extracted",
			&pgconn.PgError{Code: "42703", Message: `relation "x" does not exist`},
			"",
		},
		{
			"wrapped pg error",
			fmt.Errorf("layer: %w",
				&pgconn.PgError{Code: "42P01", Message: `relation "session_module_executions_hot" does not exist`},
			),
			"session_module_executions_hot",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractRelationName(c.err); got != c.want {
				t.Fatalf("ExtractRelationName = %q, want %q", got, c.want)
			}
		})
	}
}
