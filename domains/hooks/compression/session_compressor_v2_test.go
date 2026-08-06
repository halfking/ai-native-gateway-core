package compression

import (
	"context"
	"errors"
	"testing"
)

// MockCacheV2 implements CacheV2 interface for testing
type MockCacheV2 struct {
	getFunc func(ctx context.Context, tenantID, sessionID string) (interface{}, error)
}

func (m *MockCacheV2) Get(ctx context.Context, tenantID, sessionID string) (interface{}, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, tenantID, sessionID)
	}
	return nil, nil
}

// MockBuilder implements Builder interface for testing
type MockBuilder struct {
	buildFunc func(ctx context.Context, tenantID, sessionID string) ([]byte, interface{}, error)
}

func (m *MockBuilder) BuildFromLatestOutbound(ctx context.Context, tenantID, sessionID string) ([]byte, interface{}, error) {
	if m.buildFunc != nil {
		return m.buildFunc(ctx, tenantID, sessionID)
	}
	return nil, nil, nil
}

// TestSessionCompressor_ShouldUseV2 tests the shouldUseV2 decision logic
func TestSessionCompressor_ShouldUseV2(t *testing.T) {
	tests := []struct {
		name     string
		deps     SessionCompressorDeps
		tenantID string
		expected bool
	}{
		{
			name: "v2 components nil",
			deps: SessionCompressorDeps{
				Cache:   &SessionCache{},
				CacheV2: nil,
				Builder: nil,
			},
			tenantID: "test-tenant",
			expected: false,
		},
		{
			name: "cache v2 nil",
			deps: SessionCompressorDeps{
				CacheV2: nil,
				Builder: &MockBuilder{},
			},
			tenantID: "test-tenant",
			expected: false,
		},
		{
			name: "builder nil",
			deps: SessionCompressorDeps{
				CacheV2: &MockCacheV2{},
				Builder: nil,
			},
			tenantID: "test-tenant",
			expected: false,
		},
		{
			name: "v2 components available but feature flag disabled",
			deps: SessionCompressorDeps{
				CacheV2: &MockCacheV2{},
				Builder: &MockBuilder{},
			},
			tenantID: "test-tenant",
			expected: false, // Feature flag is disabled by default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &SessionCompressor{deps: tt.deps}
			result := sc.shouldUseV2(tt.tenantID)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestSessionCompressor_TryLoadV2State tests V2 state loading
func TestSessionCompressor_TryLoadV2State(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		cacheV2      *MockCacheV2
		builder      *MockBuilder
		expectedBody []byte
		expectedOK   bool
		expectError  bool
	}{
		{
			name: "successful load",
			cacheV2: &MockCacheV2{
				getFunc: func(ctx context.Context, tenantID, sessionID string) (interface{}, error) {
					return map[string]interface{}{"data": "test"}, nil
				},
			},
			builder: &MockBuilder{
				buildFunc: func(ctx context.Context, tenantID, sessionID string) ([]byte, interface{}, error) {
					return []byte(`[{"role":"user","content":"test"}]`), nil, nil
				},
			},
			expectedBody: []byte(`[{"role":"user","content":"test"}]`),
			expectedOK:   true,
			expectError:  false,
		},
		{
			name: "cache get error",
			cacheV2: &MockCacheV2{
				getFunc: func(ctx context.Context, tenantID, sessionID string) (interface{}, error) {
					return nil, errors.New("cache error")
				},
			},
			builder:      &MockBuilder{},
			expectedBody: nil,
			expectedOK:   false,
			expectError:  false, // Should not error, just fallback
		},
		{
			name: "builder error",
			cacheV2: &MockCacheV2{
				getFunc: func(ctx context.Context, tenantID, sessionID string) (interface{}, error) {
					return map[string]interface{}{"data": "test"}, nil
				},
			},
			builder: &MockBuilder{
				buildFunc: func(ctx context.Context, tenantID, sessionID string) ([]byte, interface{}, error) {
					return nil, nil, errors.New("builder error")
				},
			},
			expectedBody: nil,
			expectedOK:   false,
			expectError:  false, // Should not error, just fallback
		},
		{
			name: "new session (nil state)",
			cacheV2: &MockCacheV2{
				getFunc: func(ctx context.Context, tenantID, sessionID string) (interface{}, error) {
					return nil, nil
				},
			},
			builder:      &MockBuilder{},
			expectedBody: nil,
			expectedOK:   true, // ok=true for new session
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := &SessionCompressor{
				deps: SessionCompressorDeps{
					CacheV2: tt.cacheV2,
					Builder: tt.builder,
				},
			}

			body, ok := sc.tryLoadV2State(ctx, "test-tenant", "test-session")

			if ok != tt.expectedOK {
				t.Errorf("expected ok=%v, got ok=%v", tt.expectedOK, ok)
			}

			if string(body) != string(tt.expectedBody) {
				t.Errorf("expected body=%s, got body=%s", tt.expectedBody, body)
			}
		})
	}
}

// TestSessionCompressor_V2Integration tests that V2 components work together
func TestSessionCompressor_V2Integration(t *testing.T) {
	// This is a basic integration test
	// More comprehensive tests would require database/redis setup

	ctx := context.Background()

	cacheV2 := &MockCacheV2{
		getFunc: func(ctx context.Context, tenantID, sessionID string) (interface{}, error) {
			return map[string]interface{}{"turn_no": 1}, nil
		},
	}

	builder := &MockBuilder{
		buildFunc: func(ctx context.Context, tenantID, sessionID string) ([]byte, interface{}, error) {
			return []byte(`[{"role":"user","content":"hello"}]`), nil, nil
		},
	}

	sc := &SessionCompressor{
		deps: SessionCompressorDeps{
			CacheV2: cacheV2,
			Builder: builder,
		},
	}

	// Test that components are properly integrated
	if sc.deps.CacheV2 == nil {
		t.Error("CacheV2 should not be nil")
	}

	if sc.deps.Builder == nil {
		t.Error("Builder should not be nil")
	}

	// Test tryLoadV2State
	body, ok := sc.tryLoadV2State(ctx, "test-tenant", "test-session")
	if !ok {
		t.Error("expected ok=true")
	}

	if len(body) == 0 {
		t.Error("expected non-empty body")
	}
}
