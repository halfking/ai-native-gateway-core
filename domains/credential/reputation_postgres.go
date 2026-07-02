// Package credential - PostgreSQL-backed ReputationStore implementation.
//
// Mirrors migrations/133_provider_reputation.sql:
//   - provider_reputation_timeseries (daily aggregates, ON CONFLICT upsert)
//   - provider_incidents (event log, soft-resolved)
//
// Enabled() returns false if the pool is nil; all methods become no-ops in
// that case so callers can compose with InMemoryReputationStore safely.
package credential

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReputationDB 抽象 PG 接口，方便通过 pgxmock 注入（仅用于单元测试）。
// 生产环境传入 *pgxpool.Pool。
type ReputationDB interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

// PostgresReputationStore 基于 PostgreSQL 的存储实现
type PostgresReputationStore struct {
	pool *pgxpool.Pool
	db   ReputationDB // 测试时可注入；nil 时回落到 pool
}

// NewPostgresReputationStore 创建 PG 存储
func NewPostgresReputationStore(pool *pgxpool.Pool) *PostgresReputationStore {
	return &PostgresReputationStore{pool: pool}
}

// newPostgresReputationStoreWithDB 测试用：通过 mock DB 注入
func newPostgresReputationStoreWithDB(db ReputationDB) *PostgresReputationStore {
	return &PostgresReputationStore{db: db}
}

// Enabled 是否可用
func (s *PostgresReputationStore) Enabled() bool {
	if s == nil {
		return false
	}
	if s.db != nil {
		return true
	}
	return s.pool != nil
}

// exec / query / queryRow 在 db 存在时优先用 db，否则回落到 pool
func (s *PostgresReputationStore) exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	if s.db != nil {
		return s.db.Exec(ctx, sql, args...)
	}
	if s.pool == nil {
		return pgconn.CommandTag{}, ErrNoDatabase
	}
	return s.pool.Exec(ctx, sql, args...)
}

func (s *PostgresReputationStore) query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if s.db != nil {
		return s.db.Query(ctx, sql, args...)
	}
	if s.pool == nil {
		return nil, ErrNoDatabase
	}
	return s.pool.Query(ctx, sql, args...)
}

func (s *PostgresReputationStore) queryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if s.db != nil {
		return s.db.QueryRow(ctx, sql, args...)
	}
	if s.pool == nil {
		// 返回一个永远报 ErrNoDatabase 的 pgx.Row
		return errorRow{err: ErrNoDatabase}
	}
	return s.pool.QueryRow(ctx, sql, args...)
}

// errorRow 是 pgx.Row 的最小占位实现（仅在 db/pool 都为 nil 时使用）
type errorRow struct{ err error }

func (r errorRow) Scan(_ ...interface{}) error { return r.err }

// ---------------------------------------------------------------------------
// Timeseries
// ---------------------------------------------------------------------------

// GetTimeseries 获取时序数据
func (s *PostgresReputationStore) GetTimeseries(ctx context.Context, providerID, model string, days int) ([]TimeseriesRow, error) {
	if !s.Enabled() {
		return nil, nil
	}
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -days+1)

	rows, err := s.query(ctx, `
		SELECT id, provider_id, model, date,
		       reliability_score, avg_latency_ms, error_rate,
		       request_count, success_count,
		       bandit_alpha, bandit_beta, success_rate,
		       rate_limit_errors, quota_errors, auth_errors,
		       timeout_errors, network_errors, other_errors,
		       created_at, updated_at
		FROM provider_reputation_timeseries
		WHERE provider_id = $1 AND model = $2 AND date >= $3
		ORDER BY date ASC
	`, providerID, model, cutoff)
	if err != nil {
		return nil, fmt.Errorf("reputation: GetTimeseries query: %w", err)
	}
	defer rows.Close()

	out := make([]TimeseriesRow, 0, days)
	for rows.Next() {
		var r TimeseriesRow
		if err := rows.Scan(
			&r.ID, &r.ProviderID, &r.Model, &r.Date,
			&r.ReliabilityScore, &r.AvgLatencyMs, &r.ErrorRate,
			&r.RequestCount, &r.SuccessCount,
			&r.BanditAlpha, &r.BanditBeta, &r.SuccessRate,
			&r.RateLimitErrors, &r.QuotaErrors, &r.AuthErrors,
			&r.TimeoutErrors, &r.NetworkErrors, &r.OtherErrors,
			&r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("reputation: GetTimeseries scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reputation: GetTimeseries rows: %w", err)
	}
	return out, nil
}

// SaveTimeseries 保存每日指标（ON CONFLICT upsert）
func (s *PostgresReputationStore) SaveTimeseries(ctx context.Context, row *TimeseriesRow) error {
	if !s.Enabled() {
		return ErrNoDatabase
	}
	if row == nil || row.ProviderID == "" || row.Model == "" {
		return errors.New("credential: SaveTimeseries requires provider_id and model")
	}
	row.Date = row.Date.UTC().Truncate(24 * time.Hour)

	_, err := s.exec(ctx, `
		INSERT INTO provider_reputation_timeseries (
			provider_id, model, date,
			reliability_score, avg_latency_ms, error_rate,
			request_count, success_count,
			bandit_alpha, bandit_beta, success_rate,
			rate_limit_errors, quota_errors, auth_errors,
			timeout_errors, network_errors, other_errors
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (provider_id, model, date) DO UPDATE SET
			reliability_score = EXCLUDED.reliability_score,
			avg_latency_ms    = EXCLUDED.avg_latency_ms,
			error_rate        = EXCLUDED.error_rate,
			request_count     = EXCLUDED.request_count,
			success_count     = EXCLUDED.success_count,
			bandit_alpha      = EXCLUDED.bandit_alpha,
			bandit_beta       = EXCLUDED.bandit_beta,
			success_rate      = EXCLUDED.success_rate,
			rate_limit_errors = EXCLUDED.rate_limit_errors,
			quota_errors      = EXCLUDED.quota_errors,
			auth_errors       = EXCLUDED.auth_errors,
			timeout_errors    = EXCLUDED.timeout_errors,
			network_errors    = EXCLUDED.network_errors,
			other_errors      = EXCLUDED.other_errors
	`,
		row.ProviderID, row.Model, row.Date,
		row.ReliabilityScore, row.AvgLatencyMs, row.ErrorRate,
		row.RequestCount, row.SuccessCount,
		row.BanditAlpha, row.BanditBeta, row.SuccessRate,
		row.RateLimitErrors, row.QuotaErrors, row.AuthErrors,
		row.TimeoutErrors, row.NetworkErrors, row.OtherErrors,
	)
	if err != nil {
		return fmt.Errorf("reputation: SaveTimeseries: %w", err)
	}
	return nil
}

// ListProviderModelPairs 列出已存在的 (provider, model) 对
func (s *PostgresReputationStore) ListProviderModelPairs(ctx context.Context) ([]ProviderModelKey, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.query(ctx, `
		SELECT DISTINCT provider_id, model
		FROM provider_reputation_timeseries
		ORDER BY provider_id, model
	`)
	if err != nil {
		return nil, fmt.Errorf("reputation: ListProviderModelPairs: %w", err)
	}
	defer rows.Close()

	out := make([]ProviderModelKey, 0)
	for rows.Next() {
		var k ProviderModelKey
		if err := rows.Scan(&k.ProviderID, &k.Model); err != nil {
			return nil, fmt.Errorf("reputation: ListProviderModelPairs scan: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Incidents
// ---------------------------------------------------------------------------

// GetRecentIncidents 获取最近事件
func (s *PostgresReputationStore) GetRecentIncidents(ctx context.Context, providerID string, model string, days int) ([]Incident, error) {
	if !s.Enabled() {
		return nil, nil
	}
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)

	// 模型为空时只按 provider 过滤；非空时同时按 (provider, model) 过滤
	var (
		rows pgx.Rows
		err  error
	)
	if model == "" {
		rows, err = s.query(ctx, `
			SELECT id, provider_id, COALESCE(model, ''), incident_type, impact_level,
			       COALESCE(description, ''),
			       started_at, ended_at, COALESCE(duration_seconds, 0),
			       COALESCE(affected_requests, 0), COALESCE(affected_tenants, 0),
			       resolved, COALESCE(resolution_notes, ''),
			       created_at, updated_at
			FROM provider_incidents
			WHERE provider_id = $1 AND started_at >= $2
			ORDER BY started_at DESC
		`, providerID, cutoff)
	} else {
		rows, err = s.query(ctx, `
			SELECT id, provider_id, COALESCE(model, ''), incident_type, impact_level,
			       COALESCE(description, ''),
			       started_at, ended_at, COALESCE(duration_seconds, 0),
			       COALESCE(affected_requests, 0), COALESCE(affected_tenants, 0),
			       resolved, COALESCE(resolution_notes, ''),
			       created_at, updated_at
			FROM provider_incidents
			WHERE provider_id = $1 AND model = $2 AND started_at >= $3
			ORDER BY started_at DESC
		`, providerID, model, cutoff)
	}
	if err != nil {
		return nil, fmt.Errorf("reputation: GetRecentIncidents: %w", err)
	}
	defer rows.Close()

	out := make([]Incident, 0)
	for rows.Next() {
		inc, scanErr := scanIncident(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, inc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetUnresolvedIncidents 获取未解决事件
func (s *PostgresReputationStore) GetUnresolvedIncidents(ctx context.Context, providerID string, model string) ([]Incident, error) {
	if !s.Enabled() {
		return nil, nil
	}

	var (
		rows pgx.Rows
		err  error
	)
	if model == "" {
		rows, err = s.query(ctx, `
			SELECT id, provider_id, COALESCE(model, ''), incident_type, impact_level,
			       COALESCE(description, ''),
			       started_at, ended_at, COALESCE(duration_seconds, 0),
			       COALESCE(affected_requests, 0), COALESCE(affected_tenants, 0),
			       resolved, COALESCE(resolution_notes, ''),
			       created_at, updated_at
			FROM provider_incidents
			WHERE provider_id = $1 AND resolved = FALSE
			ORDER BY started_at DESC
		`, providerID)
	} else {
		rows, err = s.query(ctx, `
			SELECT id, provider_id, COALESCE(model, ''), incident_type, impact_level,
			       COALESCE(description, ''),
			       started_at, ended_at, COALESCE(duration_seconds, 0),
			       COALESCE(affected_requests, 0), COALESCE(affected_tenants, 0),
			       resolved, COALESCE(resolution_notes, ''),
			       created_at, updated_at
			FROM provider_incidents
			WHERE provider_id = $1 AND model = $2 AND resolved = FALSE
			ORDER BY started_at DESC
		`, providerID, model)
	}
	if err != nil {
		return nil, fmt.Errorf("reputation: GetUnresolvedIncidents: %w", err)
	}
	defer rows.Close()

	out := make([]Incident, 0)
	for rows.Next() {
		inc, scanErr := scanIncident(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, inc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// RecordIncident 记录事件
func (s *PostgresReputationStore) RecordIncident(ctx context.Context, incident *Incident) error {
	if !s.Enabled() {
		return ErrNoDatabase
	}
	if incident == nil || incident.ProviderID == "" || incident.Type == "" {
		return errors.New("credential: RecordIncident requires provider_id and type")
	}
	if incident.StartedAt.IsZero() {
		incident.StartedAt = time.Now().UTC()
	}

	// model 为空字符串时存 NULL（表示跨模型事件）
	var modelArg interface{}
	if incident.Model != "" {
		modelArg = incident.Model
	}
	var endedArg interface{}
	if incident.EndedAt != nil {
		endedArg = *incident.EndedAt
	}

	err := s.queryRow(ctx, `
		INSERT INTO provider_incidents (
			provider_id, model, incident_type, impact_level,
			description, started_at, ended_at,
			affected_requests, affected_tenants,
			resolved, resolution_notes
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at, updated_at
	`,
		incident.ProviderID, modelArg, string(incident.Type), string(incident.Impact),
		incident.Description, incident.StartedAt, endedArg,
		incident.AffectedRequests, incident.AffectedTenants,
		incident.Resolved, incident.ResolutionNotes,
	).Scan(&incident.ID, &incident.CreatedAt, &incident.UpdatedAt)
	if err != nil {
		return fmt.Errorf("reputation: RecordIncident: %w", err)
	}
	if incident.EndedAt != nil {
		incident.Duration = incident.EndedAt.Sub(incident.StartedAt)
		incident.DurationSeconds = int(incident.Duration.Seconds())
	}
	return nil
}

// ResolveIncident 标记事件已解决
func (s *PostgresReputationStore) ResolveIncident(ctx context.Context, incidentID int64, resolutionNotes string) error {
	if !s.Enabled() {
		return ErrNoDatabase
	}
	tag, err := s.exec(ctx, `
		UPDATE provider_incidents
		SET resolved          = TRUE,
		    ended_at          = COALESCE(ended_at, NOW()),
		    resolution_notes  = $2,
		    updated_at        = NOW()
		WHERE id = $1 AND resolved = FALSE
	`, incidentID, resolutionNotes)
	if err != nil {
		return fmt.Errorf("reputation: ResolveIncident: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrReputationNotFound
	}
	return nil
}

// RecordIncidentIfNotExists 在窗口内去重
//
// 使用 created_at 作为去重基准（检测时间），避免同一异常被多次扫描重复入库。
func (s *PostgresReputationStore) RecordIncidentIfNotExists(ctx context.Context, incident *Incident, dedupeWindow time.Duration) (bool, error) {
	if !s.Enabled() {
		return false, ErrNoDatabase
	}
	if dedupeWindow <= 0 {
		dedupeWindow = 1 * time.Hour
	}
	if incident.StartedAt.IsZero() {
		incident.StartedAt = time.Now().UTC()
	}
	cutoff := time.Now().UTC().Add(-dedupeWindow)

	var existing int64
	var err error
	if incident.Model != "" {
		err = s.queryRow(ctx, `
			SELECT id FROM provider_incidents
			WHERE provider_id   = $1
			  AND model         = $2
			  AND incident_type = $3
			  AND resolved      = FALSE
			  AND created_at    >= $4
			LIMIT 1
		`, incident.ProviderID, incident.Model, string(incident.Type), cutoff).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	if err != nil {
		return false, fmt.Errorf("reputation: dedupe check: %w", err)
	}
	if existing != 0 {
		return false, nil
	}
	if err := s.RecordIncident(ctx, incident); err != nil {
		return false, err
	}
	return true, nil
}

// scanIncident 把行扫描到 Incident（共享 SELECT 列表）
func scanIncident(rows pgx.Rows) (Incident, error) {
	var (
		inc       Incident
		typeStr   string
		impactStr string
		endedAt   sql.NullTime
	)
	if err := rows.Scan(
		&inc.ID, &inc.ProviderID, &inc.Model, &typeStr, &impactStr,
		&inc.Description,
		&inc.StartedAt, &endedAt, &inc.DurationSeconds,
		&inc.AffectedRequests, &inc.AffectedTenants,
		&inc.Resolved, &inc.ResolutionNotes,
		&inc.CreatedAt, &inc.UpdatedAt,
	); err != nil {
		return inc, fmt.Errorf("reputation: scan incident: %w", err)
	}
	inc.Type = IncidentType(typeStr)
	inc.Impact = ImpactLevel(impactStr)
	if endedAt.Valid {
		t := endedAt.Time
		inc.EndedAt = &t
		inc.Duration = t.Sub(inc.StartedAt)
	} else if inc.DurationSeconds > 0 {
		inc.Duration = time.Duration(inc.DurationSeconds) * time.Second
	}
	return inc, nil
}
