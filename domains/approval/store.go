package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ApprovalStore defines the interface for persisting approval data.
type ApprovalStore interface {
	// Approval request operations
	CreateRequest(ctx context.Context, req *ApprovalRequest) error
	GetRequest(ctx context.Context, requestID string) (*ApprovalRequest, error)
	UpdateRequest(ctx context.Context, req *ApprovalRequest) error
	ListRequests(ctx context.Context, filter ApprovalFilter) ([]*ApprovalRequest, error)

	// Configuration operations
	GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error)
	SaveConfig(ctx context.Context, config *ApprovalConfig) error

	// Approver operations
	GetApprovers(ctx context.Context, tenantID string) ([]Approver, error)
	SaveApprover(ctx context.Context, tenantID string, approver *Approver) error
	DeleteApprover(ctx context.Context, tenantID string, userID string) error

	// Rule operations
	GetRules(ctx context.Context, tenantID string) ([]ApprovalRule, error)
	SaveRule(ctx context.Context, tenantID string, rule *ApprovalRule) error
	DeleteRule(ctx context.Context, tenantID string, ruleName string) error
}

// pgApprovalStore is the PostgreSQL implementation of ApprovalStore.
type pgApprovalStore struct {
	pool  *pgxpool.Pool
	cache *redis.Client
}

// NewPGApprovalStore creates a new PostgreSQL-backed approval store.
func NewPGApprovalStore(pool *pgxpool.Pool, cache *redis.Client) ApprovalStore {
	return &pgApprovalStore{
		pool:  pool,
		cache: cache,
	}
}

var (
	ErrRequestNotFound = errors.New("approval request not found")
	ErrConfigNotFound  = errors.New("approval config not found")
	ErrInvalidRequest  = errors.New("invalid approval request")
)

// CreateRequest inserts a new approval request.
func (s *pgApprovalStore) CreateRequest(ctx context.Context, req *ApprovalRequest) error {
	if req.RequestID == "" || req.SessionID == "" || req.TenantID == "" {
		return ErrInvalidRequest
	}

	sessionSummaryJSON, err := json.Marshal(req.SessionSummary)
	if err != nil {
		return fmt.Errorf("marshal session_summary: %w", err)
	}

	sensitiveInfoJSON, err := json.Marshal(req.SensitiveInfo)
	if err != nil {
		return fmt.Errorf("marshal sensitive_info: %w", err)
	}

	metadataJSON, err := json.Marshal(req.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	const sql = `
		INSERT INTO approval_requests (
			request_id, session_id, tenant_id,
			trigger_type, trigger_reason, risk_level,
			session_summary, sensitive_info, user_message, full_context,
			estimated_cost, estimated_tokens,
			status, created_at, expires_at,
			metadata
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9, $10,
			$11, $12,
			$13, $14, $15,
			$16
		)
	`

	_, err = s.pool.Exec(ctx, sql,
		req.RequestID, req.SessionID, req.TenantID,
		string(req.TriggerType), req.TriggerReason, string(req.RiskLevel),
		sessionSummaryJSON, sensitiveInfoJSON, req.UserMessage, req.FullContext,
		req.EstimatedCost, req.EstimatedTokens,
		string(req.Status), req.CreatedAt, req.ExpiresAt,
		metadataJSON,
	)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return fmt.Errorf("request_id already exists: %w", err)
		}
		return fmt.Errorf("insert approval_request: %w", err)
	}

	// Cache the request for fast lookup
	if s.cache != nil {
		s.cacheRequest(ctx, req)
	}

	return nil
}

// GetRequest retrieves an approval request by ID.
func (s *pgApprovalStore) GetRequest(ctx context.Context, requestID string) (*ApprovalRequest, error) {
	// Try cache first
	if s.cache != nil {
		if req, err := s.getCachedRequest(ctx, requestID); err == nil {
			return req, nil
		}
	}

	const sql = `
		SELECT 
			request_id, session_id, tenant_id,
			trigger_type, trigger_reason, risk_level,
			session_summary, sensitive_info, user_message, full_context,
			estimated_cost, estimated_tokens,
			status, created_at, expires_at,
			COALESCE(approved_by, ''), COALESCE(approved_at, '0001-01-01'::timestamptz), 
			COALESCE(approval_note, ''),
			COALESCE(rejected, false), COALESCE(rejection_reason, ''),
			COALESCE(metadata, '{}'::jsonb)
		FROM approval_requests
		WHERE request_id = $1
	`

	var req ApprovalRequest
	var sessionSummaryJSON, sensitiveInfoJSON, metadataJSON []byte
	var fullContext []byte
	var approvedAt time.Time

	err := s.pool.QueryRow(ctx, sql, requestID).Scan(
		&req.RequestID, &req.SessionID, &req.TenantID,
		&req.TriggerType, &req.TriggerReason, &req.RiskLevel,
		&sessionSummaryJSON, &sensitiveInfoJSON, &req.UserMessage, &fullContext,
		&req.EstimatedCost, &req.EstimatedTokens,
		&req.Status, &req.CreatedAt, &req.ExpiresAt,
		&req.ApprovedBy, &approvedAt, &req.ApprovalNote,
		&req.Rejected, &req.RejectionReason,
		&metadataJSON,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, fmt.Errorf("query approval_request: %w", err)
	}

	// Unmarshal JSON fields
	if err := json.Unmarshal(sessionSummaryJSON, &req.SessionSummary); err != nil {
		return nil, fmt.Errorf("unmarshal session_summary: %w", err)
	}
	if len(sensitiveInfoJSON) > 0 {
		if err := json.Unmarshal(sensitiveInfoJSON, &req.SensitiveInfo); err != nil {
			return nil, fmt.Errorf("unmarshal sensitive_info: %w", err)
		}
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &req.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	req.FullContext = fullContext

	if !approvedAt.IsZero() && approvedAt.Year() > 1 {
		req.ApprovedAt = approvedAt
	}

	// Cache for next time
	if s.cache != nil {
		s.cacheRequest(ctx, &req)
	}

	return &req, nil
}

// UpdateRequest updates an existing approval request.
func (s *pgApprovalStore) UpdateRequest(ctx context.Context, req *ApprovalRequest) error {
	if req.RequestID == "" {
		return ErrInvalidRequest
	}

	metadataJSON, err := json.Marshal(req.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	const sql = `
		UPDATE approval_requests SET
			status = $2,
			approved_by = $3,
			approved_at = $4,
			approval_note = $5,
			rejected = $6,
			rejection_reason = $7,
			metadata = $8
		WHERE request_id = $1
	`

	approvedAt := pgNullTime(req.ApprovedAt)

	result, err := s.pool.Exec(ctx, sql,
		req.RequestID,
		string(req.Status),
		pgNullString(req.ApprovedBy),
		approvedAt,
		pgNullString(req.ApprovalNote),
		req.Rejected,
		pgNullString(req.RejectionReason),
		metadataJSON,
	)

	if err != nil {
		return fmt.Errorf("update approval_request: %w", err)
	}

	if result.RowsAffected() == 0 {
		return ErrRequestNotFound
	}

	// Invalidate cache
	if s.cache != nil {
		s.invalidateCache(ctx, req.RequestID)
	}

	return nil
}

// ListRequests returns approval requests matching the filter.
func (s *pgApprovalStore) ListRequests(ctx context.Context, filter ApprovalFilter) ([]*ApprovalRequest, error) {
	query := `
		SELECT 
			request_id, session_id, tenant_id,
			trigger_type, trigger_reason, risk_level,
			session_summary, sensitive_info, user_message,
			estimated_cost, estimated_tokens,
			status, created_at, expires_at,
			COALESCE(approved_by, ''), COALESCE(approved_at, '0001-01-01'::timestamptz),
			COALESCE(approval_note, ''),
			COALESCE(rejected, false), COALESCE(rejection_reason, ''),
			COALESCE(metadata, '{}'::jsonb)
		FROM approval_requests
		WHERE 1=1
	`
	args := []interface{}{}
	argPos := 1

	if filter.TenantID != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argPos)
		args = append(args, filter.TenantID)
		argPos++
	}

	if filter.Status != "" {
		query += fmt.Sprintf(" AND status = $%d", argPos)
		args = append(args, string(filter.Status))
		argPos++
	}

	if filter.RiskLevel != "" {
		query += fmt.Sprintf(" AND risk_level = $%d", argPos)
		args = append(args, string(filter.RiskLevel))
		argPos++
	}

	if filter.TriggerType != "" {
		query += fmt.Sprintf(" AND trigger_type = $%d", argPos)
		args = append(args, string(filter.TriggerType))
		argPos++
	}

	if !filter.FromDate.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argPos)
		args = append(args, filter.FromDate)
		argPos++
	}

	if !filter.ToDate.IsZero() {
		query += fmt.Sprintf(" AND created_at <= $%d", argPos)
		args = append(args, filter.ToDate)
		argPos++
	}

	query += " ORDER BY created_at DESC"

	limit := filter.Limit
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query += fmt.Sprintf(" LIMIT $%d", argPos)
	args = append(args, limit)
	argPos++

	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argPos)
		args = append(args, filter.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query approval_requests: %w", err)
	}
	defer rows.Close()

	var requests []*ApprovalRequest
	for rows.Next() {
		var req ApprovalRequest
		var sessionSummaryJSON, sensitiveInfoJSON, metadataJSON []byte
		var approvedAt time.Time

		err := rows.Scan(
			&req.RequestID, &req.SessionID, &req.TenantID,
			&req.TriggerType, &req.TriggerReason, &req.RiskLevel,
			&sessionSummaryJSON, &sensitiveInfoJSON, &req.UserMessage,
			&req.EstimatedCost, &req.EstimatedTokens,
			&req.Status, &req.CreatedAt, &req.ExpiresAt,
			&req.ApprovedBy, &approvedAt, &req.ApprovalNote,
			&req.Rejected, &req.RejectionReason,
			&metadataJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("scan approval_request: %w", err)
		}

		// Unmarshal JSON fields
		if err := json.Unmarshal(sessionSummaryJSON, &req.SessionSummary); err != nil {
			return nil, fmt.Errorf("unmarshal session_summary: %w", err)
		}
		if len(sensitiveInfoJSON) > 0 {
			if err := json.Unmarshal(sensitiveInfoJSON, &req.SensitiveInfo); err != nil {
				return nil, fmt.Errorf("unmarshal sensitive_info: %w", err)
			}
		}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &req.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal metadata: %w", err)
			}
		}

		if !approvedAt.IsZero() && approvedAt.Year() > 1 {
			req.ApprovedAt = approvedAt
		}

		requests = append(requests, &req)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return requests, nil
}

// GetConfig retrieves approval configuration for a tenant.
func (s *pgApprovalStore) GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error) {
	// Try cache first
	if s.cache != nil {
		if config, err := s.getCachedConfig(ctx, tenantID); err == nil {
			return config, nil
		}
	}

	const sql = `
		SELECT 
			tenant_id, enabled, mode, timeout_seconds, auto_reject_on_timeout,
			config, created_at, updated_at
		FROM approval_configs
		WHERE tenant_id = $1
	`

	var config ApprovalConfig
	var configJSON []byte

	err := s.pool.QueryRow(ctx, sql, tenantID).Scan(
		&config.TenantID, &config.Enabled, &config.Mode,
		&config.TimeoutSeconds, &config.AutoRejectOnTimeout,
		&configJSON, &config.CreatedAt, &config.UpdatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Return default disabled config
			return &ApprovalConfig{
				TenantID:            tenantID,
				Enabled:             false,
				Mode:                ModeDisabled,
				TimeoutSeconds:      3600,
				AutoRejectOnTimeout: true,
				Approvers:           []Approver{},
				Channels:            []NotificationChannel{},
				Rules:               []ApprovalRule{},
			}, nil
		}
		return nil, fmt.Errorf("query approval_config: %w", err)
	}

	// Parse the full config JSON
	var fullConfig struct {
		Approvers []Approver            `json:"approvers"`
		Channels  []NotificationChannel `json:"channels"`
		Rules     []ApprovalRule        `json:"rules"`
	}
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &fullConfig); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	}

	config.Approvers = fullConfig.Approvers
	config.Channels = fullConfig.Channels
	config.Rules = fullConfig.Rules

	// Cache for next time
	if s.cache != nil {
		s.cacheConfig(ctx, &config)
	}

	return &config, nil
}

// SaveConfig persists approval configuration for a tenant.
func (s *pgApprovalStore) SaveConfig(ctx context.Context, config *ApprovalConfig) error {
	if config.TenantID == "" {
		return fmt.Errorf("tenant_id is required")
	}

	// Build the full config JSON
	fullConfig := map[string]interface{}{
		"approvers": config.Approvers,
		"channels":  config.Channels,
		"rules":     config.Rules,
	}
	configJSON, err := json.Marshal(fullConfig)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	const sql = `
		INSERT INTO approval_configs (
			tenant_id, enabled, mode, timeout_seconds, auto_reject_on_timeout, config
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			mode = EXCLUDED.mode,
			timeout_seconds = EXCLUDED.timeout_seconds,
			auto_reject_on_timeout = EXCLUDED.auto_reject_on_timeout,
			config = EXCLUDED.config,
			updated_at = now()
		RETURNING created_at, updated_at
	`

	err = s.pool.QueryRow(ctx, sql,
		config.TenantID, config.Enabled, string(config.Mode),
		config.TimeoutSeconds, config.AutoRejectOnTimeout, configJSON,
	).Scan(&config.CreatedAt, &config.UpdatedAt)

	if err != nil {
		return fmt.Errorf("upsert approval_config: %w", err)
	}

	// Invalidate cache
	if s.cache != nil {
		s.invalidateConfigCache(ctx, config.TenantID)
	}

	return nil
}

// GetApprovers retrieves all approvers for a tenant.
func (s *pgApprovalStore) GetApprovers(ctx context.Context, tenantID string) ([]Approver, error) {
	const sql = `
		SELECT user_id, name, COALESCE(email, ''), COALESCE(phone, ''), role, priority, enabled
		FROM approval_approvers
		WHERE tenant_id = $1 AND enabled = true
		ORDER BY priority ASC
	`

	rows, err := s.pool.Query(ctx, sql, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query approvers: %w", err)
	}
	defer rows.Close()

	var approvers []Approver
	for rows.Next() {
		var a Approver
		if err := rows.Scan(&a.UserID, &a.Name, &a.Email, &a.Phone, &a.Role, &a.Priority, &a.Enabled); err != nil {
			return nil, fmt.Errorf("scan approver: %w", err)
		}
		approvers = append(approvers, a)
	}

	return approvers, rows.Err()
}

// SaveApprover persists an approver record.
func (s *pgApprovalStore) SaveApprover(ctx context.Context, tenantID string, approver *Approver) error {
	const sql = `
		INSERT INTO approval_approvers (tenant_id, user_id, name, email, phone, role, priority, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, user_id) DO UPDATE SET
			name = EXCLUDED.name,
			email = EXCLUDED.email,
			phone = EXCLUDED.phone,
			role = EXCLUDED.role,
			priority = EXCLUDED.priority,
			enabled = EXCLUDED.enabled,
			updated_at = now()
	`

	_, err := s.pool.Exec(ctx, sql,
		tenantID, approver.UserID, approver.Name,
		pgNullString(approver.Email), pgNullString(approver.Phone),
		approver.Role, approver.Priority, approver.Enabled,
	)

	return err
}

// DeleteApprover removes an approver.
func (s *pgApprovalStore) DeleteApprover(ctx context.Context, tenantID string, userID string) error {
	const sql = `DELETE FROM approval_approvers WHERE tenant_id = $1 AND user_id = $2`
	_, err := s.pool.Exec(ctx, sql, tenantID, userID)
	return err
}

// GetRules retrieves all rules for a tenant.
func (s *pgApprovalStore) GetRules(ctx context.Context, tenantID string) ([]ApprovalRule, error) {
	const sql = `
		SELECT name, enabled, priority, conditions, action
		FROM approval_rules
		WHERE tenant_id = $1 AND enabled = true
		ORDER BY priority DESC
	`

	rows, err := s.pool.Query(ctx, sql, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	defer rows.Close()

	var rules []ApprovalRule
	for rows.Next() {
		var rule ApprovalRule
		var conditionsJSON, actionJSON []byte

		if err := rows.Scan(&rule.Name, &rule.Enabled, &rule.Priority, &conditionsJSON, &actionJSON); err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}

		if err := json.Unmarshal(conditionsJSON, &rule.Conditions); err != nil {
			return nil, fmt.Errorf("unmarshal conditions: %w", err)
		}
		if err := json.Unmarshal(actionJSON, &rule.Action); err != nil {
			return nil, fmt.Errorf("unmarshal action: %w", err)
		}

		rules = append(rules, rule)
	}

	return rules, rows.Err()
}

// SaveRule persists a rule record.
func (s *pgApprovalStore) SaveRule(ctx context.Context, tenantID string, rule *ApprovalRule) error {
	conditionsJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return fmt.Errorf("marshal conditions: %w", err)
	}

	actionJSON, err := json.Marshal(rule.Action)
	if err != nil {
		return fmt.Errorf("marshal action: %w", err)
	}

	const sql = `
		INSERT INTO approval_rules (tenant_id, name, enabled, priority, conditions, action)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, name) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			priority = EXCLUDED.priority,
			conditions = EXCLUDED.conditions,
			action = EXCLUDED.action,
			updated_at = now()
	`

	_, err = s.pool.Exec(ctx, sql, tenantID, rule.Name, rule.Enabled, rule.Priority, conditionsJSON, actionJSON)
	return err
}

// DeleteRule removes a rule.
func (s *pgApprovalStore) DeleteRule(ctx context.Context, tenantID string, ruleName string) error {
	const sql = `DELETE FROM approval_rules WHERE tenant_id = $1 AND name = $2`
	_, err := s.pool.Exec(ctx, sql, tenantID, ruleName)
	return err
}

// Cache helper functions

func (s *pgApprovalStore) cacheRequest(ctx context.Context, req *ApprovalRequest) {
	if s.cache == nil {
		return
	}
	data, err := json.Marshal(req)
	if err != nil {
		return
	}
	key := fmt.Sprintf("approval:request:%s", req.RequestID)
	s.cache.Set(ctx, key, data, 10*time.Minute)
}

func (s *pgApprovalStore) getCachedRequest(ctx context.Context, requestID string) (*ApprovalRequest, error) {
	key := fmt.Sprintf("approval:request:%s", requestID)
	data, err := s.cache.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var req ApprovalRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *pgApprovalStore) invalidateCache(ctx context.Context, requestID string) {
	if s.cache == nil {
		return
	}
	key := fmt.Sprintf("approval:request:%s", requestID)
	s.cache.Del(ctx, key)
}

func (s *pgApprovalStore) cacheConfig(ctx context.Context, config *ApprovalConfig) {
	if s.cache == nil {
		return
	}
	data, err := json.Marshal(config)
	if err != nil {
		return
	}
	key := fmt.Sprintf("approval:config:%s", config.TenantID)
	s.cache.Set(ctx, key, data, 30*time.Minute)
}

func (s *pgApprovalStore) getCachedConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error) {
	key := fmt.Sprintf("approval:config:%s", tenantID)
	data, err := s.cache.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var config ApprovalConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *pgApprovalStore) invalidateConfigCache(ctx context.Context, tenantID string) {
	if s.cache == nil {
		return
	}
	key := fmt.Sprintf("approval:config:%s", tenantID)
	s.cache.Del(ctx, key)
}

// Helper functions for nullable SQL types

func pgNullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func pgNullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}
