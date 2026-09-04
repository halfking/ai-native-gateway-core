package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// defaultListLimit 列表默认页大小：opts 为 nil 或 Limit<=0 时生效。
const defaultListLimit = 100

// 会话元数据 SQL（时间统一存 Unix 秒；metadata 存 JSON 字符串，nil map 存 NULL）。
const (
	insertSessionSQL = `INSERT INTO sessions (id, tenant_id, user_id, created_at, updated_at, metadata) VALUES (?, ?, ?, ?, ?, ?);`
	selectSessionSQL = `SELECT id, tenant_id, user_id, created_at, updated_at, metadata FROM sessions WHERE tenant_id = ? AND id = ?;`
	updateSessionSQL = `UPDATE sessions SET user_id = ?, updated_at = ?, metadata = ? WHERE tenant_id = ? AND id = ?;`
	deleteSessionSQL = `DELETE FROM sessions WHERE tenant_id = ? AND id = ?;`
	listSessionsSQL  = `SELECT id, tenant_id, user_id, created_at, updated_at, metadata FROM sessions WHERE tenant_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?;`
)

// SQLiteSessionStore 基于 SQLite 的会话元数据存储，实现 storage.SessionStore。
type SQLiteSessionStore struct {
	db *sql.DB
}

// 编译期断言：确保实现 storage.SessionStore 接口。
var _ storage.SessionStore = (*SQLiteSessionStore)(nil)

// NewSQLiteSessionStore 创建会话元数据存储，db 一般来自 OpenSQLite。
func NewSQLiteSessionStore(db *sql.DB) *SQLiteSessionStore {
	return &SQLiteSessionStore{db: db}
}

// marshalMetadata 将 metadata 序列化为 JSON 字符串；nil map 存为 SQL NULL。
func marshalMetadata(metadata map[string]interface{}) (interface{}, error) {
	if metadata == nil {
		return nil, nil
	}
	buf, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return string(buf), nil
}

// unmarshalMetadata 反序列化 metadata；NULL 或空串读回为 nil map，保证与写入往返一致。
func unmarshalMetadata(raw sql.NullString) (map[string]interface{}, error) {
	if !raw.Valid {
		return nil, nil
	}
	s := strings.TrimSpace(raw.String)
	if s == "" {
		return nil, nil
	}
	m := make(map[string]interface{})
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// sessionScanner 抽象 *sql.Row 与 *sql.Rows 共有的 Scan 能力，便于复用行扫描逻辑。
type sessionScanner interface {
	Scan(dest ...interface{}) error
}

// scanSession 从一行查询结果还原会话元数据（user_id/metadata 允许为 NULL）。
func scanSession(row sessionScanner) (*storage.Session, error) {
	var (
		sess      storage.Session
		userID    sql.NullString
		meta      sql.NullString
		createdAt int64
		updatedAt int64
	)
	if err := row.Scan(&sess.ID, &sess.TenantID, &userID, &createdAt, &updatedAt, &meta); err != nil {
		return nil, err
	}
	sess.UserID = userID.String
	sess.CreatedAt = time.Unix(createdAt, 0).UTC()
	sess.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	metaMap, err := unmarshalMetadata(meta)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 解析会话 %s/%s metadata 失败: %w", sess.TenantID, sess.ID, err)
	}
	sess.Metadata = metaMap
	return &sess, nil
}

// CreateSession 插入一条会话元数据；CreatedAt/UpdatedAt 为零值时自动取当前时间。
func (s *SQLiteSessionStore) CreateSession(ctx context.Context, session *storage.Session) error {
	meta, err := marshalMetadata(session.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化会话 %s/%s metadata 失败: %w", session.TenantID, session.ID, err)
	}
	created := session.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := session.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	if _, err := s.db.ExecContext(ctx, insertSessionSQL,
		session.ID, session.TenantID, session.UserID, created.Unix(), updated.Unix(), meta); err != nil {
		return fmt.Errorf("sqlite: 写入会话 %s/%s 失败: %w", session.TenantID, session.ID, err)
	}
	return nil
}

// GetSession 按 (tenant_id, id) 查询单个会话；不存在时返回包装了
// storage.ErrNotFound 的错误（errors.Is 可命中）。
func (s *SQLiteSessionStore) GetSession(ctx context.Context, tenantID, sessionID string) (*storage.Session, error) {
	row := s.db.QueryRowContext(ctx, selectSessionSQL, tenantID, sessionID)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: session %s/%s", storage.ErrNotFound, tenantID, sessionID)
		}
		return nil, fmt.Errorf("sqlite: 查询会话 %s/%s 失败: %w", tenantID, sessionID, err)
	}
	return sess, nil
}

// UpdateSession 更新会话的 user_id/updated_at/metadata；
// 目标不存在时返回 storage.ErrNotFound。
func (s *SQLiteSessionStore) UpdateSession(ctx context.Context, session *storage.Session) error {
	meta, err := marshalMetadata(session.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: 序列化会话 %s/%s metadata 失败: %w", session.TenantID, session.ID, err)
	}
	updated := session.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	res, err := s.db.ExecContext(ctx, updateSessionSQL,
		session.UserID, updated.Unix(), meta, session.TenantID, session.ID)
	if err != nil {
		return fmt.Errorf("sqlite: 更新会话 %s/%s 失败: %w", session.TenantID, session.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: session %s/%s", storage.ErrNotFound, session.TenantID, session.ID)
	}
	return nil
}

// DeleteSession 删除会话；目标不存在时返回 storage.ErrNotFound。
func (s *SQLiteSessionStore) DeleteSession(ctx context.Context, tenantID, sessionID string) error {
	res, err := s.db.ExecContext(ctx, deleteSessionSQL, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("sqlite: 删除会话 %s/%s 失败: %w", tenantID, sessionID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: session %s/%s", storage.ErrNotFound, tenantID, sessionID)
	}
	return nil
}

// normalizeListOptions 归一化分页参数：opts 为 nil 或 Limit<=0 时取默认
// defaultListLimit，Offset<0（或等于 0）归零。
func normalizeListOptions(opts *storage.ListOptions) (limit, offset int) {
	limit, offset = defaultListLimit, 0
	if opts == nil {
		return limit, offset
	}
	if opts.Limit > 0 {
		limit = opts.Limit
	}
	if opts.Offset > 0 {
		offset = opts.Offset
	}
	return limit, offset
}

// ListSessions 按租户过滤、created_at 倒序分页返回会话列表；
// 无数据时返回空切片（非 nil）。
func (s *SQLiteSessionStore) ListSessions(ctx context.Context, tenantID string, opts *storage.ListOptions) ([]*storage.Session, error) {
	limit, offset := normalizeListOptions(opts)
	rows, err := s.db.QueryContext(ctx, listSessionsSQL, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 查询租户 %s 会话列表失败: %w", tenantID, err)
	}
	defer rows.Close()

	sessions := make([]*storage.Session, 0, limit)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 扫描会话行失败: %w", err)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历会话列表失败: %w", err)
	}
	return sessions, nil
}
