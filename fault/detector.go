package fault

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Detector struct {
	store       Store
	ruleEngine  *RuleEngine
	mu          sync.RWMutex
	activeRules map[int64]*Rule
	stopChan    chan struct{}
}

func NewDetector(store Store, ruleEngine *RuleEngine) *Detector {
	return &Detector{
		store:       store,
		ruleEngine:  ruleEngine,
		activeRules: make(map[int64]*Rule),
		stopChan:    make(chan struct{}),
	}
}

func (d *Detector) Start(ctx context.Context) error {
	if err := d.loadRules(ctx); err != nil {
		return err
	}

	go d.runDetectionLoop(ctx)
	slog.Info("fault detector started")
	return nil
}

func (d *Detector) Stop() {
	close(d.stopChan)
	slog.Info("fault detector stopped")
}

func (d *Detector) loadRules(ctx context.Context) error {
	rules, err := d.store.ListActiveRules(ctx)
	if err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for i := range rules {
		d.activeRules[rules[i].ID] = &rules[i]
	}

	slog.Info("fault rules loaded", "count", len(rules))
	return nil
}

func (d *Detector) runDetectionLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopChan:
			return
		case <-ticker.C:
			d.checkMetrics(ctx)
		}
	}
}

func (d *Detector) checkMetrics(ctx context.Context) {
	d.mu.RLock()
	rules := make([]*Rule, 0, len(d.activeRules))
	for _, rule := range d.activeRules {
		rules = append(rules, rule)
	}
	d.mu.RUnlock()

	for _, rule := range rules {
		go d.evaluateRule(ctx, rule)
	}
}

func (d *Detector) evaluateRule(ctx context.Context, rule *Rule) {
	metricValue, err := d.getMetricValue(ctx, rule.Metric)
	if err != nil {
		slog.Error("failed to get metric", "metric", rule.Metric, "error", err)
		return
	}

	triggered := d.checkThreshold(metricValue, rule.Operator, rule.Threshold)
	if !triggered {
		return
	}

	existingEvents, err := d.store.GetOpenEventsByRule(ctx, rule.ID)
	if err != nil {
		slog.Error("failed to get open events", "rule_id", rule.ID, "error", err)
		return
	}

	if len(existingEvents) > 0 {
		slog.Debug("rule already has open event", "rule_id", rule.ID)
		return
	}

	event := &Event{
		RuleID:      rule.ID,
		RuleName:    rule.Name,
		Severity:    rule.Severity,
		Title:       rule.Name + " triggered",
		Description: rule.Description,
		Source:      "detector",
		Status:      EventStatusNew,
		DetectedAt:  time.Now(),
	}

	if err := d.store.CreateEvent(ctx, event); err != nil {
		slog.Error("failed to create event", "rule", rule.Name, "error", err)
		return
	}

	slog.Warn("fault event created", "rule", rule.Name, "severity", rule.Severity, "metric_value", metricValue)

	if err := d.ruleEngine.ExecuteAction(ctx, event, rule); err != nil {
		slog.Error("failed to execute action", "event_id", event.ID, "error", err)
	}
}

func (d *Detector) getMetricValue(ctx context.Context, metric string) (float64, error) {
	switch metric {
	case "cpu_usage":
		return d.getCPUUsage(ctx)
	case "memory_usage":
		return d.getMemoryUsage(ctx)
	case "error_rate":
		return d.getErrorRate(ctx)
	case "response_time_p99":
		return d.getResponseTimeP99(ctx)
	default:
		return 0, nil
	}
}

func (d *Detector) getCPUUsage(ctx context.Context) (float64, error) {
	return 0.0, nil
}

func (d *Detector) getMemoryUsage(ctx context.Context) (float64, error) {
	return 0.0, nil
}

func (d *Detector) getErrorRate(ctx context.Context) (float64, error) {
	return 0.0, nil
}

func (d *Detector) getResponseTimeP99(ctx context.Context) (float64, error) {
	return 0.0, nil
}

func (d *Detector) checkThreshold(value float64, operator string, threshold float64) bool {
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

func (d *Detector) ReloadRules(ctx context.Context) error {
	return d.loadRules(ctx)
}
