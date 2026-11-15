package casdoor

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionManager manages user sessions with Redis backend.
type SessionManager struct {
	redis         *redis.Client
	sessionTTL    time.Duration
	refreshTTL    time.Duration
	sessionPrefix string
	mu            sync.RWMutex
	localSessions map[string]*Session // Fallback for when Redis is unavailable
}

// Session represents a user session.
type Session struct {
	ID           string                 `json:"id"`
	UserID       int                    `json:"user_id"`
	Username     string                 `json:"username"`
	TenantID     string                 `json:"tenant_id"`
	CreatedAt    time.Time              `json:"created_at"`
	ExpiresAt    time.Time              `json:"expires_at"`
	LastActivity time.Time              `json:"last_activity"`
	IPAddress    string                 `json:"ip_address,omitempty"`
	UserAgent    string                 `json:"user_agent,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// SessionConfig holds session manager configuration.
type SessionConfig struct {
	RedisClient   *redis.Client
	SessionTTL    time.Duration // Default: 24 hours
	RefreshTTL    time.Duration // Default: 7 days
	SessionPrefix string        // Default: "session:"
}

// NewSessionManager creates a new session manager.
func NewSessionManager(config *SessionConfig) *SessionManager {
	if config == nil {
		config = &SessionConfig{}
	}

	// Set defaults
	if config.SessionTTL == 0 {
		config.SessionTTL = 24 * time.Hour
	}
	if config.RefreshTTL == 0 {
		config.RefreshTTL = 7 * 24 * time.Hour
	}
	if config.SessionPrefix == "" {
		config.SessionPrefix = "session:"
	}

	return &SessionManager{
		redis:         config.RedisClient,
		sessionTTL:    config.SessionTTL,
		refreshTTL:    config.RefreshTTL,
		sessionPrefix: config.SessionPrefix,
		localSessions: make(map[string]*Session),
	}
}

// CreateSession creates a new session for a user.
func (m *SessionManager) CreateSession(ctx context.Context, userID int, username, tenantID string, metadata map[string]interface{}) (*Session, error) {
	// Generate session ID
	sessionID, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}

	now := time.Now()
	session := &Session{
		ID:           sessionID,
		UserID:       userID,
		Username:     username,
		TenantID:     tenantID,
		CreatedAt:    now,
		ExpiresAt:    now.Add(m.sessionTTL),
		LastActivity: now,
		Metadata:     metadata,
	}

	// Store in Redis or fallback to local
	if err := m.storeSession(ctx, session); err != nil {
		return nil, fmt.Errorf("store session: %w", err)
	}

	return session, nil
}

// GetSession retrieves a session by ID.
func (m *SessionManager) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	// Try Redis first
	if m.redis != nil {
		session, err := m.getSessionFromRedis(ctx, sessionID)
		if err == nil {
			return session, nil
		}
	}

	// Fallback to local storage
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.localSessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}

	// Check expiration
	if time.Now().After(session.ExpiresAt) {
		return nil, fmt.Errorf("session expired")
	}

	// ★ 2026-11-16 12h 修订审计轮 P2 根修：返回**副本**，不返回存储里的同一个指针。
	//   原先直接 `return session, nil` ⇒
	//   ① UpdateSession/RefreshSession 的就地改写 = 改存储对象 ⇒
	//      并发读者与写者共享同一对象 ⇒ **数据竞争**（race detector 可复现）；
	//   ② 调用方拿到的「快照」会被别的调用悄悄改掉 ⇒ 调用方无法信任读到的值
	//      （实测：UpdateSession 后调用方手里的 session.LastActivity 也变了，
	//      ⇒ 「更新后时间戳应当推进」这类断言在共享指针下**结构上不可能成立**）。
	return cloneSession(session), nil
}

// cloneSession 返回 session 的深拷贝（含 Metadata map）。
func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	c := *s
	if s.Metadata != nil {
		c.Metadata = make(map[string]interface{}, len(s.Metadata))
		for k, v := range s.Metadata {
			c.Metadata[k] = v
		}
	}
	return &c
}

// UpdateSession updates session metadata and activity time.
func (m *SessionManager) UpdateSession(ctx context.Context, sessionID string, metadata map[string]interface{}) error {
	session, err := m.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}

	// Update metadata and activity time
	session.LastActivity = time.Now()
	if metadata != nil {
		if session.Metadata == nil {
			session.Metadata = make(map[string]interface{})
		}
		for k, v := range metadata {
			session.Metadata[k] = v
		}
	}

	// Store updated session
	return m.storeSession(ctx, session)
}

// RefreshSession extends the session expiration time.
func (m *SessionManager) RefreshSession(ctx context.Context, sessionID string) (*Session, error) {
	session, err := m.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// Extend expiration
	session.ExpiresAt = time.Now().Add(m.sessionTTL)
	session.LastActivity = time.Now()

	// Store updated session
	if err := m.storeSession(ctx, session); err != nil {
		return nil, fmt.Errorf("store refreshed session: %w", err)
	}

	return session, nil
}

// DeleteSession deletes a session.
func (m *SessionManager) DeleteSession(ctx context.Context, sessionID string) error {
	// Delete from Redis
	if m.redis != nil {
		key := m.sessionPrefix + sessionID
		if err := m.redis.Del(ctx, key).Err(); err != nil && err != redis.Nil {
			return fmt.Errorf("delete from redis: %w", err)
		}
	}

	// Delete from local storage
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.localSessions, sessionID)

	return nil
}

// DeleteUserSessions deletes all sessions for a user.
func (m *SessionManager) DeleteUserSessions(ctx context.Context, userID int) error {
	// For Redis, we'd need to maintain a user->sessions index
	// For now, just implement local cleanup
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, session := range m.localSessions {
		if session.UserID == userID {
			delete(m.localSessions, id)
		}
	}

	return nil
}

// ListUserSessions lists all active sessions for a user.
func (m *SessionManager) ListUserSessions(ctx context.Context, userID int) ([]*Session, error) {
	sessions := make([]*Session, 0)

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, session := range m.localSessions {
		if session.UserID == userID && time.Now().Before(session.ExpiresAt) {
			sessions = append(sessions, session)
		}
	}

	return sessions, nil
}

// CleanupExpiredSessions removes expired sessions (should be called periodically).
func (m *SessionManager) CleanupExpiredSessions(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	count := 0

	for id, session := range m.localSessions {
		if now.After(session.ExpiresAt) {
			delete(m.localSessions, id)
			count++
		}
	}

	return count, nil
}

// storeSession stores a session in Redis or local storage.
func (m *SessionManager) storeSession(ctx context.Context, session *Session) error {
	// Try Redis first
	if m.redis != nil {
		key := m.sessionPrefix + session.ID
		ttl := time.Until(session.ExpiresAt)

		// Store session data.
		// ★ 2026-11-16 12h 修订审计轮 P2 根修：补齐 ip_address / user_agent
		//   ⇒ 写入形状与 getSessionFromRedis 的读取形状**逐字段对齐**
		//   （原先这两个字段不落 Redis ⇒ 读回来恒空 ⇒ 写读不对称）。
		pipe := m.redis.Pipeline()
		pipe.HSet(ctx, key, map[string]interface{}{
			"user_id":       session.UserID,
			"username":      session.Username,
			"tenant_id":     session.TenantID,
			"ip_address":    session.IPAddress,
			"user_agent":    session.UserAgent,
			"created_at":    session.CreatedAt.Unix(),
			"expires_at":    session.ExpiresAt.Unix(),
			"last_activity": session.LastActivity.Unix(),
		})
		pipe.Expire(ctx, key, ttl)

		if _, err := pipe.Exec(ctx); err != nil {
			// Fall through to local storage
		} else {
			return nil
		}
	}

	// Fallback to local storage
	m.mu.Lock()
	defer m.mu.Unlock()
	m.localSessions[session.ID] = session

	return nil
}

// getSessionFromRedis retrieves a session from Redis.
func (m *SessionManager) getSessionFromRedis(ctx context.Context, sessionID string) (*Session, error) {
	key := m.sessionPrefix + sessionID

	result, err := m.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis hgetall: %w", err)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("session not found")
	}

	// ★ 2026-11-16 12h 修订审计轮 P2 根修：**解析全部字段**。
	//   原实现只回填 ID（其余字段全部留空），注释自称「In production, properly
	//   parse all fields / This is a simplified version」——
	//   ⇒ 但**生产路径就是 Redis**（redis != nil 时 storeSession 写 Redis、
	//   GetSession 读 Redis）⇒ 「简化版」就是生产版。
	//   ⇒ 后果（会话身份损坏）：GetSession 返回 UserID=0 / Username="" /
	//   TenantID="" / 三个时间零值 ⇒ UpdateSession 拿它改完再 storeSession
	//   ⇒ **把 user_id 覆盖成 0、username/tenant_id 覆盖成空串**
	//   ⇒ 会话身份被抹掉；RefreshSession 同样把 CreatedAt 写成零值。
	//   ⇒ 现在按 storeSession 的写入形状逐字段解析（写入/读取形状对齐）。
	session := &Session{
		ID:           sessionID,
		UserID:       parseSessionInt(result["user_id"]),
		Username:     result["username"],
		TenantID:     result["tenant_id"],
		IPAddress:    result["ip_address"],
		UserAgent:    result["user_agent"],
		CreatedAt:    parseSessionUnix(result["created_at"]),
		ExpiresAt:    parseSessionUnix(result["expires_at"]),
		LastActivity: parseSessionUnix(result["last_activity"]),
		Metadata:     make(map[string]interface{}),
	}

	return session, nil
}

// parseSessionInt 解析 Redis hash 里以字符串形态存下的整数。
func parseSessionInt(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// parseSessionUnix 解析 Redis hash 里以字符串形态存下的 unix 秒。
func parseSessionUnix(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// generateSessionID generates a random session ID.
func generateSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// SessionStats returns session statistics.
type SessionStats struct {
	TotalSessions   int `json:"total_sessions"`
	ActiveSessions  int `json:"active_sessions"`
	ExpiredSessions int `json:"expired_sessions"`
}

// GetStats returns session statistics.
func (m *SessionManager) GetStats(ctx context.Context) (*SessionStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := &SessionStats{
		TotalSessions: len(m.localSessions),
	}

	now := time.Now()
	for _, session := range m.localSessions {
		if now.Before(session.ExpiresAt) {
			stats.ActiveSessions++
		} else {
			stats.ExpiredSessions++
		}
	}

	return stats, nil
}
