package main

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestOwnerLookupReadsOnlySessionOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT owner_user\s+FROM session_dim`).
		WithArgs("gw-session", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"owner_user"}).AddRow("alice"))
	if got := makeOwnerLookup(db)(context.Background(), "gw-session", "tenant-a"); got != "alice" {
		t.Fatalf("data owner = %q, want alice", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerLookupRequiresTenantAndHonorsCancelledContext(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lookup := makeOwnerLookup(db)
	if got := lookup(context.Background(), "gw-session", ""); got != "" {
		t.Fatalf("missing tenant returned owner %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := lookup(ctx, "gw-session", "tenant-a"); got != "" {
		t.Fatalf("cancelled lookup returned owner %q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
