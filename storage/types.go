package storage

import (
	"encoding/json"
	"time"
)

// Session 会话元数据。
type Session struct {
	ID        string
	TenantID  string
	UserID    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Metadata  map[string]interface{}
}

// SessionBody 会话内容（大对象），按轮次存储请求/响应原文。
type SessionBody struct {
	TenantID  string
	SessionID string
	TurnNo    int
	Timestamp time.Time
	Request   json.RawMessage
	Response  json.RawMessage
	Metadata  map[string]interface{}
}

// TurnMeta 会话轮次元数据。
type TurnMeta struct {
	TenantID            string
	SessionID           string
	TurnNo              int
	Timestamp           time.Time
	CompressionStrategy string
	PromptTokens        int
	CompletionTokens    int
}

// TurnDetails 会话轮次特征层（Lite 链路 v3 对等：PG17 session_turn_details
// 的 SQLite 瘦身投影）。零值字段落 NULL；数组/JSON 字段以序列化文本落库。
type TurnDetails struct {
	TenantID     string
	SessionID    string
	TurnNo       int
	RequestID    string
	Timestamp    time.Time
	Model        string
	Provider     string
	CredentialID string
	Success      *bool
	StatusCode   int
	ErrorKind    string
	LatencyMs    int
	CostUSD      float64
	ClientModel  string
	RequestType  string
	RequestClass string
	QualityFlags []string
	Attachments  json.RawMessage
}

// RequestLog 请求日志。
type RequestLog struct {
	RequestID  string
	TenantID   string
	SessionID  string
	Timestamp  time.Time
	Method     string
	Path       string
	StatusCode int
	Duration   time.Duration
	Body       json.RawMessage
}

// ListOptions 列表查询选项。
type ListOptions struct {
	Limit  int
	Offset int
	Sort   string
}

// RequestFilter 请求日志过滤条件。
type RequestFilter struct {
	TenantID  string
	SessionID string
	StartTime time.Time
	EndTime   time.Time
	Limit     int
}
