package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Mock implementations
type MockMetricsStore struct {
	mock.Mock
}

func (m *MockMetricsStore) SaveSnapshot(ctx context.Context, snapshot *providerprofile.MetricSnapshot) error {
	args := m.Called(ctx, snapshot)
	return args.Error(0)
}

func (m *MockMetricsStore) GetSnapshotsByDateRange(ctx context.Context, credentialID int64, start, end time.Time) ([]*providerprofile.MetricSnapshot, error) {
	args := m.Called(ctx, credentialID, start, end)
	return args.Get(0).([]*providerprofile.MetricSnapshot), args.Error(1)
}

func (m *MockMetricsStore) CleanupOldMetrics(ctx context.Context) (int64, error) {
	args := m.Called(ctx)
	return args.Get(0).(int64), args.Error(1)
}

type MockNetworkProber struct {
	mock.Mock
}

func (m *MockNetworkProber) ProbeLatency(ctx context.Context, credentialID int64, probeCount int) ([]int, error) {
	args := m.Called(ctx, credentialID, probeCount)
	return args.Get(0).([]int), args.Error(1)
}

type MockRequestAnalyzer struct {
	mock.Mock
}

func (m *MockRequestAnalyzer) AnalyzeRequests(ctx context.Context, credentialID int64, hours int) (*providerprofile.RequestStats, error) {
	args := m.Called(ctx, credentialID, hours)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*providerprofile.RequestStats), args.Error(1)
}

type MockScaleProvider struct {
	mock.Mock
}

func (m *MockScaleProvider) GetModelScale(ctx context.Context, credentialID int64) (*providerprofile.ScaleData, error) {
	args := m.Called(ctx, credentialID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*providerprofile.ScaleData), args.Error(1)
}

type MockCredentialLister struct {
	mock.Mock
}

func (m *MockCredentialLister) ListActiveCredentials(ctx context.Context) ([]int64, error) {
	args := m.Called(ctx)
	return args.Get(0).([]int64), args.Error(1)
}

func TestLightweightCollector_CollectMetrics(t *testing.T) {
	ctx := context.Background()

	// Setup mocks
	metricsStore := new(MockMetricsStore)
	networkProber := new(MockNetworkProber)
	requestAnalyzer := new(MockRequestAnalyzer)
	scaleProvider := new(MockScaleProvider)
	credentialLister := new(MockCredentialLister)

	// Mock expectations
	credentialLister.On("ListActiveCredentials", ctx).Return([]int64{1, 2}, nil)

	// For credential 1
	networkProber.On("ProbeLatency", ctx, int64(1), 3).Return([]int{100, 120, 110}, nil)
	requestAnalyzer.On("AnalyzeRequests", ctx, int64(1), 2).Return(&providerprofile.RequestStats{
		TotalRequests:   100,
		SuccessRequests: 95,
		ErrorCount:      5,
		ErrorTypes:      map[string]int{"400": 5},
		AvgTTFTMs:       500,
		AvgDurationMs:   2000,
	}, nil)
	scaleProvider.On("GetModelScale", ctx, int64(1)).Return(&providerprofile.ScaleData{
		ProviderID:      10,
		TotalModels:     50,
		AvailableModels: 48,
	}, nil)
	metricsStore.On("SaveSnapshot", ctx, mock.MatchedBy(func(s *providerprofile.MetricSnapshot) bool {
		return s.CredentialID == 1 && s.ProviderID == 10
	})).Return(nil)

	// For credential 2
	networkProber.On("ProbeLatency", ctx, int64(2), 3).Return([]int{150, 160, 155}, nil)
	requestAnalyzer.On("AnalyzeRequests", ctx, int64(2), 2).Return(&providerprofile.RequestStats{
		TotalRequests:   200,
		SuccessRequests: 190,
		ErrorCount:      10,
		ErrorTypes:      map[string]int{"500": 10},
		AvgTTFTMs:       600,
		AvgDurationMs:   2500,
	}, nil)
	scaleProvider.On("GetModelScale", ctx, int64(2)).Return(&providerprofile.ScaleData{
		ProviderID:      20,
		TotalModels:     30,
		AvailableModels: 28,
	}, nil)
	metricsStore.On("SaveSnapshot", ctx, mock.MatchedBy(func(s *providerprofile.MetricSnapshot) bool {
		return s.CredentialID == 2 && s.ProviderID == 20
	})).Return(nil)

	// Create collector
	collector := providerprofile.NewLightweightCollector(
		metricsStore,
		networkProber,
		requestAnalyzer,
		scaleProvider,
		credentialLister,
	)

	// Execute
	err := collector.CollectMetrics(ctx)

	// Assert
	require.NoError(t, err)
	metricsStore.AssertExpectations(t)
	networkProber.AssertExpectations(t)
	requestAnalyzer.AssertExpectations(t)
	scaleProvider.AssertExpectations(t)
	credentialLister.AssertExpectations(t)
}

func TestLightweightCollector_CollectMetrics_NoCredentials(t *testing.T) {
	ctx := context.Background()

	credentialLister := new(MockCredentialLister)
	credentialLister.On("ListActiveCredentials", ctx).Return([]int64{}, nil)

	collector := providerprofile.NewLightweightCollector(
		new(MockMetricsStore),
		new(MockNetworkProber),
		new(MockRequestAnalyzer),
		new(MockScaleProvider),
		credentialLister,
	)

	err := collector.CollectMetrics(ctx)
	assert.NoError(t, err)
}

func TestLightweightCollector_CalculatePercentiles(t *testing.T) {
	ctx := context.Background()

	metricsStore := new(MockMetricsStore)
	networkProber := new(MockNetworkProber)
	requestAnalyzer := new(MockRequestAnalyzer)
	scaleProvider := new(MockScaleProvider)
	credentialLister := new(MockCredentialLister)

	credentialLister.On("ListActiveCredentials", ctx).Return([]int64{1}, nil)

	// Test with various latencies to verify percentile calculation
	latencies := []int{100, 150, 200, 250, 300, 350, 400, 450, 500}
	networkProber.On("ProbeLatency", ctx, int64(1), 3).Return(latencies, nil)

	requestAnalyzer.On("AnalyzeRequests", ctx, int64(1), 2).Return(&providerprofile.RequestStats{
		TotalRequests:   100,
		SuccessRequests: 100,
		ErrorCount:      0,
		ErrorTypes:      map[string]int{},
		AvgTTFTMs:       500,
		AvgDurationMs:   2000,
	}, nil)

	scaleProvider.On("GetModelScale", ctx, int64(1)).Return(&providerprofile.ScaleData{
		ProviderID:      10,
		TotalModels:     10,
		AvailableModels: 10,
	}, nil)

	metricsStore.On("SaveSnapshot", ctx, mock.MatchedBy(func(s *providerprofile.MetricSnapshot) bool {
		// Verify percentile calculation
		// P50 should be around 300 (median)
		// P95 should be around 450-500
		// P99 should be around 500
		return s.NetworkMetrics.P50 >= 250 && s.NetworkMetrics.P50 <= 350 &&
			s.NetworkMetrics.P95 >= 450 && s.NetworkMetrics.P95 <= 500 &&
			s.NetworkMetrics.P99 >= 450 && s.NetworkMetrics.P99 <= 500
	})).Return(nil)

	collector := providerprofile.NewLightweightCollector(
		metricsStore,
		networkProber,
		requestAnalyzer,
		scaleProvider,
		credentialLister,
	)

	err := collector.CollectMetrics(ctx)
	require.NoError(t, err)
	metricsStore.AssertExpectations(t)
}
