package bg

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	_ "github.com/lib/pq"
)

func TestBanditFlusher_Basic(t *testing.T) {
	scorer := credential.NewBanditScorer()
	
	// Record some activity
	scorer.RecordSuccess("1", 100)
	scorer.RecordSuccess("1", 150)
	scorer.RecordFailure("1")
	scorer.RecordRateLimitHit("1")
	
	scorer.RecordSuccess("2", 200)
	scorer.UpdateQuota("2", 500, 1000)

	// Create flusher (without real DB for unit test)
	flusher := NewBanditFlusher(scorer, nil, 5*time.Second)
	
	if flusher.FlushInterval != 5*time.Second {
		t.Errorf("FlushInterval should be 5s, got %v", flusher.FlushInterval)
	}
	
	// Verify scorer has state
	scores := scorer.GetAllScores()
	if len(scores) != 2 {
		t.Errorf("Expected 2 credentials, got %d", len(scores))
	}
	
	score1 := scores["1"]
	if score1.TotalRequests != 3 {
		t.Errorf("Cred 1: expected 3 requests, got %d", score1.TotalRequests)
	}
	if score1.SuccessRequests != 2 {
		t.Errorf("Cred 1: expected 2 successes, got %d", score1.SuccessRequests)
	}
	if score1.RateLimitHits != 1 {
		t.Errorf("Cred 1: expected 1 rate limit hit, got %d", score1.RateLimitHits)
	}
	
	score2 := scores["2"]
	if score2.QuotaRemaining == nil || *score2.QuotaRemaining != 500 {
		t.Errorf("Cred 2: expected quota remaining 500, got %v", score2.QuotaRemaining)
	}
}

func TestBanditFlusher_DefaultInterval(t *testing.T) {
	scorer := credential.NewBanditScorer()
	flusher := NewBanditFlusher(scorer, nil, 0)
	
	if flusher.FlushInterval != 10*time.Second {
		t.Errorf("Default FlushInterval should be 10s, got %v", flusher.FlushInterval)
	}
}

func TestBanditFlusher_NilScorer(t *testing.T) {
	flusher := NewBanditFlusher(nil, nil, 5*time.Second)
	
	// Should not panic
	err := flusher.Flush(context.Background())
	if err != nil {
		t.Errorf("Flush with nil scorer should not error, got %v", err)
	}
}

func TestBanditFlusher_EmptyScores(t *testing.T) {
	scorer := credential.NewBanditScorer()
	flusher := NewBanditFlusher(scorer, nil, 5*time.Second)
	
	// Should not panic with empty scores
	err := flusher.Flush(context.Background())
	if err != nil {
		t.Errorf("Flush with empty scores should not error, got %v", err)
	}
}

func TestBanditFlusher_StartStop(t *testing.T) {
	scorer := credential.NewBanditScorer()
	flusher := NewBanditFlusher(scorer, nil, 100*time.Millisecond)
	
	ctx := context.Background()
	flusher.Start(ctx)
	
	// Let it run for a bit
	time.Sleep(250 * time.Millisecond)
	
	// Stop should not panic
	flusher.Stop()
	
	// Give it time to shut down
	time.Sleep(50 * time.Millisecond)
}

// TestBanditFlusher_Integration tests with a real database
// This test is skipped by default - set TEST_DB_URL to enable
func TestBanditFlusher_Integration(t *testing.T) {
	dbURL := "postgres://localhost/test_llm_gateway?sslmode=disable"
	// Skip if no DB
	t.Skip("Integration test - set TEST_DB_URL to run")
	
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to DB: %v", err)
	}
	defer db.Close()
	
	// Create test credential
	_, err = db.Exec(`
		INSERT INTO credentials (id, tenant_id, provider_id, status)
		VALUES (999999, 'test', 1, 'active')
		ON CONFLICT (id) DO NOTHING
	`)
	if err != nil {
		t.Fatalf("Failed to create test credential: %v", err)
	}
	defer db.Exec("DELETE FROM credentials WHERE id = 999999")
	
	scorer := credential.NewBanditScorer()
	scorer.RecordSuccess("999999", 100)
	scorer.RecordFailure("999999")
	
	flusher := NewBanditFlusher(scorer, db, 1*time.Second)
	
	// Flush
	err = flusher.Flush(context.Background())
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	
	// Verify DB was updated
	var alpha, beta float64
	err = db.QueryRow(`
		SELECT bandit_alpha, bandit_beta
		FROM credentials
		WHERE id = 999999
	`).Scan(&alpha, &beta)
	if err != nil {
		t.Fatalf("Failed to query: %v", err)
	}
	
	if alpha != 2.0 {
		t.Errorf("Expected alpha=2.0, got %v", alpha)
	}
	if beta != 2.0 {
		t.Errorf("Expected beta=2.0, got %v", beta)
	}
}
