package fault

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

type RuleEngine struct {
	store    Store
	executor *ActionExecutor
}

func NewRuleEngine(store Store, executor *ActionExecutor) *RuleEngine {
	return &RuleEngine{
		store:    store,
		executor: executor,
	}
}

func (re *RuleEngine) EvaluateRule(ctx context.Context, rule *Rule, metricValue float64) (bool, error) {
	return checkThreshold(metricValue, rule.Operator, rule.Threshold), nil
}

func (re *RuleEngine) ExecuteAction(ctx context.Context, event *Event, rule *Rule) error {
	if rule.Action == "" {
		slog.Info("no action configured for rule", "rule_id", rule.ID)
		return nil
	}

	actionLog := &ActionLog{
		EventID:     event.ID,
		Action:      rule.Action,
		Status:      "pending",
		TriggeredAt: time.Now(),
	}

	if err := re.store.CreateActionLog(ctx, actionLog); err != nil {
		return err
	}

	go re.executeActionAsync(context.Background(), actionLog, rule)

	return nil
}

func (re *RuleEngine) executeActionAsync(ctx context.Context, actionLog *ActionLog, rule *Rule) {
	startTime := time.Now()

	var config map[string]interface{}
	if rule.ActionConfig != nil {
		if err := json.Unmarshal(rule.ActionConfig, &config); err != nil {
			slog.Error("failed to unmarshal action config", "error", err)
			config = make(map[string]interface{})
		}
	}

	result, err := re.executor.Execute(ctx, rule.Action, config)

	durationMs := time.Since(startTime).Milliseconds()
	actionLog.DurationMs = durationMs
	now := time.Now()
	actionLog.CompletedAt = &now

	if err != nil {
		actionLog.Status = "failed"
		actionLog.Result = "error: " + err.Error()
		slog.Error("action execution failed", "action", rule.Action, "error", err)
	} else {
		actionLog.Status = "success"
		actionLog.Result = result
		slog.Info("action executed successfully", "action", rule.Action, "duration_ms", durationMs)
	}

	if err := re.store.UpdateActionLog(ctx, actionLog.ID, actionLog.Status, actionLog.Result); err != nil {
		slog.Error("failed to update action log", "error", err)
	}
}

func checkThreshold(value float64, operator string, threshold float64) bool {
	switch operator {
	case OpGte:
		return value >= threshold
	case OpLte:
		return value <= threshold
	case OpEq:
		return value == threshold
	case OpNe:
		return value != threshold
	default:
		return false
	}
}

func (re *RuleEngine) ValidateRule(rule *Rule) error {
	if rule.Name == "" {
		return errors.New("rule name is required")
	}
	if rule.Metric == "" {
		return errors.New("metric is required")
	}
	if rule.Operator == "" {
		return errors.New("operator is required")
	}
	if rule.Action == "" {
		return errors.New("action is required")
	}
	return nil
}
