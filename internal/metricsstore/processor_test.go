package metricsstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestConfigFromEnv(t *testing.T) {
	base := DefaultConfig()
	t.Setenv("LLM_GATEWAY_METRICS_INTERVAL", "45s")
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "250")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "3")
	if cfg := ConfigFromEnv(base); cfg != (Config{Interval: 45 * time.Second, BatchSize: 250, MaxRoundsPerTick: 3}) {
		t.Fatalf("unexpected runtime configuration: %+v", cfg)
	}
	t.Setenv("LLM_GATEWAY_METRICS_INTERVAL", "-1s")
	t.Setenv("LLM_GATEWAY_METRICS_BATCH_SIZE", "invalid")
	t.Setenv("LLM_GATEWAY_METRICS_MAX_ROUNDS", "0")
	if cfg := ConfigFromEnv(base); cfg != base {
		t.Fatalf("invalid overrides changed defaults: %+v", cfg)
	}
}

func TestProcessRoundRespectsBatchLimitAndCancellation(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()
	cfg := Config{BatchSize: 100, MaxRoundsPerTick: 2}
	for range cfg.MaxRoundsPerTick {
		mock.ExpectBegin()
		mock.ExpectQuery(lockProjectionSQL).WillReturnRows(sqlmock.NewRows([]string{"singleton"}).AddRow(true))
		mock.ExpectQuery(projectBatchSQL).WithArgs(cfg.BatchSize).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(cfg.BatchSize))
		mock.ExpectCommit()
	}
	processor := NewProcessor(db)
	if !processor.processRound(context.Background(), cfg) {
		t.Fatal("full batches should keep the catch-up schedule")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if processor.processRound(ctx, cfg) {
		t.Fatal("cancelled worker should stop")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessBatchCommitsProjectionAndCheckpointTogether(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()
	mock.ExpectBegin()
	mock.ExpectQuery(lockProjectionSQL).WillReturnRows(sqlmock.NewRows([]string{"singleton"}).AddRow(true))
	mock.ExpectQuery(projectBatchSQL).WithArgs(100).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectCommit()
	got, err := NewProcessor(db).ProcessBatch(context.Background(), 100)
	if err != nil || got != 2 {
		t.Fatalf("processed %d, error %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessBatchRollsBackOnPublishError(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()
	failedWrite := errors.New("projection write failed")
	mock.ExpectBegin()
	mock.ExpectQuery(lockProjectionSQL).WillReturnRows(sqlmock.NewRows([]string{"singleton"}).AddRow(true))
	mock.ExpectQuery(projectBatchSQL).WithArgs(100).WillReturnError(failedWrite)
	mock.ExpectRollback()
	got, err := NewProcessor(db).ProcessBatch(context.Background(), 100)
	if !errors.Is(err, failedWrite) || got != 0 {
		t.Fatalf("processed %d, error %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessBatchSkipsIfAnotherGatewayOwnsCheckpoint(t *testing.T) {
	db, mock, closeDB := newMockDB(t)
	defer closeDB()
	mock.ExpectBegin()
	mock.ExpectQuery(lockProjectionSQL).WillReturnRows(sqlmock.NewRows([]string{"singleton"}))
	mock.ExpectRollback()
	got, err := NewProcessor(db).ProcessBatch(context.Background(), 100)
	if err != nil || got != 0 {
		t.Fatalf("processed %d, error %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
