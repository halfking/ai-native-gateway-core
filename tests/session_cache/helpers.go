// Package session_cache_test - helpers.go
//
// 共享类型定义和辅助函数（供测试和非测试代码使用）
package session_cache_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Message 表示一条聊天消息
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SessionTurn 表示会话的一轮交互
type SessionTurn struct {
	TurnNumber      int             `json:"turn_number"`
	Timestamp       time.Time       `json:"timestamp"`
	UserMessage     Message         `json:"user_message"`
	AssistantReply  Message         `json:"assistant_reply"`
	RequestBody     json.RawMessage `json:"request_body"`
	OutboundBody    json.RawMessage `json:"outbound_body"`
	ResponseBody    json.RawMessage `json:"response_body"`
	MessageCount    int             `json:"message_count"`
	TokenEstimate   int             `json:"token_estimate"`
	CompressedCount int             `json:"compressed_count,omitempty"`
}

// MockSession 表示一个完整的模拟会话
type MockSession struct {
	SessionID string        `json:"session_id"`
	TenantID  string        `json:"tenant_id"`
	Turns     []SessionTurn `json:"turns"`
	CreatedAt time.Time     `json:"created_at"`
}

// AlignmentInfo 原始位置到压缩位置的对齐信息
type AlignmentInfo struct {
	OriginalIndex   int    `json:"original_index"`
	CompressedIndex int    `json:"compressed_index"`
	IsCompressed    bool   `json:"is_compressed"`
	CompressedInto  int    `json:"compressed_into"`
	Hash            string `json:"hash"`
}

// RawSession 原始会话状态（线程安全）
type RawSession struct {
	mu            sync.RWMutex
	SessionID     string    `json:"session_id"`
	TenantID      string    `json:"tenant_id"`
	Messages      []Message `json:"messages"`
	TurnNumber    int       `json:"turn_number"`
	TotalTokens   int       `json:"total_tokens"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	MessageHashes []string  `json:"message_hashes"`
}

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────────────────────────

// hashMessage 计算消息的hash
func hashMessage(msg Message) string {
	data := fmt.Sprintf("%s:%s", msg.Role, msg.Content)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:8])
}

// estimateTokens 估算文本的token数
func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return len(text) * 4 / 3
}

// contains 检查字符串是否包含子串
func contains(text, substr string) bool {
	return strings.Contains(text, substr)
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
