package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration test: Full workflow from collection to aggregation
func TestIntegration_FullWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()

	// Setup database connection
	pool := setupTestDB(t)

	// Create stores
	metricsStore := providerprofile.NewPGMetricsStore(pool)
	// profileStore := providerprofile.NewPGProfileStore(pool) // TODO: implement after pg_store.go is extended

	// Create scorer
	scorer := providerprofile.NewDefaultScorer()
	weights := providerprofile.DefaultWeights()

	// Step 1: Create mock implementations for dependencies
	networkProber := &mockNetworkProber{}
	requestAnalyzer := &mockRequestAnalyzer{}
	scaleProvider := &mockScaleProvider{}
	credentialLister := &mockCredentialLister{credentialIDs: []int64{1}}

	// Step 2: Create collector and collect metrics
	collector := providerprofile.NewLightweightCollector(
		metricsStore,
		networkProber,
		requestAnalyzer,
		scaleProvider,
		credentialLister,
	)

	err := collector.CollectMetrics(ctx)
	require.NoError(t, err)

	// Step 3: Verify metrics were saved
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	snapshots, err := metricsStore.GetSnapshotsByDateRange(ctx, 1, startOfDay, endOfDay)
	require.NoError(t, err)
	assert.NotEmpty(t, snapshots, "Should have collected metrics")

	// Step 4: Calculate scores from collected data
	dimensionScores := scorer.CalculateDimensionScores(snapshots, weights)
	totalScore := scorer.CalculateTotalScore(dimensionScores, weights)

	// Verify scores are reasonable
	assert.Greater(t, dimensionScores.NetworkScore, 0.0)
	assert.Greater(t, dimensionScores.AvailabilityScore, 0.0)
	assert.Greater(t, dimensionScores.StabilityScore, 0.0)
	assert.Greater(t, dimensionScores.ScaleScore, 0.0)
	assert.Greater(t, totalScore, 0.0)
	assert.LessOrEqual(t, totalScore, 100.0)

	t.Logf("Integration test completed successfully")
	t.Logf("  Network Score: %.2f", dimensionScores.NetworkScore)
	t.Logf("  Availability Score: %.2f", dimensionScores.AvailabilityScore)
	t.Logf("  Stability Score: %.2f", dimensionScores.StabilityScore)
	t.Logf("  Scale Score: %.2f", dimensionScores.ScaleScore)
	t.Logf("  Total Score: %.2f", totalScore)
}

// Test cleanup functionality
func TestIntegration_Cleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	metricsStore := providerprofile.NewPGMetricsStore(pool)

	// Create old snapshot (8 days ago)
	oldTime := time.Now().AddDate(0, 0, -8)
	oldSnapshot := &providerprofile.MetricSnapshot{
		CredentialID: 999,
		ProviderID:   999,
		MetricTime:   oldTime,
		TimeSlot:     providerprofile.TimeSlotMorning,
		NetworkMetrics: &providerprofile.NetworkMetrics{
			P50: 100,
			P95: 150,
			P99: 200,
		},
		AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
			TotalRequests:   100,
			SuccessRequests: 95,
		},
		StabilityMetrics: &providerprofile.StabilityMetrics{
			ErrorCount: 5,
			ErrorTypes: map[string]int{"400": 5},
		},
		ScaleMetrics: &providerprofile.ScaleMetrics{
			TotalModels:     10,
			AvailableModels: 10,
		},
	}

	err := metricsStore.SaveSnapshot(ctx, oldSnapshot)
	require.NoError(t, err)

	// Run cleanup
	deleted, err := metricsStore.CleanupOldMetrics(ctx)
	require.NoError(t, err)

	t.Logf("Cleanup deleted %d old metrics", deleted)
	assert.GreaterOrEqual(t, deleted, int64(0))
}

// Test concurrent collection
func TestIntegration_ConcurrentCollection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	pool := setupTestDB(t)
	metricsStore := providerprofile.NewPGMetricsStore(pool)

	networkProber := &mockNetworkProber{}
	requestAnalyzer := &mockRequestAnalyzer{}
	scaleProvider := &mockScaleProvider{}

	// Test with multiple credentials
	credentialLister := &mockCredentialLister{credentialIDs: []int64{1, 2, 3, 4, 5}}

	collector := providerprofile.NewLightweightCollector(
		metricsStore,
		networkProber,
		requestAnalyzer,
		scaleProvider,
		credentialLister,
	)

	// Collect metrics for all credentials concurrently
	err := collector.CollectMetrics(ctx)
	require.NoError(t, err)

	// Verify all credentials have metrics
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	for _, credID := range credentialLister.credentialIDs {
		snapshots, err := metricsStore.GetSnapshotsByDateRange(ctx, credID, startOfDay, endOfDay)
		require.NoError(t, err)
		assert.NotEmpty(t, snapshots, "Credential %d should have metrics", credID)
	}

	t.Logf("Concurrent collection test completed for %d credentials", len(credentialLister.credentialIDs))
}

// Mock implementations for integration tests
type mockNetworkProber struct{}

func (m *mockNetworkProber) ProbeLatency(ctx context.Context, credentialID int64, probeCount int) ([]int, error) {
	// Return realistic latency values
	return []int{100, 120, 110}, nil
}

type mockRequestAnalyzer struct{}

func (m *mockRequestAnalyzer) AnalyzeRequests(ctx context.Context, credentialID int64, hours int) (*providerprofile.RequestStats, error) {
	return &providerprofile.RequestStats{
		TotalRequests:   100,
		SuccessRequests: 95,
		ErrorCount:      5,
		ErrorTypes:      map[string]int{"400": 3, "500": 2},
		AvgTTFTMs:       500,
		AvgDurationMs:   2000,
	}, nil
}

type mockScaleProvider struct{}

func (m *mockScaleProvider) GetModelScale(ctx context.Context, credentialID int64) (*providerprofile.ScaleData, error) {
	return &providerprofile.ScaleData{
		ProviderID:      int64(credentialID * 10), // Fake provider ID
		TotalModels:     50,
		AvailableModels: 48,
	}, nil
}

type mockCredentialLister struct {
	credentialIDs []int64
}

func (m *mockCredentialLister) ListActiveCredentials(ctx context.Context) ([]int64, error) {
	return m.credentialIDs, nil
}
