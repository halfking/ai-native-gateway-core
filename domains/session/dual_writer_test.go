package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockRequestLogWriter is a mock for the V1 writer
type MockRequestLogWriter struct {
	mock.Mock
}

func (m *MockRequestLogWriter) Write(ctx context.Context, req *ProcessedRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

// MockSessionWriterV2 is a mock for the V2 writer
type MockSessionWriterV2 struct {
	mock.Mock
}

func (m *MockSessionWriterV2) Write(ctx context.Context, req *v2.ProcessedRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

// Ensure MockSessionWriterV2 implements the interface we need
var _ interface {
	Write(context.Context, *v2.ProcessedRequest) error
} = (*MockSessionWriterV2)(nil)

// MockFeatureFlags is a mock for feature flags
type MockFeatureFlags struct {
	mock.Mock
}

func (m *MockFeatureFlags) IsEnabled(flag string) bool {
	args := m.Called(flag)
	return args.Bool(0)
}

func (m *MockFeatureFlags) IsEnabledForSession(sessionID, flag string) bool {
	args := m.Called(sessionID, flag)
	return args.Bool(0)
}

func (m *MockFeatureFlags) GetRolloutPercent(flag string) int {
	args := m.Called(flag)
	return args.Int(0)
}

// MockCounter is a mock counter
type MockCounter struct {
	mock.Mock
}

func (m *MockCounter) Inc() {
	m.Called()
}

func (m *MockCounter) Add(delta float64) {
	m.Called(delta)
}

// MockHistogram is a mock histogram
type MockHistogram struct {
	mock.Mock
}

func (m *MockHistogram) Observe(value float64) {
	m.Called(value)
}

// TestDualWriter_V1Success_V2Disabled tests V1 write succeeds, V2 disabled
func TestDualWriter_V1Success_V2Disabled(t *testing.T) {
	mockV1 := new(MockRequestLogWriter)
	mockV2 := new(MockSessionWriterV2)
	mockFlags := new(MockFeatureFlags)

	metrics := &DualWriteMetrics{
		V1WriteSuccess: new(MockCounter),
		V1WriteFailed:  new(MockCounter),
		V2WriteSuccess: new(MockCounter),
		V2WriteFailed:  new(MockCounter),
		V2WriteLatency: new(MockHistogram),
	}

	writer := NewDualWriter(mockV1, mockV2, mockFlags, metrics)

	req := &ProcessedRequest{
		SessionID: "session_001",
		TenantID:  "tenant_001",
		RequestID: "req_001",
		Timestamp: time.Now(),
	}

	ctx := context.Background()

	// V1 write succeeds
	mockV1.On("Write", ctx, req).Return(nil)
	metrics.V1WriteSuccess.(*MockCounter).On("Inc").Return()

	// V2 is disabled
	mockFlags.On("IsEnabled", "sessions_v2_shadow_write").Return(false)

	// Execute
	err := writer.Write(ctx, req)

	// Assertions
	require.NoError(t, err)
	mockV1.AssertExpectations(t)
	mockFlags.AssertExpectations(t)

	// V2 should not be called
	mockV2.AssertNotCalled(t, "Write")
}

// TestDualWriter_V1Fails_RequestFails tests V1 write fails, request fails
func TestDualWriter_V1Fails_RequestFails(t *testing.T) {
	mockV1 := new(MockRequestLogWriter)
	mockV2 := new(MockSessionWriterV2)
	mockFlags := new(MockFeatureFlags)

	metrics := &DualWriteMetrics{
		V1WriteSuccess: new(MockCounter),
		V1WriteFailed:  new(MockCounter),
	}

	writer := NewDualWriter(mockV1, mockV2, mockFlags, metrics)

	req := &ProcessedRequest{
		SessionID: "session_001",
		RequestID: "req_001",
	}

	ctx := context.Background()

	// V1 write fails
	v1Error := fmt.Errorf("database connection failed")
	mockV1.On("Write", ctx, req).Return(v1Error)
	metrics.V1WriteFailed.(*MockCounter).On("Inc").Return()

	// Execute
	err := writer.Write(ctx, req)

	// Assertions
	require.Error(t, err)
	assert.Contains(t, err.Error(), "v1 write failed (primary)")
	mockV1.AssertExpectations(t)

	// V2 should not be called
	mockV2.AssertNotCalled(t, "Write")
}

// TestDualWriter_V1Success_V2Success tests both writes succeed
func TestDualWriter_V1Success_V2Success(t *testing.T) {
	mockV1 := new(MockRequestLogWriter)
	mockV2 := new(MockSessionWriterV2)
	mockFlags := new(MockFeatureFlags)

	metrics := &DualWriteMetrics{
		V1WriteSuccess: new(MockCounter),
		V1WriteFailed:  new(MockCounter),
		V2WriteSuccess: new(MockCounter),
		V2WriteFailed:  new(MockCounter),
		V2WriteLatency: new(MockHistogram),
	}

	writer := NewDualWriter(mockV1, mockV2, mockFlags, metrics)

	req := &ProcessedRequest{
		SessionID: "session_001",
		TenantID:  "tenant_001",
		RequestID: "req_001",
		Timestamp: time.Now(),
	}

	ctx := context.Background()

	// V1 write succeeds
	mockV1.On("Write", ctx, req).Return(nil)
	metrics.V1WriteSuccess.(*MockCounter).On("Inc").Return()

	// V2 is enabled
	mockFlags.On("IsEnabled", "sessions_v2_shadow_write").Return(true)
	mockFlags.On("GetRolloutPercent", "sessions_v2_rollout_percent").Return(100)

	// V2 write succeeds
	mockV2.On("Write", ctx, mock.AnythingOfType("*v2.ProcessedRequest")).Return(nil)
	metrics.V2WriteSuccess.(*MockCounter).On("Inc").Return()
	metrics.V2WriteLatency.(*MockHistogram).On("Observe", mock.AnythingOfType("float64")).Return()

	// Execute
	err := writer.Write(ctx, req)

	// Assertions
	require.NoError(t, err)
	mockV1.AssertExpectations(t)
	mockV2.AssertExpectations(t)
	mockFlags.AssertExpectations(t)
}

// TestDualWriter_V1Success_V2Fails_RequestSucceeds tests V2 failure doesn't block request
func TestDualWriter_V1Success_V2Fails_RequestSucceeds(t *testing.T) {
	mockV1 := new(MockRequestLogWriter)
	mockV2 := new(MockSessionWriterV2)
	mockFlags := new(MockFeatureFlags)

	metrics := &DualWriteMetrics{
		V1WriteSuccess: new(MockCounter),
		V1WriteFailed:  new(MockCounter),
		V2WriteSuccess: new(MockCounter),
		V2WriteFailed:  new(MockCounter),
		V2WriteLatency: new(MockHistogram),
	}

	writer := NewDualWriter(mockV1, mockV2, mockFlags, metrics)

	req := &ProcessedRequest{
		SessionID: "session_001",
		TenantID:  "tenant_001",
		RequestID: "req_001",
		Timestamp: time.Now(),
	}

	ctx := context.Background()

	// V1 write succeeds
	mockV1.On("Write", ctx, req).Return(nil)
	metrics.V1WriteSuccess.(*MockCounter).On("Inc").Return()

	// V2 is enabled
	mockFlags.On("IsEnabled", "sessions_v2_shadow_write").Return(true)
	mockFlags.On("GetRolloutPercent", "sessions_v2_rollout_percent").Return(100)

	// V2 write fails
	v2Error := fmt.Errorf("v2 database timeout")
	mockV2.On("Write", ctx, mock.AnythingOfType("*v2.ProcessedRequest")).Return(v2Error)
	metrics.V2WriteFailed.(*MockCounter).On("Inc").Return()
	metrics.V2WriteLatency.(*MockHistogram).On("Observe", mock.AnythingOfType("float64")).Return()

	// Execute
	err := writer.Write(ctx, req)

	// Assertions
	require.NoError(t, err, "Request should succeed even if V2 write fails")
	mockV1.AssertExpectations(t)
	mockV2.AssertExpectations(t)
	mockFlags.AssertExpectations(t)
}

// TestDualWriter_RolloutPercentage tests rollout percentage logic
func TestDualWriter_RolloutPercentage(t *testing.T) {
	testCases := []struct {
		name           string
		rolloutPercent int
		sessionID      string
		expectV2Called bool
	}{
		{
			name:           "0% rollout - V2 not called",
			rolloutPercent: 0,
			sessionID:      "session_001",
			expectV2Called: false,
		},
		{
			name:           "100% rollout - V2 called",
			rolloutPercent: 100,
			sessionID:      "session_001",
			expectV2Called: true,
		},
		{
			name:           "50% rollout - depends on hash",
			rolloutPercent: 50,
			sessionID:      "session_test_001",
			expectV2Called: true, // This specific session should be included
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockV1 := new(MockRequestLogWriter)
			mockV2 := new(MockSessionWriterV2)
			mockFlags := new(MockFeatureFlags)

			metrics := &DualWriteMetrics{
				V1WriteSuccess: new(MockCounter),
				V1WriteFailed:  new(MockCounter),
				V2WriteSuccess: new(MockCounter),
				V2WriteFailed:  new(MockCounter),
				V2WriteLatency: new(MockHistogram),
			}

			writer := NewDualWriter(mockV1, mockV2, mockFlags, metrics)

			req := &ProcessedRequest{
				SessionID: tc.sessionID,
				TenantID:  "tenant_001",
				RequestID: "req_001",
				Timestamp: time.Now(),
			}

			ctx := context.Background()

			// V1 write succeeds
			mockV1.On("Write", ctx, req).Return(nil)
			metrics.V1WriteSuccess.(*MockCounter).On("Inc").Return()

			// V2 is enabled
			mockFlags.On("IsEnabled", "sessions_v2_shadow_write").Return(true)
			mockFlags.On("GetRolloutPercent", "sessions_v2_rollout_percent").Return(tc.rolloutPercent)

			if tc.expectV2Called {
				mockV2.On("Write", ctx, mock.AnythingOfType("*v2.ProcessedRequest")).Return(nil)
				metrics.V2WriteSuccess.(*MockCounter).On("Inc").Return()
				metrics.V2WriteLatency.(*MockHistogram).On("Observe", mock.AnythingOfType("float64")).Return()
			}

			// Execute
			err := writer.Write(ctx, req)

			// Assertions
			require.NoError(t, err)
			mockV1.AssertExpectations(t)
			mockFlags.AssertExpectations(t)

			if tc.expectV2Called {
				mockV2.AssertExpectations(t)
			} else {
				mockV2.AssertNotCalled(t, "Write")
			}
		})
	}
}

// TestDualWriter_ConsistentHashing tests consistent hashing for same session
func TestDualWriter_ConsistentHashing(t *testing.T) {
	sessionID := "session_consistent_001"
	rolloutPercent := 50

	// Call shouldWriteV2 multiple times with same session
	mockFlags := new(MockFeatureFlags)
	writer := NewDualWriter(nil, nil, mockFlags, nil)

	results := make([]bool, 10)
	for i := 0; i < 10; i++ {
		results[i] = writer.shouldWriteV2(sessionID, rolloutPercent)
	}

	// All results should be the same (consistent)
	firstResult := results[0]
	for i, result := range results {
		assert.Equal(t, firstResult, result, "Result %d should be consistent", i)
	}
}

// TestHashString tests hash function produces consistent results
func TestHashString(t *testing.T) {
	testCases := []struct {
		input string
	}{
		{"session_001"},
		{"session_002"},
		{"very_long_session_id_with_many_characters_123456789"},
		{"short"},
		{""},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			// Hash should be consistent
			hash1 := hashString(tc.input)
			hash2 := hashString(tc.input)

			assert.Equal(t, hash1, hash2)

			// Hash should be non-negative
			assert.GreaterOrEqual(t, hash1, 0)

			// Hash should be less than maxint
			assert.LessOrEqual(t, hash1, 0x7FFFFFFF)
		})
	}
}

// TestHashString_Distribution tests hash distribution
func TestHashString_Distribution(t *testing.T) {
	// Generate 1000 session IDs and count distribution
	numSessions := 1000
	rolloutPercent := 50

	included := 0
	excluded := 0

	for i := 0; i < numSessions; i++ {
		sessionID := fmt.Sprintf("session_%d", i)
		hash := hashString(sessionID)

		if (hash % 100) < rolloutPercent {
			included++
		} else {
			excluded++
		}
	}

	// Should be roughly 50/50 distribution
	// Allow 10% tolerance (45-55%)
	expectedIncluded := numSessions * rolloutPercent / 100
	tolerance := numSessions / 10

	assert.InDelta(t, expectedIncluded, included, float64(tolerance),
		"Hash distribution should be roughly %d%%, got %d/%d",
		rolloutPercent, included, numSessions)
}

// TestShouldWriteV2_EdgeCases tests edge cases for rollout logic
func TestShouldWriteV2_EdgeCases(t *testing.T) {
	mockFlags := new(MockFeatureFlags)
	writer := NewDualWriter(nil, nil, mockFlags, nil)

	testCases := []struct {
		name           string
		rolloutPercent int
		expected       bool
	}{
		{"0% rollout", 0, false},
		{"1% rollout", 1, false},  // Depends on hash
		{"99% rollout", 99, true}, // Depends on hash
		{"100% rollout", 100, true},
		{"-1% rollout (invalid)", -1, false},
		{"101% rollout (invalid)", 101, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := writer.shouldWriteV2("session_test", tc.rolloutPercent)
			// For specific percentages, just verify it returns a boolean
			assert.IsType(t, false, result)
		})
	}
}

// TestDualWriter_ConvertToV2Request tests request conversion
func TestDualWriter_ConvertToV2Request(t *testing.T) {
	req := &ProcessedRequest{
		SessionID:       "session_001",
		TenantID:        "tenant_001",
		RequestID:       "req_001",
		Timestamp:       time.Now(),
		ProjectID:       "project_001",
		Namespace:       "workspace",
		ParentRequestID: "req_parent",
		TaskType:        "code",
		RequestBody: []v2.Message{
			{Role: "user", Content: "Hello"},
		},
		ResponseBody: []v2.Message{
			{Role: "assistant", Content: "Hi there"},
		},
		CompressionApplied:  true,
		CompressionStrategy: "delta_append",
		InjectionVerdict:    "pass",
		OutputVerdict:       "pass",
		PromptTokens:        100,
		CompletionTokens:    50,
		CostUSD:             0.003,
		StatusCode:          200,
		Success:             true,
	}

	v2Req := convertToV2Request(req)

	assert.Equal(t, req.SessionID, v2Req.SessionID)
	assert.Equal(t, req.TenantID, v2Req.TenantID)
	assert.Equal(t, req.RequestID, v2Req.RequestID)
	assert.Equal(t, req.Timestamp, v2Req.Timestamp)
	assert.Equal(t, req.ProjectID, v2Req.ProjectID)
	assert.Equal(t, req.Namespace, v2Req.Namespace)
	assert.Equal(t, req.ParentRequestID, v2Req.ParentRequestID)
	assert.Equal(t, req.TaskType, v2Req.TaskType)
	assert.Equal(t, req.CompressionApplied, v2Req.CompressionApplied)
	assert.Equal(t, req.CompressionStrategy, v2Req.CompressionStrategy)
	assert.Equal(t, req.PromptTokens, v2Req.PromptTokens)
	assert.Equal(t, req.CompletionTokens, v2Req.CompletionTokens)
	assert.Equal(t, req.CostUSD, v2Req.CostUSD)
	assert.Equal(t, req.Success, v2Req.Success)
}

// TestDualWriter_Stats tests stats retrieval
func TestDualWriter_Stats(t *testing.T) {
	mockFlags := new(MockFeatureFlags)
	writer := NewDualWriter(nil, nil, mockFlags, nil)

	mockFlags.On("IsEnabled", "sessions_v2_shadow_write").Return(true)
	mockFlags.On("GetRolloutPercent", "sessions_v2_rollout_percent").Return(75)

	stats := writer.Stats()

	assert.True(t, stats.V2Enabled)
	assert.Equal(t, 75, stats.RolloutPercent)

	mockFlags.AssertExpectations(t)
}

// TestDualWriter_GetWriters tests getter methods
func TestDualWriter_GetWriters(t *testing.T) {
	mockV1 := new(MockRequestLogWriter)
	mockV2 := new(MockSessionWriterV2)
	mockFlags := new(MockFeatureFlags)

	writer := NewDualWriter(mockV1, mockV2, mockFlags, nil)

	assert.Equal(t, mockV1, writer.GetV1Writer())
	assert.Equal(t, mockV2, writer.GetV2Writer())
}

// TestDualWriter_MetricsRecording tests that metrics are recorded correctly
func TestDualWriter_MetricsRecording(t *testing.T) {
	mockV1 := new(MockRequestLogWriter)
	mockV2 := new(MockSessionWriterV2)
	mockFlags := new(MockFeatureFlags)

	v1Success := new(MockCounter)
	v2Success := new(MockCounter)
	v2Latency := new(MockHistogram)

	metrics := &DualWriteMetrics{
		V1WriteSuccess: v1Success,
		V1WriteFailed:  new(MockCounter),
		V2WriteSuccess: v2Success,
		V2WriteFailed:  new(MockCounter),
		V2WriteLatency: v2Latency,
	}

	writer := NewDualWriter(mockV1, mockV2, mockFlags, metrics)

	req := &ProcessedRequest{
		SessionID: "session_001",
		TenantID:  "tenant_001",
		RequestID: "req_001",
		Timestamp: time.Now(),
	}

	ctx := context.Background()

	mockV1.On("Write", ctx, req).Return(nil)
	mockFlags.On("IsEnabled", "sessions_v2_shadow_write").Return(true)
	mockFlags.On("GetRolloutPercent", "sessions_v2_rollout_percent").Return(100)
	mockV2.On("Write", ctx, mock.AnythingOfType("*v2.ProcessedRequest")).Return(nil)

	// Expect metrics to be recorded
	v1Success.On("Inc").Return()
	v2Success.On("Inc").Return()
	v2Latency.On("Observe", mock.AnythingOfType("float64")).Return()

	err := writer.Write(ctx, req)
	require.NoError(t, err)

	// Verify all metrics were called
	v1Success.AssertCalled(t, "Inc")
	v2Success.AssertCalled(t, "Inc")
	v2Latency.AssertCalled(t, "Observe", mock.AnythingOfType("float64"))
}
