package credential

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBanditFlusher_SingleUpdate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create test credential
	credID := createTestCredential(t, db)
	credIDStr := fmt.Sprintf("%d", credID)

	// Create bandit and flusher
	bandit := NewBanditScorer()
	flusher := NewBanditFlusher(db, bandit, 100*time.Millisecond, 10)

	// Record some events
	bandit.RecordSuccess(credIDStr, 100)
	bandit.RecordSuccess(credIDStr, 150)
	bandit.RecordSuccess(credIDStr, 200)
	bandit.RecordFailure(credIDStr)

	flusher.MarkDirty(credIDStr)

	// Trigger flush
	flusher.Flush()

	// Verify database
	var alpha, beta float64
	var successCount, failureCount int64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.QueryRow(ctx, `
		SELECT bandit_alpha, bandit_beta, bandit_success_count, bandit_failure_count
		FROM api_keys WHERE id = $1
	`, credID).Scan(&alpha, &beta, &successCount, &failureCount)

	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	// Alpha should be 1 + 3 successes = 4
	// Beta should be 1 + 1 failure = 2
	if alpha != 4.0 || beta != 2.0 {
		t.Errorf("alpha/beta mismatch: got %.1f/%.1f, want 4.0/2.0", alpha, beta)
	}
	if successCount != 3 || failureCount != 1 {
		t.Errorf("counts mismatch: got %d/%d, want 3/1", successCount, failureCount)
	}
}

func TestBanditFlusher_BatchUpdates(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Create multiple test credentials
	credIDs := make([]int, 5)
	for i := 0; i < 5; i++ {
		credIDs[i] = createTestCredential(t, db)
	}

	bandit := NewBanditScorer()
	flusher := NewBanditFlusher(db, bandit, 100*time.Millisecond, 10)

	// Record different numbers of successes for each
	for i, credID := range credIDs {
		credIDStr := fmt.Sprintf("%d", credID)
		for j := 0; j < i+1; j++ {
			bandit.RecordSuccess(credIDStr, 100)
		}
		flusher.MarkDirty(credIDStr)
	}

	// Flush
	flusher.Flush()

	// Verify all were updated
	for i, credID := range credIDs {
		var alpha float64
		var successCount int64
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := db.QueryRow(ctx, `SELECT bandit_alpha, bandit_success_count FROM api_keys WHERE id = $1`, credID).
			Scan(&alpha, &successCount)
		cancel()
		if err != nil {
			t.Fatalf("query failed for cred %d: %v", credID, err)
		}

		expectedSuccesses := int64(i + 1)
		expectedAlpha := 1.0 + float64(expectedSuccesses) // Prior + successes

		if alpha != expectedAlpha {
			t.Errorf("cred %d: got alpha %.1f, want %.1f", credID, alpha, expectedAlpha)
		}
		if successCount != expectedSuccesses {
			t.Errorf("cred %d: got successCount %d, want %d", credID, successCount, expectedSuccesses)
		}
	}
}

func TestBanditFlusher_AutoFlush(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	credID := createTestCredential(t, db)
	credIDStr := fmt.Sprintf("%d", credID)

	bandit := NewBanditScorer()
	// Short interval for testing
	flusher := NewBanditFlusher(db, bandit, 50*time.Millisecond, 10)
	flusher.Start()
	defer flusher.Stop()

	// Record success
	bandit.RecordSuccess(credIDStr, 100)
	flusher.MarkDirty(credIDStr)

	// Wait for auto-flush
	time.Sleep(150 * time.Millisecond)

	// Verify
	var alpha float64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.QueryRow(ctx, `SELECT bandit_alpha FROM api_keys WHERE id = $1`, credID).Scan(&alpha)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if alpha != 2.0 { // 1 (prior) + 1 (success)
		t.Errorf("got alpha %.1f, want 2.0", alpha)
	}
}

func TestBanditFlusher_429Penalty(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	credID := createTestCredential(t, db)
	credIDStr := fmt.Sprintf("%d", credID)

	bandit := NewBanditScorer()
	flusher := NewBanditFlusher(db, bandit, 100*time.Millisecond, 10)

	// Record 429
	bandit.RecordRateLimitHit(credIDStr)
	flusher.MarkDirty(credIDStr)
	flusher.Flush()

	// Verify 429 count
	var count429 int64
	var penalty float64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.QueryRow(ctx, `
		SELECT bandit_429_count, penalty_429_accumulated
		FROM api_keys WHERE id = $1
	`, credID).Scan(&count429, &penalty)

	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if count429 != 1 {
		t.Errorf("got 429_count %d, want 1", count429)
	}
	if penalty <= 0 {
		t.Errorf("got penalty %.2f, want > 0", penalty)
	}
}

// Helper functions

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, "postgres://kxuser:kaixuan2024@localhost:5434/llm_gateway?sslmode=disable")
	if err != nil {
		t.Skipf("cannot connect to test DB: %v", err)
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		t.Skipf("test DB not available: %v", err)
	}
	return db
}

func createTestCredential(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id int
	err := db.QueryRow(ctx, `
		INSERT INTO api_keys (
			api_key, tenant_id, provider, model, enabled, status,
			bandit_alpha, bandit_beta, bandit_success_count, 
			bandit_failure_count, bandit_429_count,
			bandit_total_latency_ms, bandit_avg_latency_ms,
			penalty_429_accumulated
		) VALUES (
			$1, $2, $3, $4, true, 'active',
			1.0, 1.0, 0, 0, 0, 0, 0, 0.0
		) RETURNING id
	`, randomKey(), "test-tenant", "test-provider", "test-model").Scan(&id)

	if err != nil {
		t.Fatalf("failed to create test credential: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.Exec(cleanupCtx, `DELETE FROM api_keys WHERE id = $1`, id)
	})

	return id
}

func randomKey() string {
	return "test-key-" + time.Now().Format("20060102150405.000000")
}
