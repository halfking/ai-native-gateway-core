package modelquality

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dbstorage_integration_test.go — Postgres-backed integration tests for
// DBStorage. These exercise the full SQL write/read path against model_iq_runs
// + node_iq_latest and validate the round-trip behaviour that the nil-pool
// unit tests cannot cover.
//
// Skip by default: set TEST_DATABASE_URL to a Postgres instance that already
// has the migration-350 tables (model_iq_runs, node_iq_latest) and the
// upstream FK chain (providers, credentials, provider_models,
// credential_model_bindings).

func setupTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DBStorage integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("pgxpool.New failed (DB unavailable): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("test database unreachable: %v", err)
	}
	return pool
}

// setupTestFixture inserts a minimal provider → credential → provider_model →
// credential_model_binding chain so resolveNode can succeed, and returns a
// cleanup function that tears everything down in reverse FK order.
func setupTestFixture(t *testing.T, pool *pgxpool.Pool) (credID int, providerID int, rawModel string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	rawModel = "test-iq-model-" + fmt.Sprintf("%d", time.Now().UnixNano())

	// provider
	var pid int
	err := pool.QueryRow(ctx, `
		INSERT INTO providers (code, display_name, enabled, created_at)
		VALUES ('test-iq-provider', 'Test IQ Provider', true, now())
		RETURNING id`).Scan(&pid)
	if err != nil {
		t.Fatalf("insert test provider: %v", err)
	}

	// credential
	var cid int
	err = pool.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, secret_ciphertext, status, created_at)
		VALUES ($1, '\x00', 'active', now())
		RETURNING id`, pid).Scan(&cid)
	if err != nil {
		t.Fatalf("insert test credential: %v", err)
	}

	// provider_model
	var pmid int
	err = pool.QueryRow(ctx, `
		INSERT INTO provider_models (provider_id, raw_model_name, available, created_at)
		VALUES ($1, $2, true, now())
		RETURNING id`, pid, rawModel).Scan(&pmid)
	if err != nil {
		t.Fatalf("insert test provider_model: %v", err)
	}

	// credential_model_binding
	_, err = pool.Exec(ctx, `
		INSERT INTO credential_model_bindings (credential_id, provider_model_id, available, created_at)
		VALUES ($1, $2, true, now())`, cid, pmid)
	if err != nil {
		t.Fatalf("insert test binding: %v", err)
	}

	cleanup = func() {
		_, _ = pool.Exec(ctx, `DELETE FROM model_iq_runs WHERE credential_id=$1`, cid)
		_, _ = pool.Exec(ctx, `DELETE FROM node_iq_latest WHERE credential_id=$1`, cid)
		_, _ = pool.Exec(ctx, `DELETE FROM credential_model_bindings WHERE credential_id=$1`, cid)
		_, _ = pool.Exec(ctx, `DELETE FROM provider_models WHERE provider_id=$1 AND raw_model_name=$2`, pid, rawModel)
		_, _ = pool.Exec(ctx, `DELETE FROM credentials WHERE id=$1`, cid)
		_, _ = pool.Exec(ctx, `DELETE FROM providers WHERE id=$1`, pid)
	}
	return cid, pid, rawModel, cleanup
}

func TestDBStorage_SaveAndGetLatest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestPool(t)
	defer pool.Close()

	credID, _, rawModel, cleanup := setupTestFixture(t, pool)
	defer cleanup()

	store := NewDBStorage(pool)
	ctx := context.Background()

	score := &QualityScore{
		ModelName:      rawModel,
		CredentialID:   credID,
		Accuracy:       80.0,
		Stability:      100,
		Latency:        850,
		OverallScore:   82.5,
		Grade:          "B",
		BenchmarkType:  BenchmarkTypeMMLULite,
		TriggerKind:    "scheduled",
		TotalQuestions: 50,
		CorrectCount:   40,
		Timestamp:      time.Now(),
	}
	if err := store.SaveScore(ctx, score); err != nil {
		t.Fatalf("SaveScore: %v", err)
	}

	// node_iq_latest should have one row
	var latestScore float64
	err := pool.QueryRow(ctx,
		`SELECT overall_score FROM node_iq_latest WHERE credential_id=$1 AND raw_model_name=$2`,
		credID, rawModel).Scan(&latestScore)
	if err != nil {
		t.Fatalf("query node_iq_latest: %v", err)
	}
	if latestScore != 82.5 {
		t.Errorf("node_iq_latest.overall_score = %v, want 82.5", latestScore)
	}

	// GetLatestScore should return the same value
	got, err := store.GetLatestScore(ctx, "test", rawModel, credID)
	if err != nil {
		t.Fatalf("GetLatestScore: %v", err)
	}
	if got == nil {
		t.Fatal("GetLatestScore returned nil")
	}
	if got.OverallScore != 82.5 {
		t.Errorf("GetLatestScore overall = %v, want 82.5", got.OverallScore)
	}
	if got.Grade != "B" {
		t.Errorf("GetLatestScore grade = %q, want B", got.Grade)
	}
}

func TestDBStorage_HistoryOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestPool(t)
	defer pool.Close()

	credID, _, rawModel, cleanup := setupTestFixture(t, pool)
	defer cleanup()

	store := NewDBStorage(pool)
	ctx := context.Background()

	// Insert 3 scores with different timestamps
	base := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 3; i++ {
		score := &QualityScore{
			ModelName:      rawModel,
			CredentialID:   credID,
			Accuracy:       float64(60 + i*10),
			Stability:      100,
			OverallScore:   float64(65 + i*10),
			Grade:          "C",
			BenchmarkType:  BenchmarkTypeMMLULite,
			TriggerKind:    "scheduled",
			TotalQuestions: 50,
			CorrectCount:   30 + i*5,
			Timestamp:      base.Add(time.Duration(i) * time.Hour),
		}
		if err := store.SaveScore(ctx, score); err != nil {
			t.Fatalf("SaveScore[%d]: %v", i, err)
		}
	}

	history, err := store.GetScoreHistory(ctx, "test", rawModel, credID, 10)
	if err != nil {
		t.Fatalf("GetScoreHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	// Newest first
	if !history[0].Timestamp.After(history[1].Timestamp) {
		t.Errorf("history not newest-first: [0]=%v [1]=%v", history[0].Timestamp, history[1].Timestamp)
	}
	if history[0].OverallScore != 85 {
		t.Errorf("history[0].overall = %v, want 85", history[0].OverallScore)
	}
}

func TestDBStorage_FailedRunSkipsCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestPool(t)
	defer pool.Close()

	credID, _, rawModel, cleanup := setupTestFixture(t, pool)
	defer cleanup()

	store := NewDBStorage(pool)
	ctx := context.Background()

	// First: a good score
	good := &QualityScore{
		ModelName: rawModel, CredentialID: credID, Accuracy: 70, Stability: 100,
		OverallScore: 75, Grade: "C", BenchmarkType: BenchmarkTypeMMLULite,
		TriggerKind: "scheduled", TotalQuestions: 50, CorrectCount: 35,
		Timestamp: time.Now().Add(-1 * time.Hour),
	}
	if err := store.SaveScore(ctx, good); err != nil {
		t.Fatalf("SaveScore good: %v", err)
	}

	// Second: a failed score (accuracy=0 + stability=0 → status=failed)
	failed := &QualityScore{
		ModelName: rawModel, CredentialID: credID, Accuracy: 0, Stability: 0,
		OverallScore: 0, Grade: "F", BenchmarkType: BenchmarkTypeMMLULite,
		TriggerKind: "scheduled", TotalQuestions: 50, CorrectCount: 0,
		Timestamp: time.Now(),
	}
	if err := store.SaveScore(ctx, failed); err != nil {
		t.Fatalf("SaveScore failed: %v", err)
	}

	// node_iq_latest should still hold the good score (75), not 0
	var latestScore float64
	err := pool.QueryRow(ctx,
		`SELECT overall_score FROM node_iq_latest WHERE credential_id=$1 AND raw_model_name=$2`,
		credID, rawModel).Scan(&latestScore)
	if err != nil {
		t.Fatalf("query node_iq_latest: %v", err)
	}
	if latestScore != 75 {
		t.Errorf("node_iq_latest.overall_score = %v, want 75 (failed run must not overwrite cache)", latestScore)
	}

	// GetLatestScore should return 75 (failed excluded from success/partial)
	got, _ := store.GetLatestScore(ctx, "test", rawModel, credID)
	if got == nil || got.OverallScore != 75 {
		t.Errorf("GetLatestScore = %+v, want overall=75", got)
	}

	// But model_iq_runs should have BOTH rows
	var totalRuns int
	pool.QueryRow(ctx, `SELECT count(*) FROM model_iq_runs WHERE credential_id=$1`, credID).Scan(&totalRuns)
	if totalRuns != 2 {
		t.Errorf("model_iq_runs count = %d, want 2 (audit trail must keep failed)", totalRuns)
	}
}

func TestDBStorage_AggregateRecompute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestPool(t)
	defer pool.Close()

	credID, _, rawModel, cleanup := setupTestFixture(t, pool)
	defer cleanup()

	store := NewDBStorage(pool)
	ctx := context.Background()

	// Insert two scores: 70 and 90
	for _, s := range []float64{70, 90} {
		score := &QualityScore{
			ModelName: rawModel, CredentialID: credID, Accuracy: s, Stability: 100,
			OverallScore: s, Grade: "C", BenchmarkType: BenchmarkTypeMMLULite,
			TriggerKind: "scheduled", TotalQuestions: 50, CorrectCount: int(s / 2),
			Timestamp: time.Now(),
		}
		if err := store.SaveScore(ctx, score); err != nil {
			t.Fatalf("SaveScore %v: %v", s, err)
		}
		time.Sleep(10 * time.Millisecond) // ensure tested_at ordering
	}

	// node_iq_latest aggregates should reflect avg=80, min=70, max=90, count=2
	var avg, min, max float64
	var count int
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(avg_score,0), COALESCE(min_score,0), COALESCE(max_score,0), sample_count
		   FROM node_iq_latest WHERE credential_id=$1 AND raw_model_name=$2`,
		credID, rawModel).Scan(&avg, &min, &max, &count)
	if err != nil {
		t.Fatalf("query node_iq_latest aggregates: %v", err)
	}
	if count != 2 {
		t.Errorf("sample_count = %d, want 2", count)
	}
	if avg != 80 {
		t.Errorf("avg_score = %v, want 80", avg)
	}
	if min != 70 {
		t.Errorf("min_score = %v, want 70", min)
	}
	if max != 90 {
		t.Errorf("max_score = %v, want 90", max)
	}
}

func TestDBStorage_CleanupOldRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestPool(t)
	defer pool.Close()

	credID, _, rawModel, cleanup := setupTestFixture(t, pool)
	defer cleanup()

	store := NewDBStorage(pool)
	ctx := context.Background()

	// Insert a row with tested_at 10 days ago
	old := &QualityScore{
		ModelName: rawModel, CredentialID: credID, Accuracy: 50, Stability: 100,
		OverallScore: 55, Grade: "D", BenchmarkType: BenchmarkTypeMMLULite,
		TriggerKind: "scheduled", TotalQuestions: 50, CorrectCount: 25,
		Timestamp: time.Now().AddDate(0, 0, -10),
	}
	if err := store.SaveScore(ctx, old); err != nil {
		t.Fatalf("SaveScore old: %v", err)
	}

	// Insert a recent row
	recent := &QualityScore{
		ModelName: rawModel, CredentialID: credID, Accuracy: 70, Stability: 100,
		OverallScore: 75, Grade: "C", BenchmarkType: BenchmarkTypeMMLULite,
		TriggerKind: "scheduled", TotalQuestions: 50, CorrectCount: 35,
		Timestamp: time.Now(),
	}
	if err := store.SaveScore(ctx, recent); err != nil {
		t.Fatalf("SaveScore recent: %v", err)
	}

	// Cleanup runs older than 7 days
	deleted, err := store.CleanupOldRuns(ctx, 7)
	if err != nil {
		t.Fatalf("CleanupOldRuns: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	// Should have 1 row left
	var remaining int
	pool.QueryRow(ctx, `SELECT count(*) FROM model_iq_runs WHERE credential_id=$1`, credID).Scan(&remaining)
	if remaining != 1 {
		t.Errorf("remaining runs = %d, want 1", remaining)
	}
}
