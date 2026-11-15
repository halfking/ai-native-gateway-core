package autoroute

import (
	"context"
	"testing"
)

// TestTierSelectorV2Compatibility verifies that TierSelector works correctly
// with V2 task types emitted by the production classifier.
//
// CRITICAL: This test validates the P0 fix for vocabulary mismatch between
// V2 runtime task types (code, reasoning, etc.) and V3 config vocabulary
// (coding, architecture, etc.). The fix adds V2 types to TaskTypeTierMapping.
//
// Related: audit report 2026-11-15 Section 5.1 "Vocabulary Mismatch"
func TestTierSelectorV2Compatibility(t *testing.T) {
	ts := NewTierSelector(nil, true) // No DB, use in-memory defaults

	testCases := []struct {
		name           string
		taskType       TaskType
		confidence     float64
		agentDepth     int
		headerTier     string
		expectedTier   string
		expectedSource string
	}{
		// V2 task types (production classifier output)
		{
			name:           "V2: reasoning → tier-a",
			taskType:       TaskReasoning,
			confidence:     0.85,
			expectedTier:   "tier-a",
			expectedSource: "default",
		},
		{
			name:           "V2: code → tier-b",
			taskType:       TaskCode,
			confidence:     0.80,
			expectedTier:   "tier-b",
			expectedSource: "default",
		},
		{
			name:           "V2: creative → tier-c",
			taskType:       TaskCreative,
			confidence:     0.90,
			expectedTier:   "tier-c",
			expectedSource: "default",
		},
		{
			name:           "V2: agent → tier-b",
			taskType:       TaskAgent,
			confidence:     0.75,
			expectedTier:   "tier-b",
			expectedSource: "default",
		},
		{
			name:           "V2: chat → tier-b (safe default)",
			taskType:       TaskChat,
			confidence:     0.70,
			expectedTier:   "tier-b",
			expectedSource: "default",
		},

		// V3 task types (future classifier output)
		{
			name:           "V3: architecture → tier-a",
			taskType:       TaskArchitecture,
			confidence:     0.85,
			expectedTier:   "tier-a",
			expectedSource: "default",
		},
		{
			name:           "V3: coding → tier-b",
			taskType:       TaskCoding,
			confidence:     0.80,
			expectedTier:   "tier-b",
			expectedSource: "default",
		},
		{
			name:           "V3: devops → tier-c",
			taskType:       TaskDevOps,
			confidence:     0.90,
			expectedTier:   "tier-c",
			expectedSource: "default",
		},

		// Priority cascade: Header override (highest)
		{
			name:           "Header override beats default",
			taskType:       TaskCode,
			confidence:     0.80,
			headerTier:     "tier-a",
			expectedTier:   "tier-a",
			expectedSource: "header",
		},
		{
			name:           "Header override: tier-c for reasoning",
			taskType:       TaskReasoning,
			confidence:     0.85,
			headerTier:     "tier-c",
			expectedTier:   "tier-c",
			expectedSource: "header",
		},

		// Priority cascade: Depth degradation
		{
			name:           "Depth=2 forces tier-c (nested sub-agent)",
			taskType:       TaskArchitecture, // Would normally be tier-a
			confidence:     0.95,
			agentDepth:     2,
			expectedTier:   "tier-c",
			expectedSource: "depth",
		},
		{
			name:           "Depth=1 allows normal tier",
			taskType:       TaskArchitecture,
			confidence:     0.95,
			agentDepth:     1,
			expectedTier:   "tier-a",
			expectedSource: "default",
		},
		{
			name:           "Header overrides depth degradation",
			taskType:       TaskArchitecture,
			confidence:     0.95,
			agentDepth:     3, // Would force tier-c
			headerTier:     "tier-a",
			expectedTier:   "tier-a",
			expectedSource: "header",
		},

		// Confidence-based escalation
		{
			name:           "Low confidence escalates to tier-a",
			taskType:       TaskCode,
			confidence:     0.50, // Below threshold (0.75 for code)
			expectedTier:   "tier-a",
			expectedSource: "confidence_escalation",
		},
		{
			name:           "High confidence uses default tier",
			taskType:       TaskCode,
			confidence:     0.85, // Above threshold
			expectedTier:   "tier-b",
			expectedSource: "default",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			input := TierSelectionInput{
				TaskType:   tc.taskType,
				Confidence: tc.confidence,
				AgentDepth: tc.agentDepth,
				HeaderTier: tc.headerTier,
			}

			result, err := ts.SelectTier(context.Background(), input)
			if err != nil {
				t.Fatalf("SelectTier failed: %v", err)
			}

			if result.Tier != tc.expectedTier {
				t.Errorf("Expected tier %q, got %q. Reason: %s",
					tc.expectedTier, result.Tier, result.Reason)
			}

			if result.ConfigSource != tc.expectedSource {
				t.Errorf("Expected source %q, got %q",
					tc.expectedSource, result.ConfigSource)
			}

			// Verify reason is non-empty
			if result.Reason == "" {
				t.Error("Tier selection reason should not be empty")
			}
		})
	}
}

// TestV2TaskTypesHaveConfidenceThresholds verifies that all V2 task types
// have entries in MinConfidenceThresholds to prevent nil map panics.
func TestV2TaskTypesHaveConfidenceThresholds(t *testing.T) {
	v2Types := []TaskType{
		TaskChat, TaskReasoning, TaskCode, TaskAgent,
		TaskCreative, TaskLongContext, TaskVision, TaskFunctionCall,
		TaskCodeAudit, TaskIntentClassification, TaskPlanning,
	}

	for _, taskType := range v2Types {
		threshold, exists := MinConfidenceThresholds[taskType]
		if !exists {
			t.Errorf("V2 task type %q missing from MinConfidenceThresholds", taskType)
		}
		if threshold < 0.5 || threshold > 1.0 {
			t.Errorf("V2 task type %q has invalid threshold %.2f (must be 0.5-1.0)",
				taskType, threshold)
		}
	}
}

// TestV2TaskTypesHaveTierMapping verifies that all V2 task types
// have entries in TaskTypeTierMapping to prevent nil map panics.
func TestV2TaskTypesHaveTierMapping(t *testing.T) {
	v2Types := []TaskType{
		TaskChat, TaskReasoning, TaskCode, TaskAgent,
		TaskCreative, TaskLongContext, TaskVision, TaskFunctionCall,
		TaskCodeAudit, TaskIntentClassification, TaskPlanning,
	}

	validTiers := map[string]bool{
		"tier-a": true,
		"tier-b": true,
		"tier-c": true,
	}

	for _, taskType := range v2Types {
		tier, exists := TaskTypeTierMapping[taskType]
		if !exists {
			t.Errorf("V2 task type %q missing from TaskTypeTierMapping", taskType)
		}
		if !validTiers[tier] {
			t.Errorf("V2 task type %q has invalid tier %q (must be tier-a/b/c)",
				taskType, tier)
		}
	}
}

// TestV3TaskTypesHaveTierMapping verifies that all V3 task types
// are properly configured.
func TestV3TaskTypesHaveTierMapping(t *testing.T) {
	v3Types := []TaskType{
		TaskArchitecture, TaskAudit, TaskDebugging,
		TaskCoding, TaskRefactoring, TaskTesting,
		TaskDevOps, TaskDocumentation, TaskSummary, TaskDependency,
	}

	for _, taskType := range v3Types {
		tier, exists := TaskTypeTierMapping[taskType]
		if !exists {
			t.Errorf("V3 task type %q missing from TaskTypeTierMapping", taskType)
		}
		if tier != "tier-a" && tier != "tier-b" && tier != "tier-c" {
			t.Errorf("V3 task type %q has invalid tier %q", taskType, tier)
		}

		threshold, exists := MinConfidenceThresholds[taskType]
		if !exists {
			t.Errorf("V3 task type %q missing from MinConfidenceThresholds", taskType)
		}
		if threshold < 0.5 || threshold > 1.0 {
			t.Errorf("V3 task type %q has invalid threshold %.2f", taskType, threshold)
		}
	}
}

// TestTierSelectorDisabledWhenV3FlagOff verifies that TierSelector
// returns tier-b when V3 is disabled (safe fallback).
func TestTierSelectorDisabledWhenV3FlagOff(t *testing.T) {
	ts := NewTierSelector(nil, false) // V3 disabled

	input := TierSelectionInput{
		TaskType:   TaskArchitecture, // Would be tier-a if enabled
		Confidence: 0.95,
	}

	result, err := ts.SelectTier(context.Background(), input)
	if err != nil {
		t.Fatalf("SelectTier failed: %v", err)
	}

	if result.Tier != "tier-b" {
		t.Errorf("Expected tier-b when V3 disabled, got %q", result.Tier)
	}

	if result.ConfigSource != "default" {
		t.Errorf("Expected source 'default', got %q", result.ConfigSource)
	}

	if result.Reason != "v3 disabled, using default tier-b" {
		t.Errorf("Unexpected reason: %s", result.Reason)
	}
}

// TestTierSelectorPriorityOrder validates the complete priority cascade:
// header > depth > tenant > global > default
func TestTierSelectorPriorityOrder(t *testing.T) {
	ts := NewTierSelector(nil, true)

	// Test 1: Header beats everything
	result, _ := ts.SelectTier(context.Background(), TierSelectionInput{
		TaskType:   TaskArchitecture, // default tier-a
		Confidence: 0.95,
		AgentDepth: 3, // would force tier-c
		HeaderTier: "tier-b",
	})
	if result.Tier != "tier-b" || result.ConfigSource != "header" {
		t.Errorf("Header should override depth, got tier=%q source=%q",
			result.Tier, result.ConfigSource)
	}

	// Test 2: Depth beats default
	result, _ = ts.SelectTier(context.Background(), TierSelectionInput{
		TaskType:   TaskArchitecture, // default tier-a
		Confidence: 0.95,
		AgentDepth: 2,
	})
	if result.Tier != "tier-c" || result.ConfigSource != "depth" {
		t.Errorf("Depth should override default, got tier=%q source=%q",
			result.Tier, result.ConfigSource)
	}

	// Test 3: Default applies when no overrides
	result, _ = ts.SelectTier(context.Background(), TierSelectionInput{
		TaskType:   TaskArchitecture,
		Confidence: 0.95,
		AgentDepth: 0,
	})
	if result.Tier != "tier-a" || result.ConfigSource != "default" {
		t.Errorf("Default should apply, got tier=%q source=%q",
			result.Tier, result.ConfigSource)
	}
}
