package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionPreferenceEntry (V3.1, 2026-06-26) 描述一次"会话偏好"的完整信息。
//
// 不仅包含 credentialID，还包含写入时的 model。模型切换检测需要：
//   - 比对当前请求的 ClientModel 与 entry.Model
//   - 不一致 → Clear（让 P2C 重选）
//
// JSON 格式便于未来扩展（priority / set_at / source 等）。
type SessionPreferenceEntry struct {
	CredentialID int    `json:"credential_id"`
	Model        string `json:"model,omitempty"` // credential 匹配后的模型（cand.RawModel），不是客户端请求模型
	SetAt        int64  `json:"set_at"`          // UnixMilli
}

// SessionPreferenceStore 存储"会话偏好的 credential"映射。
//
// V3.1 设计：会话偏好是轻量级映射，只记"当前会话倾向用哪个 credential"。
// 删除该映射 = 强制重新选择；保留 = 优先复用。
//
// 典型使用：
//   - 成功调用后写：session_pref:<sessionID> = JSON{cred_id, model, set_at}
//   - 切模型时清：DEL session_pref:<sessionID>
//   - 路由前查：PlanCandidates 优先排到该 credential
//
// 不存储健康状态：那是 RouteNodeState 的职责。
//
// Redis Key: session_pref:<sessionID>  (String JSON)  TTL=7d
type SessionPreferenceStore struct {
	client *redis.Client
	ttl    time.Duration
}

// DefaultSessionPreferenceTTL 与 session 一致（7 天）。
const DefaultSessionPreferenceTTL = 7 * 24 * time.Hour

// NewSessionPreferenceStore 创建一个 SessionPreferenceStore。
// redisClient=nil 时所有操作返回 no-op（禁用）。
func NewSessionPreferenceStore(redisClient *redis.Client, ttl time.Duration) *SessionPreferenceStore {
	if ttl == 0 {
		ttl = DefaultSessionPreferenceTTL
	}
	return &SessionPreferenceStore{
		client: redisClient,
		ttl:    ttl,
	}
}

// sessionPreferenceRedisKey 构造 Redis key。
func sessionPreferenceRedisKey(sessionID string) string {
	return fmt.Sprintf("session_pref:%s", sessionID)
}

// Get 读取会话偏好。
//
// 返回 (entry, found, err)：
//   - entry: 命中时返回完整 entry（包含 credentialID 和 model）
//   - found: 是否命中
//   - err: Redis 错误（调用方应降级为无偏好）
//
// nil store / nil client 时返回 (nil, false, nil)。
func (s *SessionPreferenceStore) Get(ctx context.Context, sessionID string) (*SessionPreferenceEntry, bool, error) {
	if s == nil || s.client == nil || sessionID == "" {
		return nil, false, nil
	}
	key := sessionPreferenceRedisKey(sessionID)
	val, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		// Redis 错误：降级为无偏好
		return nil, false, nil
	}
	var entry SessionPreferenceEntry
	if err := json.Unmarshal([]byte(val), &entry); err != nil {
		// 数据损坏：当作未命中
		return nil, false, nil
	}
	if entry.CredentialID <= 0 {
		return nil, false, nil
	}
	return &entry, true, nil
}

// Set 写入会话偏好（覆盖式）。
//
// 在成功调用后调用：标记"这次会话倾向继续用这个 credential"。
//
// 参数：
//   - sessionID: 会话 ID
//   - credentialID: 偏好指向的 credential
//   - model: 偏好指向的 credential 匹配后的模型名（cand.RawModel），用于切模型检测
//
// nil store / nil client / sessionID="" 时返回 nil（no-op）。
func (s *SessionPreferenceStore) Set(ctx context.Context, sessionID string, credentialID int, model string) error {
	if s == nil || s.client == nil || sessionID == "" {
		return nil
	}
	entry := SessionPreferenceEntry{
		CredentialID: credentialID,
		Model:        model,
		SetAt:        time.Now().UnixMilli(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	key := sessionPreferenceRedisKey(sessionID)
	return s.client.Set(ctx, key, data, s.ttl).Err()
}

// Clear 清除会话偏好（在切模型、显式 reset 等场景调用）。
//
// 返回 ErrSessionPreferenceNotFound 表示 key 不存在（用于判断是否真清除了什么）。
func (s *SessionPreferenceStore) Clear(ctx context.Context, sessionID string) error {
	if s == nil || s.client == nil || sessionID == "" {
		return nil
	}
	key := sessionPreferenceRedisKey(sessionID)
	deleted, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrSessionPreferenceNotFound
	}
	return nil
}

// ErrSessionPreferenceNotFound 表示要清除的 key 不存在。
var (
	ErrSessionPreferenceNotFound = errors.New("session preference not found")
	ErrSessionPreferenceInvalid  = errors.New("session preference invalid")
)

// GetCredentialID 是 Get 的便捷包装，仅返回 credentialID。
//
// 调用方如果不关心 model 字段，可以用这个。返回 (credentialID, found)。
func (s *SessionPreferenceStore) GetCredentialID(ctx context.Context, sessionID string) (int, bool) {
	entry, found, _ := s.Get(ctx, sessionID)
	if !found {
		return 0, false
	}
	return entry.CredentialID, true
}