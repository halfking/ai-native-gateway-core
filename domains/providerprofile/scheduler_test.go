package providerprofile_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type MockCollector struct {
	mock.Mock
}

func (m *MockCollector) CollectMetrics(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type MockAggregator struct {
	mock.Mock
}

func (m *MockAggregator) AggregateDailyProfiles(ctx context.Context, date time.Time) error {
	args := m.Called(ctx, date)
	return args.Error(0)
}

type MockCronScheduler struct {
	mock.Mock
}

func (m *MockCronScheduler) AddJob(name string, cronExpr string, handler func(context.Context) error) error {
	args := m.Called(name, cronExpr, handler)
	return args.Error(0)
}

func TestJobScheduler_RunLightweightCollection(t *testing.T) {
	ctx := context.Background()
	
	collector := new(MockCollector)
	aggregator := new(MockAggregator)
	metricsStore := new(MockMetricsStore)
	
	collector.On("CollectMetrics", ctx).Return(nil)
	
	scheduler := providerprofile.NewJobScheduler(collector, aggregator, metricsStore)
	
	err := scheduler.RunLightweightCollection(ctx)
	require.NoError(t, err)
	collector.AssertExpectations(t)
}

func TestJobScheduler_RunLightweightCollection_Error(t *testing.T) {
	ctx := context.Background()
	
	collector := new(MockCollector)
	aggregator := new(MockAggregator)
	metricsStore := new(MockMetricsStore)
	
	collector.On("CollectMetrics", ctx).Return(errors.New("collection failed"))
	
	scheduler := providerprofile.NewJobScheduler(collector, aggregator, metricsStore)
	
	err := scheduler.RunLightweightCollection(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "lightweight collection failed")
}

func TestJobScheduler_RunDailyAggregation(t *testing.T) {
	ctx := context.Background()
	
	collector := new(MockCollector)
	aggregator := new(MockAggregator)
	metricsStore := new(MockMetricsStore)
	
	// Should aggregate yesterday's data
	aggregator.On("AggregateDailyProfiles", ctx, mock.MatchedBy(func(date time.Time) bool {
		yesterday := time.Now().AddDate(0, 0, -1)
		return date.Year() == yesterday.Year() &&
			date.Month() == yesterday.Month() &&
			date.Day() == yesterday.Day()
	})).Return(nil)
	
	scheduler := providerprofile.NewJobScheduler(collector, aggregator, metricsStore)
	
	err := scheduler.RunDailyAggregation(ctx)
	require.NoError(t, err)
	aggregator.AssertExpectations(t)
}

func TestJobScheduler_RunCleanup(t *testing.T) {
	ctx := context.Background()
	
	collector := new(MockCollector)
	aggregator := new(MockAggregator)
	metricsStore := new(MockMetricsStore)
	
	metricsStore.On("CleanupOldMetrics", ctx).Return(int64(150), nil)
	
	scheduler := providerprofile.NewJobScheduler(collector, aggregator, metricsStore)
	
	err := scheduler.RunCleanup(ctx)
	require.NoError(t, err)
	metricsStore.AssertExpectations(t)
}

func TestJobRegistry_RegisterAllJobs(t *testing.T) {
	collector := new(MockCollector)
	aggregator := new(MockAggregator)
	metricsStore := new(MockMetricsStore)
	
	jobScheduler := providerprofile.NewJobScheduler(collector, aggregator, metricsStore)
	config := providerprofile.DefaultScheduleConfig()
	registry := providerprofile.NewJobRegistry(*jobScheduler, config)
	
	cronScheduler := new(MockCronScheduler)
	
	// Expect 3 jobs to be registered
	cronScheduler.On("AddJob", "profile:collect:lightweight", "0 */2 * * *", mock.Anything).Return(nil)
	cronScheduler.On("AddJob", "profile:aggregate:daily", "0 4 * * *", mock.Anything).Return(nil)
	cronScheduler.On("AddJob", "profile:cleanup", "0 5 * * 0", mock.Anything).Return(nil)
	
	err := registry.RegisterAllJobs(cronScheduler)
	require.NoError(t, err)
	cronScheduler.AssertExpectations(t)
}

func TestJobRegistry_RegisterAllJobs_Error(t *testing.T) {
	collector := new(MockCollector)
	aggregator := new(MockAggregator)
	metricsStore := new(MockMetricsStore)
	
	jobScheduler := providerprofile.NewJobScheduler(collector, aggregator, metricsStore)
	config := providerprofile.DefaultScheduleConfig()
	registry := providerprofile.NewJobRegistry(*jobScheduler, config)
	
	cronScheduler := new(MockCronScheduler)
	
	// First job fails
	cronScheduler.On("AddJob", "profile:collect:lightweight", "0 */2 * * *", mock.Anything).Return(errors.New("scheduler error"))
	
	err := registry.RegisterAllJobs(cronScheduler)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "register lightweight collection job")
}

func TestDefaultScheduleConfig(t *testing.T) {
	config := providerprofile.DefaultScheduleConfig()
	
	assert.Equal(t, "0 */2 * * *", config.LightweightCollectionCron)
	assert.Equal(t, "0 4 * * *", config.DailyAggregationCron)
	assert.Equal(t, "0 5 * * 0", config.CleanupCron)
}
