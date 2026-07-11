// Package telemetry - dashboard_events.go
// 首页 Dashboard 数据访问埋点系统
//
// 目的：
//  1. 记录 API 访问情况（PV/UV/响应时间）
//  2. 追踪数据查询模式（哪些指标最常访问）
//  3. 性能监控（慢查询、错误率）
//  4. 用户行为分析（用于优化 API 设计）
//
// 设计原则：
//   - 异步写入，不影响主流程
//   - 批量写入，降低数据库压力
//   - 自动降级，写入失败不影响业务
//   - 完整的数据生命周期（hot → 归档）
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DashboardEvent Dashboard 访问事件
type DashboardEvent struct {
	// 基础信息
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"` // api_access, query, export, error
	Timestamp time.Time `json:"timestamp"`

	// 用户信息
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id,omitempty"`
	UserRole  string `json:"user_role,omitempty"`  // super_admin / tenant_admin / user
	SessionID string `json:"session_id,omitempty"` // 用户会话 ID（不是 gw_session_id）

	// API 信息
	APIPath    string `json:"api_path"`
	APIMethod  string `json:"api_method"`
	APIVersion string `json:"api_version,omitempty"`

	// 请求参数（脱敏）
	QueryParams map[string]interface{} `json:"query_params,omitempty"`

	// 响应信息
	StatusCode   int    `json:"status_code"`
	ResponseTime int64  `json:"response_time_ms"` // 毫秒
	CacheHit     bool   `json:"cache_hit"`
	DataSize     int    `json:"data_size,omitempty"` // 返回数据大小（字节）
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	// 客户端信息
	ClientIP  string `json:"client_ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	Referer   string `json:"referer,omitempty"`

	// 性能指标
	DBQueryTime    int64 `json:"db_query_time_ms,omitempty"`
	CacheQueryTime int64 `json:"cache_query_time_ms,omitempty"`
}

// dashboardDB is the subset of pgxpool.Pool required by DashboardEventRecorder.
type dashboardDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// DashboardEventRecorder Dashboard 事件记录器
type DashboardEventRecorder struct {
	db     dashboardDB
	logger *slog.Logger

	// 批量写入
	buffer        []*DashboardEvent
	bufferMu      sync.Mutex
	flushSize     int
	flushInterval time.Duration

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewDashboardEventRecorder 创建事件记录器
func NewDashboardEventRecorder(db *pgxpool.Pool, logger *slog.Logger) *DashboardEventRecorder {
	return newDashboardEventRecorder(db, logger)
}

func newDashboardEventRecorder(db dashboardDB, logger *slog.Logger) *DashboardEventRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &DashboardEventRecorder{
		db:            db,
		logger:        logger,
		buffer:        make([]*DashboardEvent, 0, 100),
		flushSize:     50,
		flushInterval: 10 * time.Second,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
}

// Start 启动后台刷新协程
func (r *DashboardEventRecorder) Start(ctx context.Context) {
	if r == nil || r.db == nil {
		return
	}
	go r.runFlushLoop(ctx)
}

// Stop 停止记录器
func (r *DashboardEventRecorder) Stop() {
	if r == nil {
		return
	}
	close(r.stopCh)
	<-r.doneCh
}

// Record 记录事件（异步）
func (r *DashboardEventRecorder) Record(event *DashboardEvent) {
	if r == nil || r.db == nil || event == nil {
		return
	}

	// 设置默认值
	if event.EventID == "" {
		event.EventID = generateEventID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	r.bufferMu.Lock()
	r.buffer = append(r.buffer, event)

	// 达到批量大小则触发刷新
	shouldFlush := len(r.buffer) >= r.flushSize
	r.bufferMu.Unlock()

	if shouldFlush {
		go r.flush()
	}
}

// RecordAccess 记录 API 访问（便捷方法）
func (r *DashboardEventRecorder) RecordAccess(
	tenantID, userID, userRole, sessionID string,
	apiPath, apiMethod string,
	statusCode int,
	responseTime time.Duration,
	cacheHit bool,
) {
	r.Record(&DashboardEvent{
		EventType:    "api_access",
		TenantID:     tenantID,
		UserID:       userID,
		UserRole:     userRole,
		SessionID:    sessionID,
		APIPath:      apiPath,
		APIMethod:    apiMethod,
		StatusCode:   statusCode,
		ResponseTime: responseTime.Milliseconds(),
		CacheHit:     cacheHit,
	})
}

// RecordError 记录错误
func (r *DashboardEventRecorder) RecordError(
	tenantID, userID, apiPath, apiMethod string,
	statusCode int,
	errorCode, errorMessage string,
	responseTime time.Duration,
) {
	r.Record(&DashboardEvent{
		EventType:    "error",
		TenantID:     tenantID,
		UserID:       userID,
		APIPath:      apiPath,
		APIMethod:    apiMethod,
		StatusCode:   statusCode,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
		ResponseTime: responseTime.Milliseconds(),
	})
}

// RecordQuery 记录数据查询
func (r *DashboardEventRecorder) RecordQuery(
	tenantID, userID, apiPath string,
	queryParams map[string]interface{},
	dbQueryTime, cacheQueryTime time.Duration,
	dataSize int,
) {
	r.Record(&DashboardEvent{
		EventType:      "query",
		TenantID:       tenantID,
		UserID:         userID,
		APIPath:        apiPath,
		QueryParams:    queryParams,
		DBQueryTime:    dbQueryTime.Milliseconds(),
		CacheQueryTime: cacheQueryTime.Milliseconds(),
		DataSize:       dataSize,
		StatusCode:     200,
	})
}

// runFlushLoop 后台刷新循环
func (r *DashboardEventRecorder) runFlushLoop(ctx context.Context) {
	defer close(r.doneCh)

	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			r.flush()
			return
		case <-ctx.Done():
			r.flush()
			return
		case <-ticker.C:
			r.flush()
		}
	}
}

// flush 刷新缓冲区到数据库
func (r *DashboardEventRecorder) flush() {
	r.bufferMu.Lock()
	if len(r.buffer) == 0 {
		r.bufferMu.Unlock()
		return
	}
	events := r.buffer
	r.buffer = make([]*DashboardEvent, 0, r.flushSize)
	r.bufferMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 批量插入
	for _, event := range events {
		if err := r.insertEvent(ctx, event); err != nil {
			r.logger.Warn("failed to insert dashboard event",
				"error", err,
				"event_id", event.EventID)
		}
	}
}

func (r *DashboardEventRecorder) insertEvent(ctx context.Context, event *DashboardEvent) error {
	queryParamsJSON, _ := json.Marshal(event.QueryParams)

	query := `
			INSERT INTO dashboard_access_events_hot (

			event_id, event_type, timestamp,
			tenant_id, user_id, user_role, session_id,
			api_path, api_method, api_version,
			query_params,
			status_code, response_time_ms, cache_hit, data_size,
			error_code, error_message,
			client_ip, user_agent, referer,
			db_query_time_ms, cache_query_time_ms
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
	`

	_, err := r.db.Exec(ctx, query,
		event.EventID, event.EventType, event.Timestamp,
		event.TenantID, event.UserID, event.UserRole, event.SessionID,
		event.APIPath, event.APIMethod, event.APIVersion,
		queryParamsJSON,
		event.StatusCode, event.ResponseTime, event.CacheHit, event.DataSize,
		event.ErrorCode, event.ErrorMessage,
		event.ClientIP, event.UserAgent, event.Referer,
		event.DBQueryTime, event.CacheQueryTime,
	)

	return err
}

// ────────────────────────────────────────────────────────────────
// 统计查询接口
// ────────────────────────────────────────────────────────────────

// AccessStats 访问统计
type AccessStats struct {
	TotalRequests int64   `json:"total_requests"`
	UniqueUsers   int64   `json:"unique_users"`
	UniqueTenants int64   `json:"unique_tenants"`
	AvgResponseMs float64 `json:"avg_response_ms"`
	P95ResponseMs float64 `json:"p95_response_ms"`
	P99ResponseMs float64 `json:"p99_response_ms"`
	CacheHitRate  float64 `json:"cache_hit_rate"`
	ErrorRate     float64 `json:"error_rate"`

	// 按 API 分组
	TopAPIs []APIAccessStats `json:"top_apis"`
}

// APIAccessStats API 访问统计
type APIAccessStats struct {
	APIPath       string  `json:"api_path"`
	RequestCount  int64   `json:"request_count"`
	AvgResponseMs float64 `json:"avg_response_ms"`
	ErrorRate     float64 `json:"error_rate"`
	CacheHitRate  float64 `json:"cache_hit_rate"`
}

// GetAccessStats 获取访问统计
func (r *DashboardEventRecorder) GetAccessStats(ctx context.Context, hours int) (*AccessStats, error) {
	if hours <= 0 {
		hours = 24
	}

	stats := &AccessStats{}

	// 总览统计。hot 表保存实时事件；archive 表包含已归档事件。排除已经
	// 位于 hot 的 archive 行，兼容归档迁移期间可能短暂存在的重复数据。
	overviewQuery := fmt.Sprintf(`
		WITH access_events AS (
			SELECT event_id, user_id, tenant_id, response_time_ms, cache_hit, status_code
			FROM dashboard_access_events_hot
			WHERE timestamp > NOW() - INTERVAL '%d hours'
			  AND event_type = 'api_access'
			UNION ALL
			SELECT archived.event_id, archived.user_id, archived.tenant_id, archived.response_time_ms, archived.cache_hit, archived.status_code
			FROM dashboard_access_events archived
			WHERE archived.timestamp > NOW() - INTERVAL '%d hours'
			  AND archived.event_type = 'api_access'
			  AND NOT EXISTS (
				SELECT 1 FROM dashboard_access_events_hot hot WHERE hot.event_id = archived.event_id
			  )
		)
		SELECT
			COUNT(*) as total,
			COUNT(DISTINCT user_id) as unique_users,
			COUNT(DISTINCT tenant_id) as unique_tenants,
			AVG(response_time_ms)::FLOAT as avg_response,
			PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY response_time_ms)::FLOAT as p95,
			PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY response_time_ms)::FLOAT as p99,
			COUNT(*) FILTER (WHERE cache_hit = true) * 100.0 / NULLIF(COUNT(*), 0) as cache_hit_rate,
			COUNT(*) FILTER (WHERE status_code >= 400) * 100.0 / NULLIF(COUNT(*), 0) as error_rate
		FROM access_events
	`, hours, hours)

	err := r.db.QueryRow(ctx, overviewQuery).Scan(
		&stats.TotalRequests,
		&stats.UniqueUsers,
		&stats.UniqueTenants,
		&stats.AvgResponseMs,
		&stats.P95ResponseMs,
		&stats.P99ResponseMs,
		&stats.CacheHitRate,
		&stats.ErrorRate,
	)
	if err != nil {
		return nil, err
	}

	// Top APIs
	topQuery := fmt.Sprintf(`
		WITH access_events AS (
			SELECT event_id, api_path, response_time_ms, status_code, cache_hit
			FROM dashboard_access_events_hot
			WHERE timestamp > NOW() - INTERVAL '%d hours'
			  AND event_type = 'api_access'
			UNION ALL
			SELECT archived.event_id, archived.api_path, archived.response_time_ms, archived.status_code, archived.cache_hit
			FROM dashboard_access_events archived
			WHERE archived.timestamp > NOW() - INTERVAL '%d hours'
			  AND archived.event_type = 'api_access'
			  AND NOT EXISTS (
				SELECT 1 FROM dashboard_access_events_hot hot WHERE hot.event_id = archived.event_id
			  )
		)
		SELECT
			api_path,
			COUNT(*) as request_count,
			AVG(response_time_ms)::FLOAT as avg_response,
			COUNT(*) FILTER (WHERE status_code >= 400) * 100.0 / NULLIF(COUNT(*), 0) as error_rate,
			COUNT(*) FILTER (WHERE cache_hit = true) * 100.0 / NULLIF(COUNT(*), 0) as cache_hit_rate
		FROM access_events
		GROUP BY api_path
		ORDER BY request_count DESC
		LIMIT 10
	`, hours, hours)

	rows, err := r.db.Query(ctx, topQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats.TopAPIs = make([]APIAccessStats, 0)
	for rows.Next() {
		var api APIAccessStats
		if err := rows.Scan(&api.APIPath, &api.RequestCount, &api.AvgResponseMs, &api.ErrorRate, &api.CacheHitRate); err != nil {
			return nil, err
		}
		stats.TopAPIs = append(stats.TopAPIs, api)
	}

	return stats, nil
}

// ────────────────────────────────────────────────────────────────
// 工具函数
// ────────────────────────────────────────────────────────────────

func generateEventID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}
