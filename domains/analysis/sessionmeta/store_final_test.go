package sessionmeta

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestMetadataStore_UpsertFinal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	store := NewMetadataStore(mock)
	result := Extract(Input{
		Messages: []Message{{Role: "user", Content: "close stage"}},
	})
	started := time.Now().Add(-time.Minute)

	mock.ExpectQuery(`SELECT input_hash, updated_at`).
		WithArgs("tenant-a", "gw_sess_1", StatusProvisional).
		WillReturnError(pgx.ErrNoRows)

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("tenant-a", "gw_sess_1", SchemaVersion, StatusFinal, result.InputHash, pgxmock.AnyArg(), "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := store.UpsertFinal(context.Background(), "tenant-a", "gw_sess_1", "", result, started); err != nil {
		t.Fatalf("UpsertFinal: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMetadataStore_UpsertFinal_SkipsStaleWorker(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	store := NewMetadataStore(mock)
	result := Extract(Input{Messages: []Message{{Role: "user", Content: "stale corpus"}}})
	started := time.Now().Add(-5 * time.Minute)
	provUpdated := time.Now()

	mock.ExpectQuery(`SELECT input_hash, updated_at`).
		WithArgs("tenant-a", "gw_sess_1", StatusProvisional).
		WillReturnRows(pgxmock.NewRows([]string{"input_hash", "updated_at"}).
			AddRow("different-hash", provUpdated))

	if err := store.UpsertFinal(context.Background(), "tenant-a", "gw_sess_1", "", result, started); err != nil {
		t.Fatalf("UpsertFinal stale: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMetadataStore_UpsertFinal_SkipsUnchangedHash(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	store := NewMetadataStore(mock)
	result := Extract(Input{Messages: []Message{{Role: "user", Content: "same"}}})
	started := time.Now().Add(-time.Minute)

	mock.ExpectQuery(`SELECT input_hash, updated_at`).
		WithArgs("tenant-a", "gw_sess_1", StatusProvisional).
		WillReturnRows(pgxmock.NewRows([]string{"input_hash", "updated_at"}).
			AddRow(result.InputHash, started.Add(-time.Second)))

	mock.ExpectExec(`INSERT INTO public\.session_analysis_metadata`).
		WithArgs("tenant-a", "gw_sess_1", SchemaVersion, StatusFinal, result.InputHash, pgxmock.AnyArg(), "").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	if err := store.UpsertFinal(context.Background(), "tenant-a", "gw_sess_1", "", result, started); err != nil {
		t.Fatalf("UpsertFinal unchanged: %v", err)
	}
}
