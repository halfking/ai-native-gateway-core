package clientprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Store 客户端画像存储接口
type Store interface {
	// GetProfile 获取客户端画像
	GetProfile(ctx context.Context, identityHash string) (*ClientProfile, error)

	// UpsertProfile 插入或更新客户端画像
	UpsertProfile(ctx context.Context, profile *ClientProfile) error

	// ListProfiles 列出租户的客户端画像（分页）
	ListProfiles(ctx context.Context, tenantID string, limit, offset int) ([]*ProfileSummary, error)

	// CountProfiles 统计租户的客户端数量
	CountProfiles(ctx context.Context, tenantID string) (int64, error)

	// SaveEvent 保存客户端行为事件
	SaveEvent(ctx context.Context, event *ClientBehaviorEvent) error

	// GetEvents 获取客户端行为事件（时间范围查询）
	GetEvents(ctx context.Context, identityHash string, startTime, endTime time.Time) ([]*ClientBehaviorEvent, error)
}

// PostgresStore PostgreSQL实现
type PostgresStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewPostgresStore 创建PostgreSQL存储
func NewPostgresStore(db *sql.DB, logger *slog.Logger) *PostgresStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &PostgresStore{
		db:     db,
		logger: logger,
	}
}

// GetProfile 获取客户端画像
func (s *PostgresStore) GetProfile(ctx context.Context, identityHash string) (*ClientProfile, error) {
	query := `
		SELECT identity_hash, tenant_id, virtual_client_id,
		       total_sessions, total_requests, first_seen_at, last_seen_at,
		       preferred_models, task_distribution, avg_session_length, avg_tokens_per_turn,
		       error_rate, approval_rate, active_hours, peak_usage_day, updated_at
		FROM client_profiles
		WHERE identity_hash = $1
	`

	var profile ClientProfile
	var preferredModelsJSON, taskDistJSON, activeHoursJSON []byte
	var peakUsageDay sql.NullInt32

	err := s.db.QueryRowContext(ctx, query, identityHash).Scan(
		&profile.IdentityHash, &profile.TenantID, &profile.VirtualClientID,
		&profile.TotalSessions, &profile.TotalRequests, &profile.FirstSeenAt, &profile.LastSeenAt,
		&preferredModelsJSON, &taskDistJSON, &profile.AvgSessionLength, &profile.AvgTokensPerTurn,
		&profile.ErrorRate, &profile.ApprovalRate, &activeHoursJSON, &peakUsageDay, &profile.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query profile: %w", err)
	}

	// 解析JSONB字段
	if len(preferredModelsJSON) > 0 {
		if err := json.Unmarshal(preferredModelsJSON, &profile.PreferredModels); err != nil {
			s.logger.Warn("failed to unmarshal preferred_models", "error", err)
		}
	}

	if len(taskDistJSON) > 0 {
		if err := json.Unmarshal(taskDistJSON, &profile.TaskDistribution); err != nil {
			s.logger.Warn("failed to unmarshal task_distribution", "error", err)
		}
	}

	if len(activeHoursJSON) > 0 {
		if err := json.Unmarshal(activeHoursJSON, &profile.ActiveHours); err != nil {
			s.logger.Warn("failed to unmarshal active_hours", "error", err)
		}
	}

	if peakUsageDay.Valid {
		profile.PeakUsageDay = time.Weekday(peakUsageDay.Int32)
	}

	return &profile, nil
}

// UpsertProfile 插入或更新客户端画像
func (s *PostgresStore) UpsertProfile(ctx context.Context, profile *ClientProfile) error {
	// 序列化JSONB字段
	preferredModelsJSON, err := json.Marshal(profile.PreferredModels)
	if err != nil {
		return fmt.Errorf("marshal preferred_models: %w", err)
	}

	taskDistJSON, err := json.Marshal(profile.TaskDistribution)
	if err != nil {
		return fmt.Errorf("marshal task_distribution: %w", err)
	}

	activeHoursJSON, err := json.Marshal(profile.ActiveHours)
	if err != nil {
		return fmt.Errorf("marshal active_hours: %w", err)
	}

	query := `
		INSERT INTO client_profiles (
			identity_hash, tenant_id, virtual_client_id,
			total_sessions, total_requests, first_seen_at, last_seen_at,
			preferred_models, task_distribution, avg_session_length, avg_tokens_per_turn,
			error_rate, approval_rate, active_hours, peak_usage_day, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (identity_hash) DO UPDATE SET
			total_sessions = EXCLUDED.total_sessions,
			total_requests = EXCLUDED.total_requests,
			last_seen_at = EXCLUDED.last_seen_at,
			preferred_models = EXCLUDED.preferred_models,
			task_distribution = EXCLUDED.task_distribution,
			avg_session_length = EXCLUDED.avg_session_length,
			avg_tokens_per_turn = EXCLUDED.avg_tokens_per_turn,
			error_rate = EXCLUDED.error_rate,
			approval_rate = EXCLUDED.approval_rate,
			active_hours = EXCLUDED.active_hours,
			peak_usage_day = EXCLUDED.peak_usage_day,
			updated_at = EXCLUDED.updated_at
	`

	_, err = s.db.ExecContext(ctx, query,
		profile.IdentityHash, profile.TenantID, profile.VirtualClientID,
		profile.TotalSessions, profile.TotalRequests, profile.FirstSeenAt, profile.LastSeenAt,
		preferredModelsJSON, taskDistJSON, profile.AvgSessionLength, profile.AvgTokensPerTurn,
		profile.ErrorRate, profile.ApprovalRate, activeHoursJSON, int(profile.PeakUsageDay), profile.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}

	return nil
}

// ListProfiles 列出租户的客户端画像（分页）
func (s *PostgresStore) ListProfiles(ctx context.Context, tenantID string, limit, offset int) ([]*ProfileSummary, error) {
	query := `
		SELECT identity_hash, virtual_client_id, total_sessions, total_requests, last_seen_at,
		       preferred_models, task_distribution, error_rate
		FROM client_profiles
		WHERE tenant_id = $1
		ORDER BY last_seen_at DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := s.db.QueryContext(ctx, query, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query profiles: %w", err)
	}
	defer rows.Close()

	var summaries []*ProfileSummary
	for rows.Next() {
		var summary ProfileSummary
		var preferredModelsJSON, taskDistJSON []byte

		if err := rows.Scan(
			&summary.IdentityHash, &summary.VirtualClientID,
			&summary.TotalSessions, &summary.TotalRequests, &summary.LastSeenAt,
			&preferredModelsJSON, &taskDistJSON, &summary.ErrorRate,
		); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}

		// 提取TopModel
		if len(preferredModelsJSON) > 0 {
			var models []ModelPreference
			if err := json.Unmarshal(preferredModelsJSON, &models); err == nil && len(models) > 0 {
				summary.TopModel = models[0].ModelName
			}
		}

		// 提取PrimaryTaskType
		if len(taskDistJSON) > 0 {
			var taskDist map[string]int64
			if err := json.Unmarshal(taskDistJSON, &taskDist); err == nil {
				maxCount := int64(0)
				for taskType, count := range taskDist {
					if count > maxCount {
						maxCount = count
						summary.PrimaryTaskType = taskType
					}
				}
			}
		}

		summaries = append(summaries, &summary)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profiles: %w", err)
	}

	return summaries, nil
}

// CountProfiles 统计租户的客户端数量
func (s *PostgresStore) CountProfiles(ctx context.Context, tenantID string) (int64, error) {
	query := `SELECT COUNT(*) FROM client_profiles WHERE tenant_id = $1`

	var count int64
	err := s.db.QueryRowContext(ctx, query, tenantID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count profiles: %w", err)
	}

	return count, nil
}

// SaveEvent 保存客户端行为事件
func (s *PostgresStore) SaveEvent(ctx context.Context, event *ClientBehaviorEvent) error {
	query := `
		INSERT INTO client_behavior_events (
			event_id, identity_hash, tenant_id, session_id, request_id,
			event_type, model, task_type, tokens_used, latency_ms, success, event_time
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`

	_, err := s.db.ExecContext(ctx, query,
		event.EventID, event.IdentityHash, event.TenantID, event.SessionID, event.RequestID,
		event.EventType, event.Model, event.TaskType, event.TokensUsed, event.LatencyMs,
		event.Success, event.Timestamp,
	)

	if err != nil {
		return fmt.Errorf("save event: %w", err)
	}

	return nil
}

// GetEvents 获取客户端行为事件（时间范围查询）
func (s *PostgresStore) GetEvents(ctx context.Context, identityHash string, startTime, endTime time.Time) ([]*ClientBehaviorEvent, error) {
	query := `
		SELECT event_id, identity_hash, tenant_id, session_id, request_id,
		       event_type, model, task_type, tokens_used, latency_ms, success, event_time
		FROM client_behavior_events
		WHERE identity_hash = $1 AND event_time >= $2 AND event_time <= $3
		ORDER BY event_time DESC
	`

	rows, err := s.db.QueryContext(ctx, query, identityHash, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []*ClientBehaviorEvent
	for rows.Next() {
		var event ClientBehaviorEvent
		if err := rows.Scan(
			&event.EventID, &event.IdentityHash, &event.TenantID, &event.SessionID, &event.RequestID,
			&event.EventType, &event.Model, &event.TaskType, &event.TokensUsed, &event.LatencyMs,
			&event.Success, &event.Timestamp,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, &event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	return events, nil
}
