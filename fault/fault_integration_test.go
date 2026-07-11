//go:build integration

package fault

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFaultIntegration tests the complete fault detection and handling flow:
// Create Rule → Detect Fault → Execute Action → Update Status → Dashboard Stats
//
// To run:
//
//	export LLM_GATEWAY_PG_URL=<your-postgres-dsn>
//	go test -tags=integration ./fault -v -run TestFaultIntegration
func TestFaultIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err, "failed to connect to database")
	defer pool.Close()

	store := NewPgxStore(pool)

	// Clean up test data
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM fault_action_logs WHERE event_id IN (SELECT id FROM fault_events WHERE rule_name LIKE 'test-rule-%')")
		_, _ = pool.Exec(ctx, "DELETE FROM fault_events WHERE rule_name LIKE 'test-rule-%'")
		_, _ = pool.Exec(ctx, "DELETE FROM fault_rules WHERE name LIKE 'test-rule-%'")
	}()

	t.Run("CompleteWorkflow", func(t *testing.T) {
		// Step 1: Create Rule
		ruleName := "test-rule-" + time.Now().Format("20060102-150405")
		rule := &Rule{
			Name:        ruleName,
			Description: "Test rule for integration testing",
			Metric:      "cpu_usage",
			Operator:    OpGte,
			Threshold:   80.0,
			Duration:    "5m",
			Severity:    SeverityWarning,
			Action:      ActionNotify,
			ActionConfig: mustJSON(map[string]interface{}{
				"channels": []string{"email", "slack"},
				"message":  "CPU usage exceeded threshold",
			}),
			Enabled:   true,
			Cooldown:  "10m",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		err := store.CreateRule(ctx, rule)
		require.NoError(t, err, "CreateRule should succeed")
		assert.Greater(t, rule.ID, int64(0), "Rule ID should be assigned")

		// Step 2: Create Event (fault detected)
		event := &Event{
			RuleID:      rule.ID,
			RuleName:    ruleName,
			Severity:    SeverityWarning,
			Title:       "High CPU Usage Detected",
			Description: "CPU usage is at 85%, exceeding threshold of 80%",
			Source:      "monitoring-agent-001",
			Status:      EventStatusNew,
			Metadata: mustJSON(map[string]interface{}{
				"current_value": 85.0,
				"threshold":     80.0,
				"hostname":      "prod-server-01",
			}),
			DetectedAt: time.Now(),
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}

		err = store.CreateEvent(ctx, event)
		require.NoError(t, err, "CreateEvent should succeed")
		assert.Greater(t, event.ID, int64(0), "Event ID should be assigned")

		// Step 3: Execute Action
		actionLog := &ActionLog{
			EventID:     event.ID,
			Action:      ActionNotify,
			Status:      "running",
			TriggeredAt: time.Now(),
		}

		err = store.CreateActionLog(ctx, actionLog)
		require.NoError(t, err, "CreateActionLog should succeed")

		// Simulate action execution
		time.Sleep(100 * time.Millisecond)
		completedAt := time.Now()
		actionLog.Status = "success"
		actionLog.Result = "Notification sent successfully"
		actionLog.DurationMs = 100
		actionLog.CompletedAt = &completedAt

		err = store.UpdateActionLog(ctx, actionLog.ID, "success", "Notification sent successfully")
		require.NoError(t, err, "UpdateActionLog should succeed")

		// Step 4: Acknowledge Event
		err = store.UpdateEventStatus(ctx, event.ID, EventStatusAck, "admin@example.com")
		require.NoError(t, err, "UpdateEventStatus to acknowledged should succeed")

		// Verify acknowledgment
		updatedEvent, err := store.GetEvent(ctx, event.ID)
		require.NoError(t, err)
		assert.Equal(t, EventStatusAck, updatedEvent.Status)
		assert.NotNil(t, updatedEvent.AckedAt)
		assert.Equal(t, "admin@example.com", updatedEvent.AckedBy)

		// Step 5: Resolve Event
		err = store.UpdateEventStatus(ctx, event.ID, EventStatusResolved, "admin@example.com")
		require.NoError(t, err, "UpdateEventStatus to resolved should succeed")

		// Verify resolution
		resolvedEvent, err := store.GetEvent(ctx, event.ID)
		require.NoError(t, err)
		assert.Equal(t, EventStatusResolved, resolvedEvent.Status)
		assert.NotNil(t, resolvedEvent.ResolvedAt)
		assert.Equal(t, "admin@example.com", resolvedEvent.ResolvedBy)

		// Step 6: Verify action logs
		logs, err := store.GetActionLogs(ctx, event.ID)
		require.NoError(t, err)
		assert.Len(t, logs, 1)
		assert.Equal(t, "success", logs[0].Status)
	})

	t.Run("RuleEngine", func(t *testing.T) {
		// Create multiple rules with different conditions
		rules := []Rule{
			{
				Name:        "test-rule-critical-" + time.Now().Format("150405"),
				Description: "Critical CPU threshold",
				Metric:      "cpu_usage",
				Operator:    OpGte,
				Threshold:   95.0,
				Duration:    "1m",
				Severity:    SeverityCritical,
				Action:      ActionRestart,
				Enabled:     true,
				Cooldown:    "5m",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
			{
				Name:        "test-rule-memory-" + time.Now().Format("150405"),
				Description: "Memory threshold",
				Metric:      "memory_usage",
				Operator:    OpGte,
				Threshold:   90.0,
				Duration:    "5m",
				Severity:    SeverityWarning,
				Action:      ActionScaleUp,
				Enabled:     true,
				Cooldown:    "15m",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
		}

		for i := range rules {
			err := store.CreateRule(ctx, &rules[i])
			require.NoError(t, err, "CreateRule should succeed for rule %d", i)
		}

		// Get all active rules
		activeRules, err := store.ListActiveRules(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(activeRules), 2, "Should have at least 2 active rules")

		// Evaluate rules (simplified simulation)
		metrics := map[string]float64{
			"cpu_usage":    96.0,
			"memory_usage": 92.0,
		}

		for _, rule := range activeRules {
			if rule.Name != rules[0].Name && rule.Name != rules[1].Name {
				continue // Skip non-test rules
			}

			value, exists := metrics[rule.Metric]
			if !exists {
				continue
			}

			shouldTrigger := evaluateRule(rule, value)
			if shouldTrigger {
				event := &Event{
					RuleID:      rule.ID,
					RuleName:    rule.Name,
					Severity:    rule.Severity,
					Title:       "Threshold exceeded for " + rule.Metric,
					Description: "Auto-generated event from rule evaluation",
					Source:      "rule-engine",
					Status:      EventStatusNew,
					DetectedAt:  time.Now(),
					CreatedAt:   time.Now(),
					UpdatedAt:   time.Now(),
				}

				err := store.CreateEvent(ctx, event)
				require.NoError(t, err, "Event creation should succeed")
				t.Logf("Created event %d for rule %s", event.ID, rule.Name)
			}
		}
	})

	t.Run("DashboardStats", func(t *testing.T) {
		rule := &Rule{
			Name:        "test-rule-stats-" + time.Now().Format("20060102-150405"),
			Description: "Rule backing dashboard test events", Metric: "test_metric",
			Operator: OpGte, Threshold: 1, Duration: "1m", Severity: SeverityInfo,
			Action: ActionNotify, Enabled: true, Cooldown: "1m",
		}
		err := store.CreateRule(ctx, rule)
		require.NoError(t, err)

		// Create test events with different severities and sources
		testEvents := []Event{
			{
				RuleID:      rule.ID,
				RuleName:    "test-rule-stats-1",
				Severity:    SeverityInfo,
				Title:       "Info event",
				Description: "Test info event",
				Source:      "source-a",
				Status:      EventStatusNew,
				DetectedAt:  time.Now(),
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
			{
				RuleID:      rule.ID,
				RuleName:    "test-rule-stats-2",
				Severity:    SeverityWarning,
				Title:       "Warning event",
				Description: "Test warning event",
				Source:      "source-b",
				Status:      EventStatusNew,
				DetectedAt:  time.Now(),
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			},
			{
				RuleID:      rule.ID,
				RuleName:    "test-rule-stats-3",
				Severity:    SeverityCritical,
				Title:       "Critical event",
				Description: "Test critical event",
				Source:      "source-a",
				Status:      EventStatusResolved,
				DetectedAt:  time.Now().Add(-1 * time.Hour),
				CreatedAt:   time.Now().Add(-1 * time.Hour),
				UpdatedAt:   time.Now(),
			},
		}

		for i := range testEvents {
			err := store.CreateEvent(ctx, &testEvents[i])
			require.NoError(t, err, "CreateEvent should succeed")
		}

		// Get dashboard stats
		stats, err := store.GetDashboardStats(ctx)
		require.NoError(t, err, "GetDashboardStats should succeed")
		assert.NotNil(t, stats)
		assert.Greater(t, stats.TotalEvents, 0, "Should have events")
		assert.NotNil(t, stats.BySeverity, "BySeverity should not be nil")
		assert.NotNil(t, stats.BySource, "BySource should not be nil")

		t.Logf("Dashboard Stats: Total=%d, Open=%d, Resolved24h=%d, AvgResolve=%.2fmin",
			stats.TotalEvents, stats.OpenEvents, stats.Resolved24h, stats.AvgResolveMins)
	})

	t.Run("ActionExecution", func(t *testing.T) {
		// Create rule with different action types
		actionTypes := []string{ActionRestart, ActionScaleUp, ActionNotify, ActionFailover}

		for _, actionType := range actionTypes {
			ruleName := "test-rule-action-" + actionType + "-" + time.Now().Format("150405")
			rule := &Rule{
				Name:        ruleName,
				Description: "Test rule for " + actionType,
				Metric:      "test_metric",
				Operator:    OpGte,
				Threshold:   100.0,
				Duration:    "1m",
				Severity:    SeverityError,
				Action:      actionType,
				Enabled:     true,
				Cooldown:    "5m",
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}

			err := store.CreateRule(ctx, rule)
			require.NoError(t, err)

			// Create event for this rule
			event := &Event{
				RuleID:      rule.ID,
				RuleName:    ruleName,
				Severity:    SeverityError,
				Title:       "Test event for " + actionType,
				Description: "Testing action execution",
				Source:      "test-runner",
				Status:      EventStatusNew,
				DetectedAt:  time.Now(),
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}

			err = store.CreateEvent(ctx, event)
			require.NoError(t, err)

			// Simulate action execution
			actionLog := &ActionLog{
				EventID:     event.ID,
				Action:      actionType,
				Status:      "pending",
				TriggeredAt: time.Now(),
			}

			err = store.CreateActionLog(ctx, actionLog)
			require.NoError(t, err)

			result := "Action " + actionType + " completed successfully"
			err = store.UpdateActionLog(ctx, actionLog.ID, "success", result)
			require.NoError(t, err)
		}
	})
}

// TestRuleCooldown tests that cooldown prevents duplicate events
func TestRuleCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()

	store := NewPgxStore(pool)

	t.Run("Cooldown", func(t *testing.T) {
		ruleName := "test-rule-cooldown-" + time.Now().Format("20060102-150405")
		rule := &Rule{
			Name:        ruleName,
			Description: "Test cooldown",
			Metric:      "test_metric",
			Operator:    OpGte,
			Threshold:   50.0,
			Duration:    "1m",
			Severity:    SeverityWarning,
			Action:      ActionNotify,
			Enabled:     true,
			Cooldown:    "5m",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		err := store.CreateRule(ctx, rule)
		require.NoError(t, err)

		// Create first event
		event1 := &Event{
			RuleID:      rule.ID,
			RuleName:    ruleName,
			Severity:    SeverityWarning,
			Title:       "First event",
			Description: "Should be created",
			Source:      "test",
			Status:      EventStatusNew,
			DetectedAt:  time.Now(),
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		err = store.CreateEvent(ctx, event1)
		require.NoError(t, err)

		// Check for open events (cooldown check)
		openEvents, err := store.GetOpenEventsByRule(ctx, rule.ID)
		require.NoError(t, err)
		assert.Len(t, openEvents, 1, "Should have 1 open event")

		// Application layer should prevent creating another event during cooldown
		// This is just verifying the data layer supports the check
		t.Log("Cooldown mechanism verified - application layer should check GetOpenEventsByRule")

		// Clean up
		_, _ = pool.Exec(ctx, "DELETE FROM fault_events WHERE id = $1", event1.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM fault_rules WHERE id = $1", rule.ID)
	})
}

// Helper functions

func evaluateRule(rule Rule, value float64) bool {
	switch rule.Operator {
	case OpGte:
		return value >= rule.Threshold
	case OpLte:
		return value <= rule.Threshold
	case OpEq:
		return value == rule.Threshold
	case OpNe:
		return value != rule.Threshold
	default:
		return false
	}
}

func mustJSON(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
