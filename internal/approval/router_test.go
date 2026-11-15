package approval

import (
	"context"
	"testing"
)

func TestClassifySensitivity(t *testing.T) {
	tests := []struct {
		name     string
		config   RouterConfig
		result   *DetectResult
		expected SensitivityLevel
	}{
		{
			name:     "nil result returns low",
			config:   DefaultRouterConfig(),
			result:   nil,
			expected: LevelLow,
		},
		{
			name:     "empty result returns low",
			config:   DefaultRouterConfig(),
			result:   &DetectResult{},
			expected: LevelLow,
		},
		{
			name:   "high score triggers high level",
			config: DefaultRouterConfig(),
			result: &DetectResult{
				SensitivityScore: 0.85,
			},
			expected: LevelHigh,
		},
		{
			name:   "medium score triggers medium level",
			config: DefaultRouterConfig(),
			result: &DetectResult{
				SensitivityScore: 0.6,
			},
			expected: LevelMedium,
		},
		{
			name:   "low score triggers low level",
			config: DefaultRouterConfig(),
			result: &DetectResult{
				SensitivityScore: 0.3,
			},
			expected: LevelLow,
		},
		{
			name:   "force keyword triggers high level",
			config: DefaultRouterConfig(),
			result: &DetectResult{
				Triggers:         []string{"财务数据", "其他"},
				SensitivityScore: 0.3,
			},
			expected: LevelHigh,
		},
		{
			name: "cross department triggers high level when configured",
			config: RouterConfig{
				HighSensitivityThreshold:       0.8,
				RequireCasdoorKeywords:         []string{},
				CrossDepartmentRequiresCasdoor: true,
			},
			result: &DetectResult{
				CrossDepartment:  true,
				SensitivityScore: 0.5,
			},
			expected: LevelHigh,
		},
		{
			name: "cross department ignored when not configured",
			config: RouterConfig{
				HighSensitivityThreshold:       0.8,
				RequireCasdoorKeywords:         []string{},
				CrossDepartmentRequiresCasdoor: false,
			},
			result: &DetectResult{
				CrossDepartment:  true,
				SensitivityScore: 0.5,
			},
			expected: LevelMedium,
		},
		{
			name:   "english keyword triggers high level",
			config: DefaultRouterConfig(),
			result: &DetectResult{
				Triggers:         []string{"salary", "other"},
				SensitivityScore: 0.3,
			},
			expected: LevelHigh,
		},
		{
			name: "custom threshold",
			config: RouterConfig{
				HighSensitivityThreshold: 0.7,
				RequireCasdoorKeywords:   []string{},
			},
			result: &DetectResult{
				SensitivityScore: 0.75,
			},
			expected: LevelHigh,
		},
		{
			name: "boundary case at threshold",
			config: RouterConfig{
				HighSensitivityThreshold: 0.8,
				RequireCasdoorKeywords:   []string{},
			},
			result: &DetectResult{
				SensitivityScore: 0.8,
			},
			expected: LevelHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(tt.config, nil, nil)
			level := router.ClassifySensitivity(tt.result)
			if level != tt.expected {
				t.Errorf("ClassifySensitivity() = %v, want %v", level, tt.expected)
			}
		})
	}
}

func TestSensitivityLevelString(t *testing.T) {
	tests := []struct {
		level    SensitivityLevel
		expected string
	}{
		{LevelLow, "low"},
		{LevelMedium, "medium"},
		{LevelHigh, "high"},
		{SensitivityLevel(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.level.String(); got != tt.expected {
				t.Errorf("SensitivityLevel.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// MockProvider implements Provider interface for testing.
type MockProvider struct {
	name      string
	enabled   bool
	createErr error
	getResult *Approval
	getErr    error
}

func (m *MockProvider) Name() string {
	return m.name
}

func (m *MockProvider) IsEnabled() bool {
	return m.enabled
}

func (m *MockProvider) CreateApproval(ctx context.Context, req *CreateApprovalRequest) (*Approval, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &Approval{
		ID:        req.RequestID,
		SessionID: req.SessionID,
		Status:    "pending",
		Provider:  m.name,
	}, nil
}

func (m *MockProvider) GetApproval(ctx context.Context, requestID string) (*Approval, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}

func (m *MockProvider) CancelApproval(ctx context.Context, requestID string) error {
	return nil
}

func TestRouterRoute(t *testing.T) {
	nativeProvider := &MockProvider{name: "native", enabled: true}

	tests := []struct {
		name             string
		config           RouterConfig
		detectResult     *DetectResult
		casdoorEnabled   bool
		expectedProvider string
		expectedLevel    SensitivityLevel
	}{
		{
			name:   "low sensitivity routes to native",
			config: DefaultRouterConfig(),
			detectResult: &DetectResult{
				SensitivityScore: 0.3,
			},
			casdoorEnabled:   true,
			expectedProvider: "native",
			expectedLevel:    LevelLow,
		},
		{
			name:   "high sensitivity routes to casdoor",
			config: DefaultRouterConfig(),
			detectResult: &DetectResult{
				SensitivityScore: 0.9,
			},
			casdoorEnabled:   true,
			expectedProvider: "casdoor",
			expectedLevel:    LevelHigh,
		},
		{
			name:   "high sensitivity degrades to native when casdoor disabled",
			config: DefaultRouterConfig(),
			detectResult: &DetectResult{
				SensitivityScore: 0.9,
			},
			casdoorEnabled:   false,
			expectedProvider: "native",
			expectedLevel:    LevelHigh,
		},
		{
			name:   "force keyword routes to casdoor",
			config: DefaultRouterConfig(),
			detectResult: &DetectResult{
				Triggers:         []string{"financial_data"},
				SensitivityScore: 0.2,
			},
			casdoorEnabled:   true,
			expectedProvider: "casdoor",
			expectedLevel:    LevelHigh,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			casdoor := &MockProvider{name: "casdoor", enabled: tt.casdoorEnabled}
			router := NewRouter(tt.config, nativeProvider, casdoor)

			req := &CreateApprovalRequest{
				DetectResult: tt.detectResult,
			}

			provider, level := router.Route(req)

			if provider.Name() != tt.expectedProvider {
				t.Errorf("Route() provider = %v, want %v", provider.Name(), tt.expectedProvider)
			}
			if level != tt.expectedLevel {
				t.Errorf("Route() level = %v, want %v", level, tt.expectedLevel)
			}
		})
	}
}

func TestRouterGetApproval(t *testing.T) {
	nativeProvider := &MockProvider{
		name:    "native",
		enabled: true,
		getResult: &Approval{
			ID:       "req-123",
			Status:   "approved",
			Provider: "native",
		},
	}

	casdoorProvider := &MockProvider{
		name:    "casdoor",
		enabled: true,
		getResult: &Approval{
			ID:       "req-456",
			Status:   "pending",
			Provider: "casdoor",
		},
	}

	router := NewRouter(DefaultRouterConfig(), nativeProvider, casdoorProvider)

	t.Run("finds approval in native provider", func(t *testing.T) {
		approval, err := router.GetApproval(context.Background(), "req-123")
		if err != nil {
			t.Fatalf("GetApproval() error = %v", err)
		}
		if approval == nil {
			t.Fatal("GetApproval() returned nil")
		}
		if approval.Provider != "native" {
			t.Errorf("GetApproval() provider = %v, want native", approval.Provider)
		}
	})

	t.Run("falls back to casdoor provider", func(t *testing.T) {
		nativeProvider.getResult = nil
		approval, err := router.GetApproval(context.Background(), "req-456")
		if err != nil {
			t.Fatalf("GetApproval() error = %v", err)
		}
		if approval == nil {
			t.Fatal("GetApproval() returned nil")
		}
		if approval.Provider != "casdoor" {
			t.Errorf("GetApproval() provider = %v, want casdoor", approval.Provider)
		}
	})

	t.Run("returns error when not found", func(t *testing.T) {
		nativeProvider.getResult = nil
		casdoorProvider.getResult = nil
		_, err := router.GetApproval(context.Background(), "req-999")
		if err == nil {
			t.Error("GetApproval() expected error, got nil")
		}
	})
}
