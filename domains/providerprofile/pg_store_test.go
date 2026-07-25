package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	// 使用本地Docker测试数据库连接
	connString := "postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable"
	pool, err := pgxpool.New(context.Background(), connString)
	require.NoError(t, err)

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
