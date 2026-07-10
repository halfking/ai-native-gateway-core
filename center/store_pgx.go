package center

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxStore PostgreSQL实现
type PgxStore struct {
	db *pgxpool.Pool
}

// NewPgxStore 创建PostgreSQL存储
func NewPgxStore(db *pgxpool.Pool) *PgxStore {
	return &PgxStore{db: db}
}

// RegisterInstance 注册实例
func (s *PgxStore) RegisterInstance(ctx context.Context, instance *InstanceInfo) error {
	query := `
		INSERT INTO gateway_instances (instance_id, hostname, ip_address, region, version, build_seq, status, started_at, last_heartbeat)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (instance_id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			ip_address = EXCLUDED.ip_address,
			region = EXCLUDED.region,
			version = EXCLUDED.version,
			build_seq = EXCLUDED.build_seq,
			started_at = EXCLUDED.started_at,
			last_heartbeat = now()
	`
	_, err := s.db.Exec(ctx, query,
		instance.InstanceID, instance.Hostname, instance.IPAddress, instance.Region,
		instance.Version, instance.BuildSeq, instance.Status, instance.StartedAt,
	)
	return err
}

// GetInstance 获取实例信息
func (s *PgxStore) GetInstance(ctx context.Context, instanceID string) (*InstanceInfo, error) {
	query := `
		SELECT instance_id, hostname, ip_address, region, version, build_seq, status, started_at, last_heartbeat
		FROM gateway_instances
		WHERE instance_id = $1
	`
	instance := &InstanceInfo{}
	err := s.db.QueryRow(ctx, query, instanceID).Scan(
		&instance.InstanceID, &instance.Hostname, &instance.IPAddress, &instance.Region,
		&instance.Version, &instance.BuildSeq, &instance.Status, &instance.StartedAt, &instance.LastHeartbeat,
	)
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// ListInstances 列出实例
func (s *PgxStore) ListInstances(ctx context.Context, status string, offset, limit int) ([]InstanceInfo, int, error) {
	// 查询总数
	var total int
	countQuery := `SELECT COUNT(*) FROM gateway_instances WHERE ($1 = '' OR status = $1)`
	if err := s.db.QueryRow(ctx, countQuery, status).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查询列表
	query := `
		SELECT instance_id, hostname, ip_address, region, version, build_seq, status, started_at, last_heartbeat
		FROM gateway_instances
		WHERE ($1 = '' OR status = $1)
		ORDER BY last_heartbeat DESC
		OFFSET $2 LIMIT $3
	`
	rows, err := s.db.Query(ctx, query, status, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var instances []InstanceInfo
	for rows.Next() {
		var instance InstanceInfo
		if err := rows.Scan(
			&instance.InstanceID, &instance.Hostname, &instance.IPAddress, &instance.Region,
			&instance.Version, &instance.BuildSeq, &instance.Status, &instance.StartedAt, &instance.LastHeartbeat,
		); err != nil {
			return nil, 0, err
		}
		instances = append(instances, instance)
	}

	return instances, total, nil
}

// UpdateInstanceStatus 更新实例状态
func (s *PgxStore) UpdateInstanceStatus(ctx context.Context, instanceID, status string) error {
	query := `UPDATE gateway_instances SET status = $2, last_heartbeat = now() WHERE instance_id = $1`
	_, err := s.db.Exec(ctx, query, instanceID, status)
	return err
}

// DeleteInstance 删除实例
func (s *PgxStore) DeleteInstance(ctx context.Context, instanceID string) error {
	query := `DELETE FROM gateway_instances WHERE instance_id = $1`
	_, err := s.db.Exec(ctx, query, instanceID)
	return err
}

// RecordHeartbeat 记录心跳
func (s *PgxStore) RecordHeartbeat(ctx context.Context, instanceID string, payload *HeartbeatPayload) error {
	// 更新实例最后心跳时间
	updateQuery := `UPDATE gateway_instances SET last_heartbeat = now(), status = $2 WHERE instance_id = $1`
	if _, err := s.db.Exec(ctx, updateQuery, instanceID, StatusOnline); err != nil {
		return err
	}

	// 记录心跳历史
	insertQuery := `
		INSERT INTO instance_heartbeats (instance_id, timestamp, uptime_secs, num_goroutine, alloc_mb, status)
		VALUES ($1, now(), $2, $3, $4, $5)
	`
	_, err := s.db.Exec(ctx, insertQuery,
		instanceID, payload.UptimeSecs, payload.NumGoroutine, payload.AllocMB, StatusOnline,
	)
	return err
}

// GetLastHeartbeat 获取最后心跳时间
func (s *PgxStore) GetLastHeartbeat(ctx context.Context, instanceID string) (time.Time, error) {
	query := `SELECT last_heartbeat FROM gateway_instances WHERE instance_id = $1`
	var lastHeartbeat time.Time
	err := s.db.QueryRow(ctx, query, instanceID).Scan(&lastHeartbeat)
	return lastHeartbeat, err
}

// GetHeartbeatHistory 获取心跳历史
func (s *PgxStore) GetHeartbeatHistory(ctx context.Context, instanceID string, since time.Time, limit int) ([]HeartbeatRecord, error) {
	query := `
		SELECT instance_id, timestamp, uptime_secs, num_goroutine, alloc_mb, status
		FROM instance_heartbeats
		WHERE instance_id = $1 AND timestamp >= $2
		ORDER BY timestamp DESC
		LIMIT $3
	`
	rows, err := s.db.Query(ctx, query, instanceID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []HeartbeatRecord
	for rows.Next() {
		var record HeartbeatRecord
		if err := rows.Scan(
			&record.InstanceID, &record.Timestamp, &record.UptimeSecs,
			&record.NumGoroutine, &record.AllocMB, &record.Status,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	return records, nil
}

// CreateCommand 创建命令
func (s *PgxStore) CreateCommand(ctx context.Context, cmd *Command) error {
	argsJSON, _ := json.Marshal(cmd.Args)
	query := `
		INSERT INTO center_commands (command_id, instance_id, command, args, status, issued_at, issued_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`
	return s.db.QueryRow(ctx, query,
		cmd.CommandID, cmd.InstanceID, cmd.Command, argsJSON, cmd.Status,
		cmd.IssuedAt, cmd.IssuedBy, cmd.ExpiresAt,
	).Scan(&cmd.ID)
}

// GetCommand 获取命令
func (s *PgxStore) GetCommand(ctx context.Context, commandID string) (*Command, error) {
	query := `
		SELECT id, command_id, instance_id, command, args, status, issued_at, issued_by, expires_at, executed_at, result
		FROM center_commands
		WHERE command_id = $1
	`
	cmd := &Command{}
	var argsJSON []byte
	var resultJSON []byte
	err := s.db.QueryRow(ctx, query, commandID).Scan(
		&cmd.ID, &cmd.CommandID, &cmd.InstanceID, &cmd.Command, &argsJSON,
		&cmd.Status, &cmd.IssuedAt, &cmd.IssuedBy, &cmd.ExpiresAt, &cmd.ExecutedAt, &resultJSON,
	)
	if err != nil {
		return nil, err
	}

	if len(argsJSON) > 0 {
		json.Unmarshal(argsJSON, &cmd.Args)
	}
	if len(resultJSON) > 0 {
		json.Unmarshal(resultJSON, &cmd.Result)
	}

	return cmd, nil
}

// ListPendingCommands 列出待执行命令
func (s *PgxStore) ListPendingCommands(ctx context.Context, instanceID string) ([]Command, error) {
	query := `
		SELECT id, command_id, instance_id, command, args, status, issued_at, issued_by, expires_at
		FROM center_commands
		WHERE instance_id = $1 AND status = $2
		ORDER BY issued_at ASC
	`
	rows, err := s.db.Query(ctx, query, instanceID, CommandStatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []Command
	for rows.Next() {
		var cmd Command
		var argsJSON []byte
		if err := rows.Scan(
			&cmd.ID, &cmd.CommandID, &cmd.InstanceID, &cmd.Command, &argsJSON,
			&cmd.Status, &cmd.IssuedAt, &cmd.IssuedBy, &cmd.ExpiresAt,
		); err != nil {
			return nil, err
		}
		if len(argsJSON) > 0 {
			json.Unmarshal(argsJSON, &cmd.Args)
		}
		commands = append(commands, cmd)
	}

	return commands, nil
}

// UpdateCommandStatus 更新命令状态
func (s *PgxStore) UpdateCommandStatus(ctx context.Context, commandID, status string, result *CommandResult) error {
	resultJSON, _ := json.Marshal(result)
	query := `
		UPDATE center_commands
		SET status = $2, executed_at = now(), result = $3
		WHERE command_id = $1
	`
	_, err := s.db.Exec(ctx, query, commandID, status, resultJSON)
	return err
}

// GetCommandHistory 获取命令历史
func (s *PgxStore) GetCommandHistory(ctx context.Context, instanceID string, limit int) ([]Command, error) {
	query := `
		SELECT id, command_id, instance_id, command, args, status, issued_at, issued_by, expires_at, executed_at, result
		FROM center_commands
		WHERE instance_id = $1
		ORDER BY issued_at DESC
		LIMIT $2
	`
	rows, err := s.db.Query(ctx, query, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []Command
	for rows.Next() {
		var cmd Command
		var argsJSON []byte
		var resultJSON []byte
		if err := rows.Scan(
			&cmd.ID, &cmd.CommandID, &cmd.InstanceID, &cmd.Command, &argsJSON,
			&cmd.Status, &cmd.IssuedAt, &cmd.IssuedBy, &cmd.ExpiresAt, &cmd.ExecutedAt, &resultJSON,
		); err != nil {
			return nil, err
		}
		if len(argsJSON) > 0 {
			json.Unmarshal(argsJSON, &cmd.Args)
		}
		if len(resultJSON) > 0 {
			json.Unmarshal(resultJSON, &cmd.Result)
		}
		commands = append(commands, cmd)
	}

	return commands, nil
}

// RecordStatusReport 记录状态报告
func (s *PgxStore) RecordStatusReport(ctx context.Context, instanceID string, payload *StatusReportPayload) error {
	query := `
		INSERT INTO instance_status_reports (instance_id, timestamp, state, active_licenses, active_devices,
		                                     requests_total, requests_ok, requests_err, avg_latency_ms, p99_latency_ms)
		VALUES ($1, now(), $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := s.db.Exec(ctx, query,
		instanceID, payload.State, payload.ActiveLicenses, payload.ActiveDevices,
		payload.RequestsTotal, payload.RequestsOk, payload.RequestsErr,
		payload.AvgLatencyMs, payload.P99LatencyMs,
	)
	return err
}

// GetLatestStatus 获取最新状态
func (s *PgxStore) GetLatestStatus(ctx context.Context, instanceID string) (*StatusReportPayload, error) {
	query := `
		SELECT state, active_licenses, active_devices, requests_total, requests_ok, requests_err, avg_latency_ms, p99_latency_ms
		FROM instance_status_reports
		WHERE instance_id = $1
		ORDER BY timestamp DESC
		LIMIT 1
	`
	status := &StatusReportPayload{}
	err := s.db.QueryRow(ctx, query, instanceID).Scan(
		&status.State, &status.ActiveLicenses, &status.ActiveDevices,
		&status.RequestsTotal, &status.RequestsOk, &status.RequestsErr,
		&status.AvgLatencyMs, &status.P99LatencyMs,
	)
	if err != nil {
		return nil, err
	}
	return status, nil
}

// Ensure interface compliance
var _ Store = (*PgxStore)(nil)
