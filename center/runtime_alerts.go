package center

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/collector"
)

const (
	RuntimeAlertTriggered    = "triggered"
	RuntimeAlertAcknowledged = "acknowledged"
	RuntimeAlertResolved     = "resolved"
	RuntimeAlertSuppressed   = "suppressed"

	RuleCPUHigh        = "cpu_high"
	RuleDiskHigh       = "disk_high"
	RuleLowSuccessRate = "low_success_rate"
)

// RuntimeAlertEvent is a persisted runtime telemetry alert.
type RuntimeAlertEvent struct {
	ID              int64      `json:"id"`
	RuleKey         string     `json:"rule_key"`
	InstanceID      string     `json:"instance_id"`
	Severity        string     `json:"severity"`
	Title           string     `json:"title"`
	Message         string     `json:"message"`
	Status          string     `json:"status"`
	MetricValue     float64    `json:"metric_value"`
	DetectedAt      time.Time  `json:"detected_at"`
	AckedAt         *time.Time `json:"acked_at,omitempty"`
	AckedBy         string     `json:"acked_by,omitempty"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy      string     `json:"resolved_by,omitempty"`
	SuppressedUntil *time.Time `json:"suppressed_until,omitempty"`
}

type runtimeAlertCandidate struct {
	RuleKey     string
	Severity    string
	Title       string
	Message     string
	MetricValue float64
}

// EvaluateRuntimeAlertCandidates applies built-in allowlisted runtime thresholds.
func EvaluateRuntimeAlertCandidates(metrics collector.RuntimeMetrics) []runtimeAlertCandidate {
	var out []runtimeAlertCandidate
	if metrics.CPUUsagePct >= 95 {
		out = append(out, runtimeAlertCandidate{
			RuleKey: RuleCPUHigh, Severity: "warning", Title: "CPU 使用率过高",
			Message:     fmt.Sprintf("instance %s cpu %.1f%% >= 95%%", metrics.InstanceID, metrics.CPUUsagePct),
			MetricValue: metrics.CPUUsagePct,
		})
	}
	if metrics.DiskTotalGB > 0 {
		ratio := float64(metrics.DiskUsedGB) / float64(metrics.DiskTotalGB)
		if ratio >= 0.95 {
			out = append(out, runtimeAlertCandidate{
				RuleKey: RuleDiskHigh, Severity: "critical", Title: "磁盘使用率过高",
				Message:     fmt.Sprintf("instance %s disk %.1f%% >= 95%%", metrics.InstanceID, ratio*100),
				MetricValue: ratio * 100,
			})
		}
	}
	if metrics.Last5MinSuccessPct > 0 && metrics.Last5MinSuccessPct < 90 {
		out = append(out, runtimeAlertCandidate{
			RuleKey: RuleLowSuccessRate, Severity: "warning", Title: "请求成功率偏低",
			Message:     fmt.Sprintf("instance %s success %.1f%% < 90%%", metrics.InstanceID, metrics.Last5MinSuccessPct),
			MetricValue: metrics.Last5MinSuccessPct,
		})
	}
	return out
}

func (s *PgxStore) ProcessRuntimeAlerts(ctx context.Context, metrics collector.RuntimeMetrics) error {
	for _, candidate := range EvaluateRuntimeAlertCandidates(metrics) {
		if err := s.triggerRuntimeAlert(ctx, metrics.InstanceID, candidate); err != nil {
			return err
		}
	}
	return s.autoResolveRuntimeAlerts(ctx, metrics)
}

func (s *PgxStore) triggerRuntimeAlert(ctx context.Context, instanceID string, c runtimeAlertCandidate) error {
	var existingID int64
	var status string
	var suppressedUntil *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT id, status, suppressed_until
		FROM runtime_alert_events
		WHERE rule_key = $1 AND instance_id = $2
		  AND status IN ('triggered', 'acknowledged', 'suppressed')
		ORDER BY detected_at DESC
		LIMIT 1
	`, c.RuleKey, instanceID).Scan(&existingID, &status, &suppressedUntil)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if status == RuntimeAlertSuppressed && suppressedUntil != nil && suppressedUntil.After(time.Now().UTC()) {
			return nil
		}
		if status == RuntimeAlertTriggered || status == RuntimeAlertAcknowledged {
			return nil
		}
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO runtime_alert_events
			(rule_key, instance_id, severity, title, message, status, metric_value, detected_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'triggered', $6, now(), now())
	`, c.RuleKey, instanceID, c.Severity, c.Title, c.Message, c.MetricValue)
	return err
}

func (s *PgxStore) autoResolveRuntimeAlerts(ctx context.Context, metrics collector.RuntimeMetrics) error {
	type ruleCheck struct {
		key      string
		resolved bool
	}
	checks := []ruleCheck{
		{RuleCPUHigh, metrics.CPUUsagePct < 90},
		{RuleDiskHigh, metrics.DiskTotalGB == 0 || float64(metrics.DiskUsedGB)/float64(metrics.DiskTotalGB) < 0.90},
		{RuleLowSuccessRate, metrics.Last5MinSuccessPct == 0 || metrics.Last5MinSuccessPct >= 92},
	}
	for _, check := range checks {
		if !check.resolved {
			continue
		}
		_, err := s.db.Exec(ctx, `
			UPDATE runtime_alert_events
			SET status = 'resolved', resolved_at = now(), resolved_by = 'auto', updated_at = now()
			WHERE instance_id = $1 AND rule_key = $2 AND status IN ('triggered', 'acknowledged')
		`, metrics.InstanceID, check.key)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PgxStore) ListOpenRuntimeAlerts(ctx context.Context, limit int) ([]RuntimeAlertEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, rule_key, instance_id, severity, title, message, status, metric_value,
		       detected_at, acked_at, acked_by, resolved_at, resolved_by, suppressed_until
		FROM runtime_alert_events
		WHERE status IN ('triggered', 'acknowledged', 'suppressed')
		  AND (suppressed_until IS NULL OR suppressed_until > now())
		ORDER BY detected_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuntimeAlerts(rows)
}

func scanRuntimeAlerts(rows pgx.Rows) ([]RuntimeAlertEvent, error) {
	var out []RuntimeAlertEvent
	for rows.Next() {
		var evt RuntimeAlertEvent
		if err := rows.Scan(
			&evt.ID, &evt.RuleKey, &evt.InstanceID, &evt.Severity, &evt.Title, &evt.Message,
			&evt.Status, &evt.MetricValue, &evt.DetectedAt, &evt.AckedAt, &evt.AckedBy,
			&evt.ResolvedAt, &evt.ResolvedBy, &evt.SuppressedUntil,
		); err != nil {
			return nil, err
		}
		out = append(out, evt)
	}
	if out == nil {
		out = []RuntimeAlertEvent{}
	}
	return out, rows.Err()
}

func (s *PgxStore) AcknowledgeRuntimeAlert(ctx context.Context, id int64, actor string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE runtime_alert_events
		SET status = 'acknowledged', acked_at = now(), acked_by = $2, updated_at = now()
		WHERE id = $1 AND status = 'triggered'
	`, id, actor)
	return err
}

func (s *PgxStore) ResolveRuntimeAlert(ctx context.Context, id int64, actor string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE runtime_alert_events
		SET status = 'resolved', resolved_at = now(), resolved_by = $2, updated_at = now()
		WHERE id = $1 AND status IN ('triggered', 'acknowledged', 'suppressed')
	`, id, actor)
	return err
}

func (s *PgxStore) SuppressRuntimeAlert(ctx context.Context, id int64, until time.Time, actor string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE runtime_alert_events
		SET status = 'suppressed', suppressed_until = $2, acked_by = COALESCE(NULLIF(acked_by, ''), $3),
		    acked_at = COALESCE(acked_at, now()), updated_at = now()
		WHERE id = $1 AND status IN ('triggered', 'acknowledged')
	`, id, until, actor)
	return err
}

func runtimeAlertToOpsAlert(evt RuntimeAlertEvent) OpsAlert {
	return OpsAlert{
		ID:         fmt.Sprintf("runtime-%d", evt.ID),
		Severity:   evt.Severity,
		Title:      evt.Title,
		Message:    evt.Message,
		Source:     "runtime_metrics",
		Status:     evt.Status,
		InstanceID: evt.InstanceID,
		DetectedAt: evt.DetectedAt,
	}
}
