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

type MockProfileStore struct {
	mock.Mock
}

func (m *MockProfileStore) SaveDailyProfile(ctx context.Context, profile *providerprofile.DailyProfile) error {
	args := m.Called(ctx, profile)
	return args.Error(0)
}

func (m *MockProfileStore) GetDailyProfile(ctx context.Context, credentialID int64, date time.Time) (*providerprofile.DailyProfile, error) {
	args := m.Called(ctx, credentialID, date)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*providerprofile.DailyProfile), args.Error(1)
}

func (m *MockProfileStore) GetRecentProfiles(ctx context.Context, credentialID int64, days int) ([]*providerprofile.DailyProfile, error) {
	args := m.Called(ctx, credentialID, days)
	return args.Get(0).([]*providerprofile.DailyProfile), args.Error(1)
}

func (m *MockProfileStore) GetProfilesByProvider(ctx context.Context, providerID int64) ([]*providerprofile.DailyProfile, error) {
	args := m.Called(ctx, providerID)
	return args.Get(0).([]*providerprofile.DailyProfile), args.Error(1)
}

type MockScorer struct {
	mock.Mock
}

func (m *MockScorer) CalculateDimensionScores(snapshots []*providerprofile.MetricSnapshot, weights providerprofile.ProfileWeights) *providerprofile.DimensionScores {
	args := m.Called(snapshots, weights)
	return args.Get(0).(*providerprofile.DimensionScores)
}

func (m *MockScorer) CalculateTotalScore(scores *providerprofile.DimensionScores, weights providerprofile.ProfileWeights) float64 {
	args := m.Called(scores, weights)
	return args.Get(0).(float64)
}

func TestDailyAggregator_AggregateDailyProfiles(t *testing.T) {
	ctx := context.Background()
	testDate := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	// Setup mocks
	metricsStore := new(MockMetricsStore)
	profileStore := new(MockProfileStore)
	scorer := new(MockScorer)
	credentialLister := new(MockCredentialLister)

	// Mock credentials
	credentialLister.On("ListActiveCredentials", ctx).Return([]int64{1}, nil)

	// Mock snapshots for credential 1
	startOfDay := testDate
	endOfDay := startOfDay.Add(24 * time.Hour)
	
	snapshots := []*providerprofile.MetricSnapshot{
		{
			CredentialID: 1,
			ProviderID:   10,
			MetricTime:   testDate.Add(2 * time.Hour),
			TimeSlot:     providerprofile.TimeSlotDawn,
			NetworkMetrics: &providerprofile.NetworkMetrics{
				P50: 100,
				P95: 150,
				P99: 200,
			},
			AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
				TotalRequests:   100,
				SuccessRequests: 95,
				AvgTTFTMs:       500,
				AvgDurationMs:   2000,
			},
			StabilityMetrics: &providerprofile.StabilityMetrics{
				ErrorCount: 5,
				ErrorTypes: map[string]int{"400": 5},
			},
			ScaleMetrics: &providerprofile.ScaleMetrics{
				TotalModels:     50,
				AvailableModels: 48,
			},
		},
		{
			CredentialID: 1,
			ProviderID:   10,
			MetricTime:   testDate.Add(8 * time.Hour),
			TimeSlot:     providerprofile.TimeSlotMorning,
			NetworkMetrics: &providerprofile.NetworkMetrics{
				P50: 120,
				P95: 170,
				P99: 220,
			},
			AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
				TotalRequests:   150,
				SuccessRequests: 145,
				AvgTTFTMs:       480,
				AvgDurationMs:   1900,
			},
			StabilityMetrics: &providerprofile.StabilityMetrics{
				ErrorCount: 5,
				ErrorTypes: map[string]int{"400": 5},
			},
			ScaleMetrics: &providerprofile.ScaleMetrics{
				TotalModels:     50,
				AvailableModels: 48,
			},
		},
	}

	metricsStore.On("GetSnapshotsByDateRange", ctx, int64(1), startOfDay, endOfDay).Return(snapshots, nil)

	// Mock scorer
	dimensionScores := &providerprofile.DimensionScores{
		NetworkScore:      95.0,
		AvailabilityScore: 96.0,
		StabilityScore:    98.0,
		ScaleScore:        93.0,
	}
	scorer.On("CalculateDimensionScores", mock.Anything, mock.Anything).Return(dimensionScores)
	scorer.On("CalculateTotalScore", dimensionScores, mock.Anything).Return(95.5)

	// Mock profile store
	profileStore.On("SaveDailyProfile", ctx, mock.MatchedBy(func(p *providerprofile.DailyProfile) bool {
		return p.CredentialID == 1 &&
			p.ProviderID == 10 &&
			p.TotalScore == 95.5 &&
			len(p.TimeslotScores) > 0
	})).Return(nil)

	// Create aggregator
	aggregator := providerprofile.NewDailyAggregator(
		metricsStore,
		profileStore,
		scorer,
		providerprofile.DefaultWeights(),
		credentialLister,
	)

	// Execute
	err := aggregator.AggregateDailyProfiles(ctx, testDate)

	// Assert
	require.NoError(t, err)
	profileStore.AssertExpectations(t)
	metricsStore.AssertExpectations(t)
	credentialLister.AssertExpectations(t)
}

func TestDailyAggregator_TimeslotAnalysis(t *testing.T) {
	ctx := context.Background()
	testDate := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	metricsStore := new(MockMetricsStore)
	profileStore := new(MockProfileStore)
	scorer := providerprofile.NewDefaultScorer() // Use real scorer for this test
	credentialLister := new(MockCredentialLister)

	credentialLister.On("ListActiveCredentials", ctx).Return([]int64{1}, nil)

	// Create snapshots for different timeslots
	startOfDay := testDate
	endOfDay := startOfDay.Add(24 * time.Hour)
	
	snapshots := []*providerprofile.MetricSnapshot{
		// Morning - good performance
		{
			CredentialID: 1,
			ProviderID:   10,
			MetricTime:   testDate.Add(8 * time.Hour),
			TimeSlot:     providerprofile.TimeSlotMorning,
			NetworkMetrics: &providerprofile.NetworkMetrics{P50: 80, P95: 100, P99: 120},
			AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
				TotalRequests:   100,
				SuccessRequests: 99,
				AvgTTFTMs:       400,
				AvgDurationMs:   1500,
			},
			StabilityMetrics: &providerprofile.StabilityMetrics{ErrorCount: 1, ErrorTypes: map[string]int{"400": 1}},
			ScaleMetrics:     &providerprofile.ScaleMetrics{TotalModels: 50, AvailableModels: 50},
		},
		// Afternoon - poor performance
		{
			CredentialID: 1,
			ProviderID:   10,
			MetricTime:   testDate.Add(14 * time.Hour),
			TimeSlot:     providerprofile.TimeSlotAfternoon,
			NetworkMetrics: &providerprofile.NetworkMetrics{P50: 200, P95: 500, P99: 800},
			AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
				TotalRequests:   100,
				SuccessRequests: 85,
				AvgTTFTMs:       1000,
				AvgDurationMs:   8000,
			},
			StabilityMetrics: &providerprofile.StabilityMetrics{ErrorCount: 15, ErrorTypes: map[string]int{"500": 10, "400": 5}},
			ScaleMetrics:     &providerprofile.ScaleMetrics{TotalModels: 50, AvailableModels: 45},
		},
	}

	metricsStore.On("GetSnapshotsByDateRange", ctx, int64(1), startOfDay, endOfDay).Return(snapshots, nil)

	profileStore.On("SaveDailyProfile", ctx, mock.MatchedBy(func(p *providerprofile.DailyProfile) bool {
		// Verify timeslot analysis
		if len(p.TimeslotScores) != 2 {
			return false
		}
		// Morning should have higher score than afternoon
		morningScore := p.TimeslotScores[providerprofile.TimeSlotMorning]
		afternoonScore := p.TimeslotScores[providerprofile.TimeSlotAfternoon]
		
		return morningScore > afternoonScore &&
			p.BestTimeslot == providerprofile.TimeSlotMorning &&
			p.WorstTimeslot == providerprofile.TimeSlotAfternoon &&
			p.ScoreStddev > 0
	})).Return(nil)

	aggregator := providerprofile.NewDailyAggregator(
		metricsStore,
		profileStore,
		scorer,
		providerprofile.DefaultWeights(),
		credentialLister,
	)

	err := aggregator.AggregateDailyProfiles(ctx, testDate)
	require.NoError(t, err)
	profileStore.AssertExpectations(t)
}

func TestDailyAggregator_NoSnapshots(t *testing.T) {
	ctx := context.Background()
	testDate := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	metricsStore := new(MockMetricsStore)
	profileStore := new(MockProfileStore)
	scorer := new(MockScorer)
	credentialLister := new(MockCredentialLister)

	credentialLister.On("ListActiveCredentials", ctx).Return([]int64{1}, nil)

	startOfDay := testDate
	endOfDay := startOfDay.Add(24 * time.Hour)
	
	// No snapshots for this day
	metricsStore.On("GetSnapshotsByDateRange", ctx, int64(1), startOfDay, endOfDay).Return([]*providerprofile.MetricSnapshot{}, nil)

	// Should not call SaveDailyProfile
	aggregator := providerprofile.NewDailyAggregator(
		metricsStore,
		profileStore,
		scorer,
		providerprofile.DefaultWeights(),
		credentialLister,
	)

	err := aggregator.AggregateDailyProfiles(ctx, testDate)
	assert.NoError(t, err)
	
	// Verify SaveDailyProfile was NOT called
	profileStore.AssertNotCalled(t, "SaveDailyProfile")
}
