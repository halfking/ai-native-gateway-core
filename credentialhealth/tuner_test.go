package credentialhealth

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/pashagolub/pgxmock/v4"
)

func TestTuner_OnError_RateLimit(t *testing.T) {
	// P2-7 fix (2026-06-22 audit): 429 rate-limit is now a no-op in the
	// tuner. The executor already applies Limiter.Shrink (fast in-memory
	// backoff) on 429; the tuner must NOT also cut concurrency_limit_auto
	// (slow DB-backed change) or the credential gets double-penalized and
	// takes an hour to recover. This test asserts the tuner issues NO
	// queries and NO updates for a rate_limit error.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mock.Close()

	cfg := DefaultTunerConfig()
	tuner := NewTuner(mock, cfg)

	credID := 42
	model := "minimax-m3"

	// No mock expectations set — a 429 must not touch the DB at all.
	ctx := context.Background()
	err = tuner.OnError(ctx, credID, model, errorsx.KindRateLimit)
	if err != nil {
		t.Fatalf("OnError failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("tuner should not query/update DB on rate_limit, unmet expectations: %v", err)
	}
}

func TestTuner_OnError_Concurrent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mock.Close()

	cfg := DefaultTunerConfig()
	tuner := NewTuner(mock, cfg)

	credID := 50
	model := "test-model"

	// Mock current limit = 20
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"limit"}).AddRow(20))

	// Expect UPDATE to 18 (20 * 0.90)
	mock.ExpectExec("UPDATE credentials").
		WithArgs(18, credID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ctx := context.Background()
	err = tuner.OnError(ctx, credID, model, errorsx.KindConcurrent)
	if err != nil {
		t.Fatalf("OnError failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTuner_OnError_MinimumLimit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mock.Close()

	cfg := DefaultTunerConfig()
	cfg.MinConcurrency = 2
	tuner := NewTuner(mock, cfg)

	credID := 99
	model := "test"

	// Current limit = 3, reduce by 10% (503 factor) → 2.7 → floor to 2 (min).
	// Uses KindConcurrent since the 429 path is now a no-op (P2-7).
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"limit"}).AddRow(3))

	mock.ExpectExec("UPDATE credentials").
		WithArgs(2, credID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ctx := context.Background()
	err = tuner.OnError(ctx, credID, model, errorsx.KindConcurrent)
	if err != nil {
		t.Fatalf("OnError failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTuner_IncreaseConcurrency(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mock.Close()

	cfg := DefaultTunerConfig()
	tuner := NewTuner(mock, cfg)

	credID := 10
	model := "model-x"

	// Current limit = 15
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"limit"}).AddRow(15))

	// Expect UPDATE to 16
	mock.ExpectExec("UPDATE credentials").
		WithArgs(16, credID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ctx := context.Background()
	err = tuner.IncreaseConcurrency(ctx, credID, model)
	if err != nil {
		t.Fatalf("IncreaseConcurrency failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTuner_IncreaseConcurrency_MaxLimit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mock.Close()

	cfg := DefaultTunerConfig()
	cfg.MaxConcurrency = 20
	tuner := NewTuner(mock, cfg)

	credID := 10
	model := "model-x"

	// Already at max
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"limit"}).AddRow(20))

	// No UPDATE expected (would exceed max)

	ctx := context.Background()
	err = tuner.IncreaseConcurrency(ctx, credID, model)
	if err != nil {
		t.Fatalf("IncreaseConcurrency failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTuner_GetEffectiveLimit_Priority(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}
	defer mock.Close()

	cfg := DefaultTunerConfig()
	tuner := NewTuner(mock, cfg)

	tests := []struct {
		name     string
		manual   *int
		auto     *int
		expected int
	}{
		{
			name:     "manual overrides auto",
			manual:   intPtr(15),
			auto:     intPtr(10),
			expected: 15,
		},
		{
			name:     "auto when no manual",
			manual:   nil,
			auto:     intPtr(8),
			expected: 8,
		},
		{
			name:     "default when both nil",
			manual:   nil,
			auto:     nil,
			expected: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credID := 100
			mock.ExpectQuery("SELECT concurrency_limit, concurrency_limit_auto").
				WithArgs(credID).
				WillReturnRows(pgxmock.NewRows([]string{"concurrency_limit", "concurrency_limit_auto"}).
					AddRow(tt.manual, tt.auto))

			ctx := context.Background()
			limit, err := tuner.GetEffectiveLimit(ctx, credID)
			if err != nil {
				t.Fatalf("GetEffectiveLimit failed: %v", err)
			}

			if limit != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, limit)
			}
		})
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func intPtr(i int) *int {
	return &i
}
