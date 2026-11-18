package diagnostics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Logger 诊断日志接口
type Logger interface {
	// Save 保存诊断日志
	Save(ctx context.Context, log *SessionDiagnosticLog) error

	// Get 获取指定请求的诊断日志
	Get(ctx context.Context, requestID string) (*SessionDiagnosticLog, error)

	// ListBySession 获取指定会话的所有诊断日志
	ListBySession(ctx context.Context, sessionID string, limit int) ([]*SessionDiagnosticLog, error)

	// ListFailed 获取失败的请求日志
	ListFailed(ctx context.Context, startTime, endTime time.Time, limit int) ([]*SessionDiagnosticLog, error)

	// QueryByFilters 根据条件查询
	QueryByFilters(ctx context.Context, filters QueryFilters) ([]*SessionDiagnosticLog, error)
}

// QueryFilters 查询过滤条件
type QueryFilters struct {
	StartTime   time.Time
	EndTime     time.Time
	TenantID    string
	Model       string
	StatusCode  int
	SuccessOnly *bool // nil=全部, true=仅成功, false=仅失败
	Limit       int
	Offset      int
}

// PostgresLogger PostgreSQL实现
type PostgresLogger struct {
	db *sql.DB
}

// NewPostgresLogger 创建PostgreSQL日志记录器
func NewPostgresLogger(db *sql.DB) *PostgresLogger {
	return &PostgresLogger{db: db}
}

// Save 保存诊断日志
func (l *PostgresLogger) Save(ctx context.Context, log *SessionDiagnosticLog) error {
	// 序列化JSON字段
	routingJSON, err := json.Marshal(log.RoutingDecision)
	if err != nil {
		return fmt.Errorf("marshal routing_decision: %w", err)
	}

	attemptsJSON, err := json.Marshal(log.Attempts)
	if err != nil {
		return fmt.Errorf("marshal attempts: %w", err)
	}

	var finalErrorJSON []byte
	if log.FinalError != nil {
		finalErrorJSON, err = json.Marshal(log.FinalError)
		if err != nil {
			return fmt.Errorf("marshal final_error: %w", err)
		}
	}

	query := `
		INSERT INTO request_diagnostic_logs (
			request_id, session_id, tenant_id, client_model,
			start_time, end_time, final_status, success,
			routing_decision, attempts, final_error,
			total_attempts, switches, retries, total_duration_ms
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)
		ON CONFLICT (request_id) DO UPDATE SET
			end_time = EXCLUDED.end_time,
			final_status = EXCLUDED.final_status,
			success = EXCLUDED.success,
			routing_decision = EXCLUDED.routing_decision,
			attempts = EXCLUDED.attempts,
			final_error = EXCLUDED.final_error,
			total_attempts = EXCLUDED.total_attempts,
			switches = EXCLUDED.switches,
			retries = EXCLUDED.retries,
			total_duration_ms = EXCLUDED.total_duration_ms
	`

	_, err = l.db.ExecContext(ctx, query,
		log.RequestID,
		log.SessionID,
		log.TenantID,
		log.ClientModel,
		log.StartTime,
		log.EndTime,
		log.FinalStatus,
		log.Success,
		routingJSON,
		attemptsJSON,
		finalErrorJSON,
		log.TotalAttempts,
		log.Switches,
		log.Retries,
		log.TotalDuration,
	)

	return err
}

// Get 获取指定请求的诊断日志
func (l *PostgresLogger) Get(ctx context.Context, requestID string) (*SessionDiagnosticLog, error) {
	query := `
		SELECT
			request_id, session_id, tenant_id, client_model,
			start_time, end_time, final_status, success,
			routing_decision, attempts, final_error,
			total_attempts, switches, retries, total_duration_ms
		FROM request_diagnostic_logs
		WHERE request_id = $1
	`

	var log SessionDiagnosticLog
	var routingJSON, attemptsJSON, finalErrorJSON []byte

	err := l.db.QueryRowContext(ctx, query, requestID).Scan(
		&log.RequestID,
		&log.SessionID,
		&log.TenantID,
		&log.ClientModel,
		&log.StartTime,
		&log.EndTime,
		&log.FinalStatus,
		&log.Success,
		&routingJSON,
		&attemptsJSON,
		&finalErrorJSON,
		&log.TotalAttempts,
		&log.Switches,
		&log.Retries,
		&log.TotalDuration,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("diagnostic log not found for request_id: %s", requestID)
	}
	if err != nil {
		return nil, err
	}

	// 反序列化JSON字段
	if len(routingJSON) > 0 {
		if err := json.Unmarshal(routingJSON, &log.RoutingDecision); err != nil {
			return nil, fmt.Errorf("unmarshal routing_decision: %w", err)
		}
	}

	if err := json.Unmarshal(attemptsJSON, &log.Attempts); err != nil {
		return nil, fmt.Errorf("unmarshal attempts: %w", err)
	}

	if len(finalErrorJSON) > 0 {
		log.FinalError = &ErrorRecord{}
		if err := json.Unmarshal(finalErrorJSON, log.FinalError); err != nil {
			return nil, fmt.Errorf("unmarshal final_error: %w", err)
		}
	}

	return &log, nil
}

// ListBySession 获取指定会话的所有诊断日志
func (l *PostgresLogger) ListBySession(ctx context.Context, sessionID string, limit int) ([]*SessionDiagnosticLog, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT
			request_id, session_id, tenant_id, client_model,
			start_time, end_time, final_status, success,
			routing_decision, attempts, final_error,
			total_attempts, switches, retries, total_duration_ms
		FROM request_diagnostic_logs
		WHERE session_id = $1
		ORDER BY start_time DESC
		LIMIT $2
	`

	rows, err := l.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return l.scanRows(rows)
}

// ListFailed 获取失败的请求日志
func (l *PostgresLogger) ListFailed(ctx context.Context, startTime, endTime time.Time, limit int) ([]*SessionDiagnosticLog, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT
			request_id, session_id, tenant_id, client_model,
			start_time, end_time, final_status, success,
			routing_decision, attempts, final_error,
			total_attempts, switches, retries, total_duration_ms
		FROM request_diagnostic_logs
		WHERE success = false
		  AND start_time >= $1
		  AND start_time < $2
		ORDER BY start_time DESC
		LIMIT $3
	`

	rows, err := l.db.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return l.scanRows(rows)
}

// QueryByFilters 根据条件查询
func (l *PostgresLogger) QueryByFilters(ctx context.Context, filters QueryFilters) ([]*SessionDiagnosticLog, error) {
	if filters.Limit <= 0 {
		filters.Limit = 100
	}

	query := `
		SELECT
			request_id, session_id, tenant_id, client_model,
			start_time, end_time, final_status, success,
			routing_decision, attempts, final_error,
			total_attempts, switches, retries, total_duration_ms
		FROM request_diagnostic_logs
		WHERE start_time >= $1
		  AND start_time < $2
	`

	args := []interface{}{filters.StartTime, filters.EndTime}
	argIdx := 3

	if filters.TenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, filters.TenantID)
		argIdx++
	}

	if filters.Model != "" {
		query += fmt.Sprintf(" AND client_model = $%d", argIdx)
		args = append(args, filters.Model)
		argIdx++
	}

	if filters.StatusCode > 0 {
		query += fmt.Sprintf(" AND final_status = $%d", argIdx)
		args = append(args, filters.StatusCode)
		argIdx++
	}

	if filters.SuccessOnly != nil {
		query += fmt.Sprintf(" AND success = $%d", argIdx)
		args = append(args, *filters.SuccessOnly)
		argIdx++
	}

	query += fmt.Sprintf(" ORDER BY start_time DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, filters.Limit, filters.Offset)

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return l.scanRows(rows)
}

// scanRows 扫描结果集
func (l *PostgresLogger) scanRows(rows *sql.Rows) ([]*SessionDiagnosticLog, error) {
	var logs []*SessionDiagnosticLog

	for rows.Next() {
		var log SessionDiagnosticLog
		var routingJSON, attemptsJSON, finalErrorJSON []byte

		err := rows.Scan(
			&log.RequestID,
			&log.SessionID,
			&log.TenantID,
			&log.ClientModel,
			&log.StartTime,
			&log.EndTime,
			&log.FinalStatus,
			&log.Success,
			&routingJSON,
			&attemptsJSON,
			&finalErrorJSON,
			&log.TotalAttempts,
			&log.Switches,
			&log.Retries,
			&log.TotalDuration,
		)
		if err != nil {
			return nil, err
		}

		// 反序列化JSON字段
		if len(routingJSON) > 0 {
			if err := json.Unmarshal(routingJSON, &log.RoutingDecision); err != nil {
				return nil, fmt.Errorf("unmarshal routing_decision: %w", err)
			}
		}

		if err := json.Unmarshal(attemptsJSON, &log.Attempts); err != nil {
			return nil, fmt.Errorf("unmarshal attempts: %w", err)
		}

		if len(finalErrorJSON) > 0 {
			log.FinalError = &ErrorRecord{}
			if err := json.Unmarshal(finalErrorJSON, log.FinalError); err != nil {
				return nil, fmt.Errorf("unmarshal final_error: %w", err)
			}
		}

		logs = append(logs, &log)
	}

	return logs, rows.Err()
}

// CreateTable 创建表结构（用于初始化）
func CreateTable(db *sql.DB) error {
	query := `
		CREATE TABLE IF NOT EXISTS request_diagnostic_logs (
			id BIGSERIAL PRIMARY KEY,
			request_id VARCHAR(255) NOT NULL UNIQUE,
			session_id VARCHAR(255),
			tenant_id VARCHAR(255),
			client_model VARCHAR(255),
			
			start_time TIMESTAMP NOT NULL,
			end_time TIMESTAMP NOT NULL,
			final_status INT NOT NULL,
			success BOOLEAN NOT NULL,
			
			routing_decision JSONB,
			attempts JSONB NOT NULL,
			final_error JSONB,
			
			total_attempts INT NOT NULL,
			switches INT NOT NULL DEFAULT 0,
			retries INT NOT NULL DEFAULT 0,
			total_duration_ms BIGINT NOT NULL,
			
			created_at TIMESTAMP DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_diagnostic_request_id ON request_diagnostic_logs(request_id);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_session_id ON request_diagnostic_logs(session_id);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_tenant_id ON request_diagnostic_logs(tenant_id);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_start_time ON request_diagnostic_logs(start_time);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_success ON request_diagnostic_logs(success);
		CREATE INDEX IF NOT EXISTS idx_diagnostic_final_status ON request_diagnostic_logs(final_status);
	`

	_, err := db.Exec(query)
	return err
}
