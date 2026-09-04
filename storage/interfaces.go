// Package storage 定义 llm-gateway 的双模式存储架构：
//   - full 模式：PostgreSQL + Redis，面向多实例生产部署；
//   - lite 模式：SQLite + File + Memory，面向单机轻量部署。
//
// 本包仅包含存储接口、数据类型、哨兵错误与存储工厂，具体实现位于后续波次。
package storage

import (
	"context"
	"time"
)

// StorageMode 存储模式。
type StorageMode string

const (
	// StorageModeFull 完整模式：PostgreSQL + Redis。
	StorageModeFull StorageMode = "full"
	// StorageModeLite 轻量模式：SQLite + File + Memory。
	StorageModeLite StorageMode = "lite"
)

// SessionStore 会话元数据存储。
type SessionStore interface {
	CreateSession(ctx context.Context, session *Session) error
	GetSession(ctx context.Context, tenantID, sessionID string) (*Session, error)
	UpdateSession(ctx context.Context, session *Session) error
	ListSessions(ctx context.Context, tenantID string, opts *ListOptions) ([]*Session, error)
	DeleteSession(ctx context.Context, tenantID, sessionID string) error
}

// BodiesStore 会话内容存储（大对象）。
type BodiesStore interface {
	Write(ctx context.Context, body *SessionBody) error
	Read(ctx context.Context, tenantID, sessionID string, turnNo int) (*SessionBody, error)
	ReadRange(ctx context.Context, tenantID, sessionID string, startTurn, endTurn int) ([]*SessionBody, error)
	Delete(ctx context.Context, tenantID, sessionID string) error
}

// TurnsStore 会话轮次元数据存储。
type TurnsStore interface {
	WriteTurnMeta(ctx context.Context, meta *TurnMeta) error
	GetTurnsMeta(ctx context.Context, tenantID, sessionID string) ([]*TurnMeta, error)
}

// RequestLogStore 请求日志存储。
type RequestLogStore interface {
	WriteRequest(ctx context.Context, req *RequestLog) error
	GetRequest(ctx context.Context, requestID string) (*RequestLog, error)
	ListRequests(ctx context.Context, filter *RequestFilter) ([]*RequestLog, error)
}

// StateStore 运行时状态存储。
type StateStore interface {
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Get(ctx context.Context, key string) (interface{}, error)
	Delete(ctx context.Context, key string) error
}
