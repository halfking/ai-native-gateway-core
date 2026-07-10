// Package session_cache_test - three_tier_cache_test.go
//
// 三层缓存系统测试：
// - 第一层：原始会话缓存（Raw Session Cache）
// - 第二层：压缩会话缓存（Compressed Session Cache）
// - 第三层：安全审计后缓存（Audited Session Cache）
package session_cache_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────────────────────────────────────
// 第一层：原始会话缓存
// ──────────────────────────────────────────────────────────────────────────────

// RawSessionCache 存储原始会话的完整消息历史（线程安全）
type RawSessionCache struct {
	mu       sync.RWMutex
	sessions map[string]*RawSession // key: sessionID
}

// RawSession 已在 helpers.go 中定义

func NewRawSessionCache() *RawSessionCache {
	return &RawSessionCache{
		sessions: make(map[string]*RawSession),
	}
}

// AddTurn 添加一轮对话到原始缓存（线程安全）
func (c *RawSessionCache) AddTurn(sessionID, tenantID string, userMsg, assistantMsg Message) *RawSession {
	// 先获取或创建会话（使用读锁检查，写锁创建）
	c.mu.RLock()
	session, exists := c.sessions[sessionID]
	c.mu.RUnlock()

	if !exists {
		c.mu.Lock()
		// 双重检查
		session, exists = c.sessions[sessionID]
		if !exists {
			session = &RawSession{
				SessionID: sessionID,
				TenantID:  tenantID,
				Messages:  []Message{},
				CreatedAt: time.Now(),
			}
			c.sessions[sessionID] = session
		}
		c.mu.Unlock()
	}

	// 对单个session加锁（写操作）
	session.mu.Lock()
	defer session.mu.Unlock()

	// 添加用户消息
	session.Messages = append(session.Messages, userMsg)
	session.MessageHashes = append(session.MessageHashes, hashMessage(userMsg))

	// 添加助手回复
	session.Messages = append(session.Messages, assistantMsg)
	session.MessageHashes = append(session.MessageHashes, hashMessage(assistantMsg))

	session.TurnNumber++
	session.TotalTokens += estimateTokens(userMsg.Content) + estimateTokens(assistantMsg.Content)
	session.UpdatedAt = time.Now()

	return session
}

// Get 获取原始会话（线程安全）
func (c *RawSessionCache) Get(sessionID string) (*RawSession, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, ok := c.sessions[sessionID]
	return session, ok
}

// ──────────────────────────────────────────────────────────────────────────────
// 第二层：压缩会话缓存
// ──────────────────────────────────────────────────────────────────────────────

// CompressedSessionCache 存储压缩后的会话（线程安全）
type CompressedSessionCache struct {
	mu       sync.RWMutex
	sessions map[string]*CompressedSession
}

// CompressedSession 压缩会话状态
type CompressedSession struct {
	SessionID              string          `json:"session_id"`
	TenantID               string          `json:"tenant_id"`
	CompressedMessages     []Message       `json:"compressed_messages"`      // 压缩后的消息
	CompressionStrategy    string          `json:"compression_strategy"`     // 压缩策略
	OriginalMessageCount   int             `json:"original_message_count"`   // 原始消息数
	CompressedMessageCount int             `json:"compressed_message_count"` // 压缩后消息数
	OriginalTokens         int             `json:"original_tokens"`          // 原始token数
	CompressedTokens       int             `json:"compressed_tokens"`        // 压缩后token数
	CompressionRatio       float64         `json:"compression_ratio"`        // 压缩比
	AlignmentMap           []AlignmentInfo `json:"alignment_map"`            // 位置对齐信息
	UpdatedAt              time.Time       `json:"updated_at"`
}

// AlignmentInfo 已在 helpers.go 中定义

func NewCompressedSessionCache() *CompressedSessionCache {
	return &CompressedSessionCache{
		sessions: make(map[string]*CompressedSession),
	}
}

// Compress 压缩原始会话（线程安全）
func (c *CompressedSessionCache) Compress(ctx context.Context, raw *RawSession) (*CompressedSession, error) {
	// 安全读取raw的所有字段
	raw.mu.RLock()
	messages := make([]Message, len(raw.Messages))
	copy(messages, raw.Messages)
	hashes := make([]string, len(raw.MessageHashes))
	copy(hashes, raw.MessageHashes)
	totalTokens := raw.TotalTokens
	sessionID := raw.SessionID
	tenantID := raw.TenantID
	raw.mu.RUnlock()

	compressed := &CompressedSession{
		SessionID:            sessionID,
		TenantID:             tenantID,
		OriginalMessageCount: len(messages),
		OriginalTokens:       totalTokens,
		AlignmentMap:         make([]AlignmentInfo, len(messages)),
		UpdatedAt:            time.Now(),
	}

	if len(messages) <= 10 {
		// 不压缩
		compressed.CompressedMessages = messages
		compressed.CompressionStrategy = "none"
		compressed.CompressedMessageCount = len(messages)
		compressed.CompressedTokens = totalTokens
		compressed.CompressionRatio = 1.0

		// 1:1 对齐
		for i := range messages {
			compressed.AlignmentMap[i] = AlignmentInfo{
				OriginalIndex:   i,
				CompressedIndex: i,
				IsCompressed:    false,
				Hash:            hashes[i],
			}
		}
	} else {
		// 压缩：保留最近10条，前面的生成摘要
		toCompressCount := len(messages) - 10
		toKeepMessages := messages[toCompressCount:]

		// 生成摘要
		summaryContent := fmt.Sprintf("[会话摘要] 前%d条消息已压缩：用户询问了API使用方法，包括认证、请求格式、模型选择等问题。", toCompressCount)
		summaryMsg := Message{
			Role:    "system",
			Content: summaryContent,
		}

		// 构建压缩后的消息列表
		compressed.CompressedMessages = append([]Message{summaryMsg}, toKeepMessages...)
		compressed.CompressionStrategy = "summary"
		compressed.CompressedMessageCount = len(compressed.CompressedMessages)
		compressed.CompressedTokens = estimateTokens(summaryContent)
		for _, msg := range toKeepMessages {
			compressed.CompressedTokens += estimateTokens(msg.Content)
		}
		compressed.CompressionRatio = float64(compressed.CompressedTokens) / float64(totalTokens)

		// 构建对齐映射
		// 前面的消息都被压缩到索引0（摘要）
		for i := 0; i < toCompressCount; i++ {
			compressed.AlignmentMap[i] = AlignmentInfo{
				OriginalIndex:   i,
				CompressedIndex: 0,
				IsCompressed:    true,
				CompressedInto:  0,
				Hash:            hashes[i],
			}
		}
		// 后面的消息保持1:1对齐（偏移+1因为有摘要）
		for i := toCompressCount; i < len(messages); i++ {
			compressed.AlignmentMap[i] = AlignmentInfo{
				OriginalIndex:   i,
				CompressedIndex: i - toCompressCount + 1,
				IsCompressed:    false,
				Hash:            hashes[i],
			}
		}
	}

	// 保存到map（线程安全）
	c.mu.Lock()
	c.sessions[sessionID] = compressed
	c.mu.Unlock()

	return compressed, nil
}

// Get 获取压缩会话（线程安全）
func (c *CompressedSessionCache) Get(sessionID string) (*CompressedSession, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, ok := c.sessions[sessionID]
	return session, ok
}

// Set 设置压缩会话（线程安全）
func (c *CompressedSessionCache) Set(sessionID string, session *CompressedSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[sessionID] = session
}

// ──────────────────────────────────────────────────────────────────────────────
// 第三层：安全审计后缓存
// ──────────────────────────────────────────────────────────────────────────────

// AuditedSessionCache 存储审计后的会话（线程安全）
type AuditedSessionCache struct {
	mu       sync.RWMutex
	sessions map[string]*AuditedSession
}

// AuditedSession 审计后的会话状态
type AuditedSession struct {
	SessionID         string          `json:"session_id"`
	TenantID          string          `json:"tenant_id"`
	AuditedMessages   []Message       `json:"audited_messages"`   // 审计后的消息
	AuditScore        int             `json:"audit_score"`        // 审计分数 0-10
	SecurityScore     int             `json:"security_score"`     // 安全分数 0-10
	SensitiveDetected bool            `json:"sensitive_detected"` // 是否检测到敏感内容
	PIIStripped       bool            `json:"pii_stripped"`       // 是否移除PII
	InjectionDetected bool            `json:"injection_detected"` // 是否检测到注入
	JailbreakDetected bool            `json:"jailbreak_detected"` // 是否检测到越狱
	AlignmentMap      []AlignmentInfo `json:"alignment_map"`      // 与第二层的对齐信息
	CompressedTokens  int             `json:"compressed_tokens"`  // 第二层token数
	AuditedTokens     int             `json:"audited_tokens"`     // 审计后token数
	UpdatedAt         time.Time       `json:"updated_at"`
}

func NewAuditedSessionCache() *AuditedSessionCache {
	return &AuditedSessionCache{
		sessions: make(map[string]*AuditedSession),
	}
}

// Audit 对压缩会话进行安全审计
func (c *AuditedSessionCache) Audit(ctx context.Context, compressed *CompressedSession) (*AuditedSession, error) {
	// 模拟安全审计：检测敏感词、注入、越狱等
	audited := &AuditedSession{
		SessionID:        compressed.SessionID,
		TenantID:         compressed.TenantID,
		AuditedMessages:  make([]Message, len(compressed.CompressedMessages)),
		AlignmentMap:     make([]AlignmentInfo, len(compressed.CompressedMessages)),
		CompressedTokens: compressed.CompressedTokens,
		UpdatedAt:        time.Now(),
	}

	// 默认分数
	audited.AuditScore = 8
	audited.SecurityScore = 9

	// 逐条审计
	for i, msg := range compressed.CompressedMessages {
		auditedMsg := msg
		isModified := false

		// 检测敏感词（模拟）
		if containsSensitiveWords(msg.Content) {
			audited.SensitiveDetected = true
			audited.AuditScore -= 2
		}

		// 检测PII（模拟）
		if containsPII(msg.Content) {
			auditedMsg.Content = stripPII(msg.Content)
			audited.PIIStripped = true
			isModified = true
		}

		// 检测注入（模拟）
		if containsInjection(msg.Content) {
			audited.InjectionDetected = true
			audited.SecurityScore -= 3
		}

		// 检测越狱（模拟）
		if containsJailbreak(msg.Content) {
			audited.JailbreakDetected = true
			audited.SecurityScore -= 4
		}

		audited.AuditedMessages[i] = auditedMsg

		// 构建对齐信息
		audited.AlignmentMap[i] = AlignmentInfo{
			OriginalIndex:   i, // 在第二层的索引
			CompressedIndex: i, // 在第三层的索引（通常1:1）
			IsCompressed:    isModified,
			Hash:            hashMessage(auditedMsg),
		}
	}

	// 计算审计后token数
	for _, msg := range audited.AuditedMessages {
		audited.AuditedTokens += estimateTokens(msg.Content)
	}

	// 保存到map（线程安全）
	c.mu.Lock()
	c.sessions[compressed.SessionID] = audited
	c.mu.Unlock()
	return audited, nil
}

// Get 获取审计后会话（线程安全）
func (c *AuditedSessionCache) Get(sessionID string) (*AuditedSession, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, ok := c.sessions[sessionID]
	return session, ok
}

// Set 设置审计会话（线程安全）
func (c *AuditedSessionCache) Set(sessionID string, session *AuditedSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[sessionID] = session
}

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数（hashMessage, estimateTokens, contains 已在 helpers.go 中定义）
// ──────────────────────────────────────────────────────────────────────────────

func containsSensitiveWords(text string) bool {
	// 模拟敏感词检测
	sensitiveWords := []string{"密码", "password", "秘钥", "secret"}
	for _, word := range sensitiveWords {
		if contains(text, word) {
			return true
		}
	}
	return false
}

func containsPII(text string) bool {
	// 模拟PII检测（简化版）
	return contains(text, "@") || contains(text, "电话") || contains(text, "手机")
}

func stripPII(text string) string {
	// 模拟PII移除（简化版）
	return text + " [PII已移除]"
}

func containsInjection(text string) bool {
	// 模拟注入检测
	injectionPatterns := []string{"ignore previous", "system:", "admin", "DROP TABLE"}
	for _, pattern := range injectionPatterns {
		if contains(text, pattern) {
			return true
		}
	}
	return false
}

func containsJailbreak(text string) bool {
	// 模拟越狱检测
	jailbreakPatterns := []string{"pretend you are", "忽略规则", "forget all instructions"}
	for _, pattern := range jailbreakPatterns {
		if contains(text, pattern) {
			return true
		}
	}
	return false
}

// contains 已在 helpers.go 中定义

// ──────────────────────────────────────────────────────────────────────────────
// 测试用例
// ──────────────────────────────────────────────────────────────────────────────

func TestThreeTierCache(t *testing.T) {
	ctx := context.Background()

	// 初始化三层缓存
	rawCache := NewRawSessionCache()
	compressedCache := NewCompressedSessionCache()
	auditedCache := NewAuditedSessionCache()

	// 生成测试会话
	sessions := GenerateMockSessions(3)

	for _, mockSession := range sessions {
		t.Run(fmt.Sprintf("Session_%s", mockSession.SessionID), func(t *testing.T) {
			sessionID := mockSession.SessionID
			tenantID := mockSession.TenantID

			// 模拟多轮交互
			for _, turn := range mockSession.Turns {
				t.Logf("\n=== 第 %d 轮交互 ===", turn.TurnNumber)

				// ────────────────────────────────────────────────────────
				// 步骤1：用户请求到达，加入第一层缓存（原始会话）
				// ────────────────────────────────────────────────────────
				rawSession := rawCache.AddTurn(sessionID, tenantID, turn.UserMessage, turn.AssistantReply)
				require.NotNil(t, rawSession)
				assert.Equal(t, turn.TurnNumber, rawSession.TurnNumber)
				t.Logf("[L1 原始缓存] 消息数=%d, 总Token=%d", len(rawSession.Messages), rawSession.TotalTokens)

				// ────────────────────────────────────────────────────────
				// 步骤2：调用压缩模块，输出到第二层缓存
				// ────────────────────────────────────────────────────────
				compressedSession, err := compressedCache.Compress(ctx, rawSession)
				require.NoError(t, err)
				require.NotNil(t, compressedSession)
				t.Logf("[L2 压缩缓存] 策略=%s, 原始消息=%d, 压缩后=%d, 压缩比=%.2f",
					compressedSession.CompressionStrategy,
					compressedSession.OriginalMessageCount,
					compressedSession.CompressedMessageCount,
					compressedSession.CompressionRatio)

				// 验证对齐信息
				assert.Len(t, compressedSession.AlignmentMap, compressedSession.OriginalMessageCount)

				// ────────────────────────────────────────────────────────
				// 步骤3：安全审计，输出到第三层缓存
				// ────────────────────────────────────────────────────────
				auditedSession, err := auditedCache.Audit(ctx, compressedSession)
				require.NoError(t, err)
				require.NotNil(t, auditedSession)
				t.Logf("[L3 审计缓存] 审计分=%d, 安全分=%d, 敏感=%v, PII=%v, 注入=%v, 越狱=%v",
					auditedSession.AuditScore,
					auditedSession.SecurityScore,
					auditedSession.SensitiveDetected,
					auditedSession.PIIStripped,
					auditedSession.InjectionDetected,
					auditedSession.JailbreakDetected)

				// ────────────────────────────────────────────────────────
				// 步骤4：将第三层数据发送给LLM（模拟）
				// ────────────────────────────────────────────────────────
				llmRequest := map[string]interface{}{
					"model":    "gpt-4",
					"messages": auditedSession.AuditedMessages,
				}
				llmRequestJSON, err := json.Marshal(llmRequest)
				require.NoError(t, err)
				t.Logf("[LLM 请求] 大小=%d bytes", len(llmRequestJSON))

				// ────────────────────────────────────────────────────────
				// 步骤5：收到LLM响应（已在模拟数据中）
				// ────────────────────────────────────────────────────────

				// ────────────────────────────────────────────────────────
				// 验证三层缓存的一致性
				// ────────────────────────────────────────────────────────
				// 检查第一层
				retrievedRaw, ok := rawCache.Get(sessionID)
				require.True(t, ok)
				assert.Equal(t, rawSession.TurnNumber, retrievedRaw.TurnNumber)

				// 检查第二层
				retrievedCompressed, ok := compressedCache.Get(sessionID)
				require.True(t, ok)
				assert.Equal(t, compressedSession.CompressionStrategy, retrievedCompressed.CompressionStrategy)

				// 检查第三层
				retrievedAudited, ok := auditedCache.Get(sessionID)
				require.True(t, ok)
				assert.Equal(t, auditedSession.AuditScore, retrievedAudited.AuditScore)
			}

			// 会话结束后的统计
			t.Log("\n=== 会话统计 ===")
			rawSession, _ := rawCache.Get(sessionID)
			compressedSession, _ := compressedCache.Get(sessionID)
			auditedSession, _ := auditedCache.Get(sessionID)

			t.Logf("L1(原始): 轮次=%d, 消息数=%d, Token=%d",
				rawSession.TurnNumber, len(rawSession.Messages), rawSession.TotalTokens)
			t.Logf("L2(压缩): 压缩策略=%s, 压缩比=%.2f, Token=%d→%d (节省%.1f%%)",
				compressedSession.CompressionStrategy,
				compressedSession.CompressionRatio,
				compressedSession.OriginalTokens,
				compressedSession.CompressedTokens,
				(1-compressedSession.CompressionRatio)*100)
			t.Logf("L3(审计): 分数=%d/%d, Token=%d",
				auditedSession.AuditScore, auditedSession.SecurityScore, auditedSession.AuditedTokens)

			// 验证Token节省效果
			if len(mockSession.Turns) > 10 {
				assert.Less(t, compressedSession.CompressedTokens, compressedSession.OriginalTokens,
					"压缩应该减少token数量")
			}
		})
	}
}

// TestCacheAlignment 测试三层缓存的位置对齐
func TestCacheAlignment(t *testing.T) {
	ctx := context.Background()
	rawCache := NewRawSessionCache()
	compressedCache := NewCompressedSessionCache()

	sessionID := "test_alignment"
	tenantID := "tenant_001"

	// 添加15轮对话（会触发压缩）
	session := GenerateSingleSession(sessionID, tenantID, time.Now(), 15)

	var rawSession *RawSession
	for _, turn := range session.Turns {
		rawSession = rawCache.AddTurn(sessionID, tenantID, turn.UserMessage, turn.AssistantReply)
	}

	// 压缩
	compressedSession, err := compressedCache.Compress(ctx, rawSession)
	require.NoError(t, err)

	// 验证对齐映射
	t.Log("=== 对齐映射验证 ===")
	for _, align := range compressedSession.AlignmentMap {
		t.Logf("原始[%d] → 压缩[%d], 是否压缩=%v, Hash=%s",
			align.OriginalIndex, align.CompressedIndex, align.IsCompressed, align.Hash)

		if align.IsCompressed {
			// 被压缩的消息应该都指向索引0（摘要）
			assert.Equal(t, 0, align.CompressedIndex, "被压缩的消息应该指向摘要")
		} else {
			// 未压缩的消息应该能在压缩后的列表中找到
			assert.Less(t, align.CompressedIndex, len(compressedSession.CompressedMessages))
		}
	}

	// 验证压缩效果
	assert.Equal(t, 30, len(rawSession.Messages), "15轮对话应该有30条消息")
	assert.Equal(t, "summary", compressedSession.CompressionStrategy, "应该使用summary策略")
	assert.Less(t, len(compressedSession.CompressedMessages), len(rawSession.Messages),
		"压缩后消息数应该减少")
}
