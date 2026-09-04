package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// maxListRequestsLimit 请求日志单次查询的返回条数上限。
const maxListRequestsLimit = 1000

// 请求日志 SQL（时间存 Unix 秒；duration 存毫秒；Body 大字段不入库，仅记 has_body 标记）。
const (
	insertRequestSQL = `
INSERT INTO request_logs (request_id, tenant_id, session_id, ts, method, path, status_code, duration_ms, has_body)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`

	selectRequestSQL = `
SELECT request_id, tenant_id, session_id, ts, method, path, status_code, duration_ms, COALESCE(has_body, 0) AS has_body
FROM request_logs
WHERE request_id = ?;`

	selectRequestListSQL = `
SELECT request_id, tenant_id, session_id, ts, method, path, status_code, duration_ms, COALESCE(has_body, 0) AS has_body
FROM request_logs`
)

// SQLiteRequestLogStore 基于 SQLite 的请求日志存储，实现 storage.RequestLogStore。
type SQLiteRequestLogStore struct {
	db *sql.DB
}

// 编译期断言：确保实现 storage.RequestLogStore 接口。
var _ storage.RequestLogStore = (*SQLiteRequestLogStore)(nil)

// NewSQLiteRequestLogStore 创建请求日志存储，db 一般来自 OpenSQLite。
func NewSQLiteRequestLogStore(db *sql.DB) *SQLiteRequestLogStore {
	return &SQLiteRequestLogStore{db: db}
}

// WriteRequest 写入一条请求日志：Duration 按毫秒落库；Body 大字段不入库，
// 仅记录 has_body 标记；Timestamp 为零值时自动取当前时间。
func (s *SQLiteRequestLogStore) WriteRequest(ctx context.Context, req *storage.RequestLog) error {
	if req == nil {
		return errors.New("sqlite: 请求日志不能为空")
	}
	ts := req.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	hasBody := 0
	if len(req.Body) > 0 {
		hasBody = 1
	}
	if _, err := s.db.ExecContext(ctx, insertRequestSQL,
		req.RequestID, req.TenantID, req.SessionID, ts.Unix(),
		req.Method, req.Path, req.StatusCode, req.Duration.Milliseconds(), hasBody); err != nil {
		return fmt.Errorf("sqlite: 写入请求日志 %s 失败: %w", req.RequestID, err)
	}
	return nil
}

// requestScanner 抽象 *sql.Row 与 *sql.Rows 共有的 Scan 能力。
type requestScanner interface {
	Scan(dest ...interface{}) error
}

// scanRequestLog 从一行查询结果还原请求日志；Body 不入库，读回恒为 nil。
func scanRequestLog(row requestScanner) (*storage.RequestLog, error) {
	var (
		req        storage.RequestLog
		sessionID  sql.NullString
		ts         int64
		statusCode sql.NullInt64
		durationMs sql.NullInt64
		hasBody    int64
	)
	if err := row.Scan(&req.RequestID, &req.TenantID, &sessionID, &ts, &req.Method, &req.Path, &statusCode, &durationMs, &hasBody); err != nil {
		return nil, err
	}
	req.SessionID = sessionID.String
	req.Timestamp = time.Unix(ts, 0).UTC()
	req.StatusCode = int(statusCode.Int64)
	req.Duration = time.Duration(durationMs.Int64) * time.Millisecond
	req.Body = nil
	return &req, nil
}

// GetRequest 按 request_id 查询请求日志；不存在时返回包装了
// storage.ErrNotFound 的错误（errors.Is 可命中）。
func (s *SQLiteRequestLogStore) GetRequest(ctx context.Context, requestID string) (*storage.RequestLog, error) {
	row := s.db.QueryRowContext(ctx, selectRequestSQL, requestID)
	req, err := scanRequestLog(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: request %s", storage.ErrNotFound, requestID)
		}
		return nil, fmt.Errorf("sqlite: 查询请求日志 %s 失败: %w", requestID, err)
	}
	return req, nil
}

// normalizeRequestLimit 归一化 limit：<=0 取默认 defaultListLimit，超过
// maxListRequestsLimit 截断。
func normalizeRequestLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListRequestsLimit {
		return maxListRequestsLimit
	}
	return limit
}

// ListRequests 按 filter（tenant_id/session_id/startTime/endTime）过滤并按
// ts 倒序返回，filter 为 nil 时返回全部（受限）。WHERE 条件全部参数化拼接，
// 防止 SQL 注入；limit 默认 100、上限 1000。无数据时返回空切片（非 nil）。
func (s *SQLiteRequestLogStore) ListRequests(ctx context.Context, filter *storage.RequestFilter) ([]*storage.RequestLog, error) {
	var (
		conds []string
		args  []interface{}
		limit int
	)
	if filter != nil {
		limit = filter.Limit
		if filter.TenantID != "" {
			conds = append(conds, "tenant_id = ?")
			args = append(args, filter.TenantID)
		}
		if filter.SessionID != "" {
			conds = append(conds, "session_id = ?")
			args = append(args, filter.SessionID)
		}
		if !filter.StartTime.IsZero() {
			conds = append(conds, "ts >= ?")
			args = append(args, filter.StartTime.Unix())
		}
		if !filter.EndTime.IsZero() {
			conds = append(conds, "ts <= ?")
			args = append(args, filter.EndTime.Unix())
		}
	}
	query := selectRequestListSQL
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, normalizeRequestLimit(limit))
	query += " ORDER BY ts DESC LIMIT ?;"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 查询请求日志列表失败: %w", err)
	}
	defer rows.Close()

	logs := make([]*storage.RequestLog, 0)
	for rows.Next() {
		req, err := scanRequestLog(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 扫描请求日志行失败: %w", err)
		}
		logs = append(logs, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历请求日志列表失败: %w", err)
	}
	return logs, nil
}
