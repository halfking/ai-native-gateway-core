package sessionmeta

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func TestMetadataStore_UpsertProvisional(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	store := NewMetadataStore(mock)
	result := Extract(Input{
		Messages: []Message{{Role: "user", Content: "修复登录失败"}},
	})

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("tenant-a", "gw_sess_1", SchemaVersion, StatusProvisional, result.InputHash, pgxmock.AnyArg(), "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := store.UpsertProvisional(context.Background(), "tenant-a", "gw_sess_1", "", result); err != nil {
		t.Fatalf("UpsertProvisional: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMetadataStore_UpsertProvisional_SkipsUnchangedHash(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	store := NewMetadataStore(mock)
	result := Extract(Input{Messages: []Message{{Role: "user", Content: "hello"}}})

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("tenant-a", "gw_sess_1", SchemaVersion, StatusProvisional, result.InputHash, pgxmock.AnyArg(), "").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	if err := store.UpsertProvisional(context.Background(), "tenant-a", "gw_sess_1", "", result); err != nil {
		t.Fatalf("UpsertProvisional unchanged: %v", err)
	}
}

func TestMetadataStore_UpsertProvisional_NotConfigured(t *testing.T) {
	store := NewMetadataStore(nil)
	err := store.UpsertProvisional(context.Background(), "t", "s", "", Result{InputHash: "abc"})
	if err != ErrStoreNotConfigured {
		t.Fatalf("err = %v, want ErrStoreNotConfigured", err)
	}
}
