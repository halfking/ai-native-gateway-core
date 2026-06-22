package credentialhealth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRecorder_AppendAndGetRecent(t *testing.T) {
	// Setup mock Redis
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	recorder := NewRecorder(client, 1*time.Hour, 100)
	ctx := context.Background()

	credID := 42
	model := "minimax-m3"
	now := time.Now()

	// Append 5 successful calls
	for i := 0; i < 5; i++ {
		entry := CallEntry{
			RequestID: "req_success_" + string(rune('0'+i)),
			Timestamp: now.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Success:   true,
			LatencyMs: 200 + i*10,
		}
		if err := recorder.Append(ctx, credID, model, entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	// Append 3 failed calls (429, 503, quota)
	failedErrors := []string{"rate_limit", "concurrent", "quota"}
	for i, errKind := range failedErrors {
		entry := CallEntry{
			RequestID: "req_fail_" + string(rune('0'+i)),
			Timestamp: now.Add(time.Duration(5+i) * time.Minute).UnixMilli(),
			Success:   false,
			LatencyMs: 0,
			ErrorKind: errKind,
		}
		if err := recorder.Append(ctx, credID, model, entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	// Get recent (last 10 minutes)
	since := now.Add(-10 * time.Minute)
	entries, err := recorder.GetRecent(ctx, credID, model, since)
	if err != nil {
		t.Fatalf("get recent failed: %v", err)
	}

	if len(entries) != 8 {
		t.Errorf("expected 8 entries, got %d", len(entries))
	}

	// Verify stats
	stats := ComputeStats(entries)
	if stats.Total != 8 {
		t.Errorf("total: expected 8, got %d", stats.Total)
	}
	if stats.Success != 5 {
		t.Errorf("success: expected 5, got %d", stats.Success)
	}
	if stats.Failed != 3 {
		t.Errorf("failed: expected 3, got %d", stats.Failed)
	}

	expectedRate := 3.0 / 8.0
	if stats.FailureRate != expectedRate {
		t.Errorf("failure rate: expected %.2f, got %.2f", expectedRate, stats.FailureRate)
	}

	if stats.ErrorKinds["rate_limit"] != 1 {
		t.Errorf("rate_limit count: expected 1, got %d", stats.ErrorKinds["rate_limit"])
	}
}

func TestRecorder_MaxSize_LTRIM(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer client.Close()

	// Max size = 10
	recorder := NewRecorder(client, 1*time.Hour, 10)
	ctx := context.Background()

	credID := 99
	model := "test-model"
	now := time.Now()

	// Append 20 entries (should keep only last 10)
	for i := 0; i < 20; i++ {
		entry := CallEntry{
			RequestID: "req_" + string(rune('0'+i)),
			Timestamp: now.Add(time.Duration(i) * time.Second).UnixMilli(),
			Success:   true,
			LatencyMs: 100,
		}
		if err := recorder.Append(ctx, credID, model, entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	// Get all
	entries, err := recorder.GetRecent(ctx, credID, model, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("get recent failed: %v", err)
	}

	if len(entries) != 10 {
		t.Errorf("expected 10 entries (LTRIM), got %d", len(entries))
	}

	// Verify newest entries are kept (req_10 to req_19)
	// Entries are stored newest-first in LIST
	firstEntry := entries[0]
	if firstEntry.RequestID != "req_9" { // ASCII '0'+19 = 'S', so this test needs adjustment
		// Actually req_10-19 would be chars beyond digits, let's check timestamp instead
		lastTimestamp := now.Add(19 * time.Second).UnixMilli()
		if entries[0].Timestamp != lastTimestamp {
			t.Errorf("expected newest entry timestamp %d, got %d", lastTimestamp, entries[0].Timestamp)
		}
	}
}

func TestRecorder_DisabledRedis(t *testing.T) {
	// Nil client
	recorder := NewRecorder(nil, 1*time.Hour, 100)
	if recorder.Enabled() {
		t.Error("should be disabled with nil client")
	}

	ctx := context.Background()
	entry := CallEntry{
		RequestID: "req_1",
		Timestamp: time.Now().UnixMilli(),
		Success:   true,
		LatencyMs: 100,
	}

	// Should not error
	if err := recorder.Append(ctx, 1, "model", entry); err != nil {
		t.Errorf("append should not error when disabled: %v", err)
	}

	entries, err := recorder.GetRecent(ctx, 1, "model", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Errorf("get recent should not error when disabled: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("should return empty when disabled, got %d entries", len(entries))
	}
}
