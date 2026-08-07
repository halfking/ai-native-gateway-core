package providerprofile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	connString := os.Getenv("TEST_DATABASE_URL")
	if connString == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	pool, err := pgxpool.New(context.Background(), connString)
	require.NoError(t, err)
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("integration database unavailable: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
	})

	return pool
}

func TestPGMetricsStore_SaveSnapshot(t *testing.T) {
	pool := setupTestDB(t)
	store := providerprofile.NewPGMetricsStore(pool)

	snapshot := &providerprofile.MetricSnapshot{
		CredentialID: 1,
		ProviderID:   1,
		MetricTime:   time.Now(),
		TimeSlot:     providerprofile.TimeSlotMorning,
		NetworkMetrics: &providerprofile.NetworkMetrics{
			P50: 100,
			P95: 200,
			P99: 300,
		},
		AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
			TotalRequests:   100,
			SuccessRequests: 95,
			AvgTTFTMs:       500,
			AvgDurationMs:   2000,
		},
		StabilityMetrics: &providerprofile.StabilityMetrics{
			ErrorCount: 5,
			ErrorTypes: map[string]int{"500": 3, "timeout": 2},
		},
		ScaleMetrics: &providerprofile.ScaleMetrics{
			TotalModels:     10,
			AvailableModels: 9,
		},
	}

	err := store.SaveSnapshot(context.Background(), snapshot)
	assert.NoError(t, err)
}
