package vibecoding

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// SessionManager 会话管理器
type SessionManager struct {
	store Store
}

// NewSessionManager 创建会话管理器
func NewSessionManager(store Store) *SessionManager {
	return &SessionManager{
		store: store,
	}
}

// CreateSession 创建会话
func (m *SessionManager) CreateSession(ctx context.Context, tenantID, taskType string, projectID *int64) (*Session, error) {
	session := &Session{
		ProjectID: projectID,
		TenantID:  tenantID,
		SessionID: generateSessionID(),
		TaskType:  taskType,
		Status:    SessionStatusActive,
		Messages:  []Message{},
		Metadata:  make(map[string]interface{}),
	}

	if err := m.store.CreateSession(ctx, session); err != nil {
		slog.Error("create session failed", "error", err)
		return nil, err
	}

	slog.Info("session created", "session_id", session.SessionID, "task_type", taskType)
	return session, nil
}

// GetSession 获取会话
func (m *SessionManager) GetSession(ctx context.Context, id int64) (*Session, error) {
	return m.store.GetSession(ctx, id)
}

// GetSessionBySessionID 根据SessionID获取会话
func (m *SessionManager) GetSessionBySessionID(ctx context.Context, sessionID string) (*Session, error) {
	return m.store.GetSessionBySessionID(ctx, sessionID)
}

// ListSessions 列出会话
func (m *SessionManager) ListSessions(ctx context.Context, projectID *int64, status SessionStatus, offset, limit int) ([]Session, int, error) {
	return m.store.ListSessions(ctx, projectID, status, offset, limit)
}

// AddMessage 添加消息到会话
func (m *SessionManager) AddMessage(ctx context.Context, sessionID string, role, content string) error {
	session, err := m.store.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		return err
	}

	message := Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}
	session.Messages = append(session.Messages, message)

	if err := m.store.UpdateSession(ctx, session); err != nil {
		slog.Error("add message failed", "error", err)
		return err
	}

	return nil
}

// CompleteSession 完成会话
func (m *SessionManager) CompleteSession(ctx context.Context, sessionID string) error {
	session, err := m.store.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		return err
	}

	now := time.Now()
	if err := m.store.UpdateSessionStatus(ctx, session.ID, SessionStatusCompleted, &now); err != nil {
		slog.Error("complete session failed", "error", err)
		return err
	}

	slog.Info("session completed", "session_id", sessionID)
	return nil
}

// FailSession 标记会话失败
func (m *SessionManager) FailSession(ctx context.Context, sessionID string) error {
	session, err := m.store.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		return err
	}

	now := time.Now()
	if err := m.store.UpdateSessionStatus(ctx, session.ID, SessionStatusFailed, &now); err != nil {
		slog.Error("fail session failed", "error", err)
		return err
	}

	slog.Info("session failed", "session_id", sessionID)
	return nil
}

// CancelSession 取消会话
func (m *SessionManager) CancelSession(ctx context.Context, sessionID string) error {
	session, err := m.store.GetSessionBySessionID(ctx, sessionID)
	if err != nil {
		return err
	}

	now := time.Now()
	if err := m.store.UpdateSessionStatus(ctx, session.ID, SessionStatusCancelled, &now); err != nil {
		slog.Error("cancel session failed", "error", err)
		return err
	}

	slog.Info("session cancelled", "session_id", sessionID)
	return nil
}

// GetSessionStats 获取会话统计
func (m *SessionManager) GetSessionStats(ctx context.Context, projectID *int64) (*SessionStats, error) {
	// 获取各状态会话数量
	active, totalActive, _ := m.store.ListSessions(ctx, projectID, SessionStatusActive, 0, 1)
	completed, totalCompleted, _ := m.store.ListSessions(ctx, projectID, SessionStatusCompleted, 0, 1)
	failed, totalFailed, _ := m.store.ListSessions(ctx, projectID, SessionStatusFailed, 0, 1)

	stats := &SessionStats{
		TotalSessions:     totalActive + totalCompleted + totalFailed,
		ActiveSessions:    len(active),
		CompletedSessions: len(completed),
		FailedSessions:    len(failed),
	}

	return stats, nil
}

// generateSessionID 生成会话ID
func generateSessionID() string {
	return "sess_" + uuid.New().String()
}

// SessionStats 会话统计
type SessionStats struct {
	TotalSessions     int `json:"total_sessions"`
	ActiveSessions    int `json:"active_sessions"`
	CompletedSessions int `json:"completed_sessions"`
	FailedSessions    int `json:"failed_sessions"`
}
