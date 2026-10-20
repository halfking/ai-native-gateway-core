package fault

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/dbrows"
	"github.com/kaixuan/llm-gateway-go/internal/jsoncol"
)

type PgxStore struct {
	pool *pgxpool.Pool
}

// ★ 读路径的 NULL 语义（2026-10-21 修，见 docs/audit/2026-10-21-R60-新装缺列族.md §9）：
//
// `fault_events` 里 `acked_at` / `acked_by` / `resolved_at` / `resolved_by` 四列
// **都可空**（权威 DDL：sql/objects/tables/fault_events.sql），而
// `Event` 结构体对**时间戳**用 `*time.Time`（NULL ⇒ nil ⇒ omitempty 不输出），
// 对**文本**却用 `string`（⇒ 扫 NULL 必炸：cannot scan NULL into *string）。
//
// ⇒ ⇒ 同一张表里两种 NULL 语义 ⇒ 生产上 `public.fault_events` 的 9 行
// **全部** `acked_by`/`resolved_by` 为 NULL ⇒ `GET /api/admin/faults/events`
// （cmd/gateway/main.go 注册的 `/api/admin/faults/*`）**必然 500**。
//
// ⇒ 修法：把两个文本列在 SQL 侧用 COALESCE 兜成空串 ⇒ 与时间戳的既有语义对齐
// （「缺省 ⇒ 空串 ⇒ JSON 里 omitempty 不输出」），**不**改 Go 结构体、**不**改 API 形状。
// ⇒ GetEvent / GetOpenEventsByRule / ListEvents 三处必须同步改 —— 漏一处就还是坏的。
//
// ⚠ 注释里别写两个连续单引号：gofmt 的 doc 注释正规化会把它们改成弯引号，
// 于是「gofmt 后文件又变了」变成常态（本轮真踩了一次）。
func NewPgxStore(pool *pgxpool.Pool) *PgxStore {
	return &PgxStore{pool: pool}
}

func (s *PgxStore) CreateEvent(ctx context.Context, event *Event) error {
	metadataJSON, _ := json.Marshal(event.Metadata)
	return s.pool.QueryRow(ctx, `
		INSERT INTO fault_events (rule_id, rule_name, severity, title, description, source,
		                          status, metadata, detected_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		RETURNING id, created_at
	`, event.RuleID, event.RuleName, event.Severity, event.Title, event.Description,
		event.Source, event.Status, metadataJSON, event.DetectedAt,
	).Scan(&event.ID, &event.CreatedAt)
}

func (s *PgxStore) GetEvent(ctx context.Context, eventID int64) (*Event, error) {
	var event Event
	var metadataJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, rule_id, rule_name, severity, title, description, source, status, metadata,
		       detected_at, acked_at, COALESCE(acked_by, ''), resolved_at, COALESCE(resolved_by, ''),
		       created_at, updated_at
		FROM fault_events WHERE id = $1
	`, eventID).Scan(
		&event.ID, &event.RuleID, &event.RuleName, &event.Severity, &event.Title,
		&event.Description, &event.Source, &event.Status, &metadataJSON,
		&event.DetectedAt, &event.AckedAt, &event.AckedBy, &event.ResolvedAt,
		&event.ResolvedBy, &event.CreatedAt, &event.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("event not found")
		}
		return nil, err
	}
	jsoncol.Decode("fault.PgxStore.GetEvent/metadata", metadataJSON, &event.Metadata)
	return &event, nil
}

func (s *PgxStore) GetOpenEventsByRule(ctx context.Context, ruleID int64) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, rule_id, rule_name, severity, title, description, source, status, metadata,
		       detected_at, acked_at, COALESCE(acked_by, ''), resolved_at, COALESCE(resolved_by, ''),
		       created_at, updated_at
		FROM fault_events
		WHERE rule_id = $1 AND status IN ('new', 'acknowledged', 'resolving')
		ORDER BY detected_at DESC
	`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var metadataJSON []byte
		if err := rows.Scan(
			&event.ID, &event.RuleID, &event.RuleName, &event.Severity, &event.Title,
			&event.Description, &event.Source, &event.Status, &metadataJSON,
			&event.DetectedAt, &event.AckedAt, &event.AckedBy, &event.ResolvedAt,
			&event.ResolvedBy, &event.CreatedAt, &event.UpdatedAt,
		); err != nil {
			return nil, err
		}
		jsoncol.Decode("fault.PgxStore.GetOpenEventsByRule/metadata", metadataJSON, &event.Metadata)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *PgxStore) UpdateEventStatus(ctx context.Context, eventID int64, status EventStatus, actor string) error {
	var query string
	var args []interface{}

	switch status {
	case EventStatusAck:
		query = `UPDATE fault_events SET status = $1, acked_at = NOW(), acked_by = $2, updated_at = NOW() WHERE id = $3`
		args = []interface{}{status, actor, eventID}
	case EventStatusResolved:
		query = `UPDATE fault_events SET status = $1, resolved_at = NOW(), resolved_by = $2, updated_at = NOW() WHERE id = $3`
		args = []interface{}{status, actor, eventID}
	default:
		query = `UPDATE fault_events SET status = $1, updated_at = NOW() WHERE id = $2`
		args = []interface{}{status, eventID}
	}

	_, err := s.pool.Exec(ctx, query, args...)
	return err
}

func (s *PgxStore) ListEvents(ctx context.Context, status EventStatus, offset, limit int) ([]Event, int, error) {
	var whereClause string
	var args []interface{}

	if status != "" {
		whereClause = "WHERE status = $1"
		args = append(args, status)
	}

	query := `SELECT id, rule_id, rule_name, severity, title, description, source, status, metadata,
	                 detected_at, acked_at, COALESCE(acked_by, ''), resolved_at, COALESCE(resolved_by, ''),
	                 created_at, updated_at
	          FROM fault_events ` + whereClause + `
	          ORDER BY detected_at DESC
	          LIMIT $` + offsetPlaceholder(len(args)+1) + ` OFFSET $` + offsetPlaceholder(len(args)+2)

	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var metadataJSON []byte
		if err := rows.Scan(
			&event.ID, &event.RuleID, &event.RuleName, &event.Severity, &event.Title,
			&event.Description, &event.Source, &event.Status, &metadataJSON,
			&event.DetectedAt, &event.AckedAt, &event.AckedBy, &event.ResolvedAt,
			&event.ResolvedBy, &event.CreatedAt, &event.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		jsoncol.Decode("fault.PgxStore.ListEvents/metadata", metadataJSON, &event.Metadata)
		events = append(events, event)
	}

	var total int
	countQuery := `SELECT COUNT(*) FROM fault_events ` + whereClause
	if err := s.pool.QueryRow(ctx, countQuery, args[:len(args)-2]...).Scan(&total); err != nil {
		return nil, 0, err
	}

	return events, total, rows.Err()
}

func (s *PgxStore) CreateRule(ctx context.Context, rule *Rule) error {
	actionConfigJSON, _ := json.Marshal(rule.ActionConfig)
	return s.pool.QueryRow(ctx, `
		INSERT INTO fault_rules (name, description, metric, operator, threshold, duration,
		                         severity, action, action_config, enabled, cooldown, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		RETURNING id, created_at
	`, rule.Name, rule.Description, rule.Metric, rule.Operator, rule.Threshold, rule.Duration,
		rule.Severity, rule.Action, actionConfigJSON, rule.Enabled, rule.Cooldown,
	).Scan(&rule.ID, &rule.CreatedAt)
}

func (s *PgxStore) GetRule(ctx context.Context, ruleID int64) (*Rule, error) {
	var rule Rule
	var actionConfigJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, description, metric, operator, threshold, duration, severity, action,
		       action_config, enabled, cooldown, created_at, updated_at
		FROM fault_rules WHERE id = $1
	`, ruleID).Scan(
		&rule.ID, &rule.Name, &rule.Description, &rule.Metric, &rule.Operator, &rule.Threshold,
		&rule.Duration, &rule.Severity, &rule.Action, &actionConfigJSON, &rule.Enabled,
		&rule.Cooldown, &rule.CreatedAt, &rule.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("rule not found")
		}
		return nil, err
	}
	jsoncol.Decode("fault.PgxStore.GetRule/action_config", actionConfigJSON, &rule.ActionConfig)
	return &rule, nil
}

func (s *PgxStore) UpdateRule(ctx context.Context, rule *Rule) error {
	actionConfigJSON, _ := json.Marshal(rule.ActionConfig)
	_, err := s.pool.Exec(ctx, `
		UPDATE fault_rules
		SET name = $1, description = $2, metric = $3, operator = $4, threshold = $5,
		    duration = $6, severity = $7, action = $8, action_config = $9, enabled = $10,
		    cooldown = $11, updated_at = NOW()
		WHERE id = $12
	`, rule.Name, rule.Description, rule.Metric, rule.Operator, rule.Threshold, rule.Duration,
		rule.Severity, rule.Action, actionConfigJSON, rule.Enabled, rule.Cooldown, rule.ID)
	return err
}

func (s *PgxStore) DeleteRule(ctx context.Context, ruleID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM fault_rules WHERE id = $1`, ruleID)
	return err
}

func (s *PgxStore) ListActiveRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, metric, operator, threshold, duration, severity, action,
		       action_config, enabled, cooldown, created_at, updated_at
		FROM fault_rules WHERE enabled = true
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanRules(rows)
}

func (s *PgxStore) ListAllRules(ctx context.Context, offset, limit int) ([]Rule, int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, metric, operator, threshold, duration, severity, action,
		       action_config, enabled, cooldown, created_at, updated_at
		FROM fault_rules
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	rules, err := s.scanRules(rows)
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fault_rules`).Scan(&total); err != nil {
		return nil, 0, err
	}

	return rules, total, nil
}

func (s *PgxStore) scanRules(rows interface {
	Next() bool
	Scan(...interface{}) error
	Err() error
	Close()
}) ([]Rule, error) {
	var rules []Rule
	for rows.Next() {
		var rule Rule
		var actionConfigJSON []byte
		if err := rows.Scan(
			&rule.ID, &rule.Name, &rule.Description, &rule.Metric, &rule.Operator, &rule.Threshold,
			&rule.Duration, &rule.Severity, &rule.Action, &actionConfigJSON, &rule.Enabled,
			&rule.Cooldown, &rule.CreatedAt, &rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		jsoncol.Decode("fault.PgxStore.ListActiveRules/action_config", actionConfigJSON, &rule.ActionConfig)
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *PgxStore) CreateActionLog(ctx context.Context, log *ActionLog) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO fault_action_logs (event_id, action, status, triggered_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, log.EventID, log.Action, log.Status, log.TriggeredAt).Scan(&log.ID)
}

func (s *PgxStore) GetActionLogs(ctx context.Context, eventID int64) ([]ActionLog, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, event_id, action, status, result, duration_ms, triggered_at, completed_at
		FROM fault_action_logs WHERE event_id = $1
		ORDER BY triggered_at DESC
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []ActionLog
	for rows.Next() {
		var log ActionLog
		if err := rows.Scan(
			&log.ID, &log.EventID, &log.Action, &log.Status, &log.Result, &log.DurationMs,
			&log.TriggeredAt, &log.CompletedAt,
		); err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

func (s *PgxStore) UpdateActionLog(ctx context.Context, logID int64, status, result string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fault_action_logs
		SET status = $1, result = $2, completed_at = NOW()
		WHERE id = $3
	`, status, result, logID)
	return err
}

func (s *PgxStore) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	stats := &DashboardStats{
		BySeverity: make(map[Severity]int),
		BySource:   make(map[string]int),
	}

	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fault_events`).Scan(&stats.TotalEvents); err != nil {
		return nil, err
	}

	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fault_events WHERE status IN ('new', 'acknowledged', 'resolving')`).Scan(&stats.OpenEvents); err != nil {
		return nil, err
	}

	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fault_events WHERE status = 'resolved' AND resolved_at > NOW() - INTERVAL '24 hours'`).Scan(&stats.Resolved24h); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `SELECT severity, COUNT(*) FROM fault_events GROUP BY severity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var count int
		if err := rows.Scan(&sev, &count); err != nil {
			if dbrows.SkipOrFail("fault.PgxStore.GetDashboardStats/bySeverity", err) {
				continue
			}
		}
		stats.BySeverity[Severity(sev)] = count
	}
	// R66: severity 分组被截断 = 仪表盘严重度分布静默失真（分母
	// TotalEvents 用的是 COUNT(*)，不随本读截断，缺口无痕）。上抛。
	// 直接消费 rows.Err()（非 dbrows.Err 包装），使站位级守卫能逐循环
	// 判定；单行跳行留痕仍走 dbrows.SkipOrFail。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fault.PgxStore.GetDashboardStats: iterate rows: %w", err)
	}

	return stats, nil
}

func offsetPlaceholder(n int) string {
	return strconv.Itoa(n)
}
