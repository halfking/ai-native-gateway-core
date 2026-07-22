package config

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestTimeoutConfig_CalculateStatic(t *testing.T) {
	tc := &TimeoutConfig{
		upstreamBaseSeconds: 90,
		mode:                TimeoutModeStatic,
	}

	result := tc.CalculateEffectiveTimeout(TimeoutCalculationInput{
		ContextSizeTokens: 50000,
	})

	if result.EffectiveTimeoutSeconds != 90 {
		t.Errorf("expected 90s, got %d", result.EffectiveTimeoutSeconds)
	}
	if result.Mode != TimeoutModeStatic {
		t.Errorf("expected static mode, got %s", result.Mode)
	}
}

func TestTimeoutConfig_CalculateContextAware(t *testing.T) {
	tc := &TimeoutConfig{
		upstreamBaseSeconds:    90,
		upstreamMinSeconds:     20,
		upstreamMaxSeconds:     180,
		contextThresholdTokens: 20000,
		contextBonusSeconds:    45,
		mode:                   TimeoutModeContextAware,
	}

	tests := []struct {
		name             string
		contextTokens    int
		expectedTimeout  int
		expectedInReason string
	}{
		{
			name:             "small context",
			contextTokens:    10000,
			expectedTimeout:  90,
			expectedInReason: "below_threshold",
		},
		{
			name:             "large context",
			contextTokens:    50000,
			expectedTimeout:  135, // 90 + 45
			expectedInReason: "exceeds_threshold",
		},
		{
			name:             "at threshold",
			contextTokens:    20000,
			expectedTimeout:  90,
			expectedInReason: "below_threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tc.CalculateEffectiveTimeout(TimeoutCalculationInput{
				ContextSizeTokens: tt.contextTokens,
			})

			if result.EffectiveTimeoutSeconds != tt.expectedTimeout {
				t.Errorf("expected %ds, got %ds", tt.expectedTimeout, result.EffectiveTimeoutSeconds)
			}
			if result.Mode != TimeoutModeContextAware {
				t.Errorf("expected context_aware mode, got %s", result.Mode)
			}
		})
	}
}

func TestTimeoutConfig_CalculateAdaptive(t *testing.T) {
	tc := &TimeoutConfig{
		upstreamBaseSeconds:    90,
		upstreamMinSeconds:     20,
		upstreamMaxSeconds:     180,
		contextThresholdTokens: 20000,
		contextBonusSeconds:    45,
		mode:                   TimeoutModeAdaptive,
	}

	tests := []struct {
		name             string
		input            TimeoutCalculationInput
		expectedMin      int
		expectedMax      int
		expectedInReason string
	}{
		{
			name: "small context, no history",
			input: TimeoutCalculationInput{
				ContextSizeTokens: 10000,
			},
			expectedMin: 90,
			expectedMax: 90,
		},
		{
			name: "large context, no history",
			input: TimeoutCalculationInput{
				ContextSizeTokens: 50000,
			},
			expectedMin: 135, // 90 + 45
			expectedMax: 135,
		},
		{
			name: "large context + historical latency",
			input: TimeoutCalculationInput{
				ContextSizeTokens:   50000,
				HistoricalLatencyMS: 40000, // 40s
			},
			expectedMin: 145, // 90 + 45 + 10 (25% of 40s)
			expectedMax: 145,
		},
		{
			name: "large context + high network latency",
			input: TimeoutCalculationInput{
				ContextSizeTokens: 50000,
				NetworkLatencyMS:  600,
			},
			expectedMin: 140, // 90 + 45 + 5
			expectedMax: 140,
		},
		{
			name: "all factors combined",
			input: TimeoutCalculationInput{
				ContextSizeTokens:   50000,
				HistoricalLatencyMS: 40000,
				NetworkLatencyMS:    600,
			},
			expectedMin: 150, // 90 + 45 + 10 + 5
			expectedMax: 150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tc.CalculateEffectiveTimeout(tt.input)

			if result.EffectiveTimeoutSeconds < tt.expectedMin || result.EffectiveTimeoutSeconds > tt.expectedMax {
				t.Errorf("expected timeout in range [%d, %d], got %d",
					tt.expectedMin, tt.expectedMax, result.EffectiveTimeoutSeconds)
			}
			if result.Mode != TimeoutModeAdaptive {
				t.Errorf("expected adaptive mode, got %s", result.Mode)
			}
		})
	}
}

func TestTimeoutConfig_Clamp(t *testing.T) {
	tc := &TimeoutConfig{
		upstreamMinSeconds: 20,
		upstreamMaxSeconds: 180,
	}

	tests := []struct {
		input    int
		expected int
	}{
		{input: 10, expected: 20},   // below min
		{input: 90, expected: 90},   // in range
		{input: 200, expected: 180}, // above max
		{input: 20, expected: 20},   // at min
		{input: 180, expected: 180}, // at max
	}

	for _, tt := range tests {
		result := tc.clamp(tt.input)
		if result != tt.expected {
			t.Errorf("clamp(%d) = %d, want %d", tt.input, result, tt.expected)
		}
	}
}

func TestTimeoutConfig_ReloadFromDB_Fallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Create with nil DB (should use defaults)
	tc := &TimeoutConfig{
		db:                     nil,
		logger:                 logger,
		clientDefaultSeconds:   60,
		upstreamBaseSeconds:    90,
		upstreamMinSeconds:     20,
		upstreamMaxSeconds:     180,
		contextThresholdTokens: 20000,
		mode:                   TimeoutModeAdaptive,
	}

	err := tc.ReloadFromDB(context.Background())
	if err == nil {
		t.Error("expected error with nil DB, got nil")
	}

	// Should still use defaults
	if tc.upstreamBaseSeconds != 90 {
		t.Errorf("expected default 90s, got %d", tc.upstreamBaseSeconds)
	}
}

func TestTimeoutConfig_GetRetryConfig(t *testing.T) {
	tc := &TimeoutConfig{
		maxRetryAttempts:   3,
		baseDelayMS:        1000,
		maxDelayMS:         10000,
		exponentialBackoff: true,
	}

	maxAttempts, baseDelay, maxDelay, expBackoff := tc.GetRetryConfig()

	if maxAttempts != 3 {
		t.Errorf("expected maxAttempts=3, got %d", maxAttempts)
	}
	if baseDelay != 1000 {
		t.Errorf("expected baseDelay=1000, got %d", baseDelay)
	}
	if maxDelay != 10000 {
		t.Errorf("expected maxDelay=10000, got %d", maxDelay)
	}
	if !expBackoff {
		t.Error("expected exponentialBackoff=true, got false")
	}
}

func TestTimeoutConfig_GetKeepaliveInterval(t *testing.T) {
	tc := &TimeoutConfig{
		keepaliveIntervalSecs: 15,
	}

	interval := tc.GetKeepaliveInterval()
	if interval != 15 {
		t.Errorf("expected 15s, got %d", interval)
	}
}

func TestTimeoutConfig_GetCurrentMode(t *testing.T) {
	tc := &TimeoutConfig{
		mode: TimeoutModeAdaptive,
	}

	mode := tc.GetCurrentMode()
	if mode != TimeoutModeAdaptive {
		t.Errorf("expected adaptive, got %s", mode)
	}
}

// Integration test with real database (only runs if DB_TEST_URL is set)
func TestTimeoutConfig_ReloadFromDB_Integration(t *testing.T) {
	dbURL := os.Getenv("DB_TEST_URL")
	if dbURL == "" {
		t.Skip("DB_TEST_URL not set, skipping integration test")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tc := NewTimeoutConfig(db, logger)
	defer tc.Stop()

	// Wait a bit for initial load
	time.Sleep(100 * time.Millisecond)

	// Verify config was loaded
	baseTimeout := tc.GetBaseTimeout()
	if baseTimeout == 0 {
		t.Error("expected non-zero base timeout after reload")
	}

	t.Logf("Loaded config: baseTimeout=%ds, mode=%s",
		baseTimeout, tc.GetCurrentMode())
}

func BenchmarkTimeoutConfig_CalculateAdaptive(b *testing.B) {
	tc := &TimeoutConfig{
		upstreamBaseSeconds:    90,
		upstreamMinSeconds:     20,
		upstreamMaxSeconds:     180,
		contextThresholdTokens: 20000,
		contextBonusSeconds:    45,
		mode:                   TimeoutModeAdaptive,
	}

	input := TimeoutCalculationInput{
		ContextSizeTokens:   50000,
		HistoricalLatencyMS: 40000,
		NetworkLatencyMS:    600,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tc.CalculateEffectiveTimeout(input)
	}
}
