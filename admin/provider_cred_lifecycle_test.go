package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func TestIsCredentialLifecycleStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  bool
	}{
		{value: "active", want: true},
		{value: "disabled", want: true},
		{value: "suspended", want: true},
		{value: "retired", want: true},
		{value: "deprecated", want: false},
		{value: "test", want: false},
		{value: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			if got := isCredentialLifecycleStatus(tt.value); got != tt.want {
				t.Fatalf("isCredentialLifecycleStatus(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestSetCredentialLifecycleStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantUpdate bool
		wantErr    bool
	}{
		{name: "updated", wantUpdate: true},
		{name: "not found"},
		{name: "database error", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()

			expectation := mock.ExpectExec("UPDATE credentials SET lifecycle_status").
				WithArgs("disabled", 11, 7)
			switch tt.name {
			case "updated":
				expectation.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			case "not found":
				expectation.WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			case "database error":
				expectation.WillReturnError(errors.New("constraint failed"))
			}

			updated, err := setCredentialLifecycleStatus(context.Background(), mock, 7, 11, "disabled")
			if (err != nil) != tt.wantErr {
				t.Fatalf("setCredentialLifecycleStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
			if updated != tt.wantUpdate {
				t.Fatalf("setCredentialLifecycleStatus() updated = %v, want %v", updated, tt.wantUpdate)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
