package anomaly

import (
	"time"
)

// AnomalyResult 异常检测结果
type AnomalyResult struct {
	Code     string                 `json:"code"`     // 异常代码（attachment_replay_loop, media_text_imbalance, etc.）
	Severity string                 `json:"severity"` // 严重程度：low, medium, high, critical
	Message  string                 `json:"message"`  // 人类可读的描述
	Metrics  map[string]interface{} `json:"metrics"`  // 详细指标
}

// Request 检测器输入（简化版，避免循环依赖）
type Request struct {
	SessionID   string
	APIKeyID    int64
	TenantID    string
	Attachments []Attachment
	Messages    []Message
	Model       string
}

// Attachment 附件（简化）
type Attachment struct {
	Type string // image, file, etc.
	Data []byte // base64 decoded data
	URL  string // data URL or http URL
	Size int64
}

// Message 消息（简化）
type Message struct {
	Role        string
	Content     string
	Attachments []Attachment
}

// AttachmentFingerprint 附件集合指纹
type AttachmentFingerprint struct {
	Hash          string    // SHA256 of sorted attachment hashes
	Count         int       // 附件数量
	TotalBytes    int64     // 总字节数
	FirstSeenAt   time.Time // 首次出现时间
	LastSeenAt    time.Time // 最后一次出现时间
	OccurrenceSeq []int     // 出现的 turn 序号（用于调试）
}

// SessionWindow 会话窗口（用于跟踪历史）
type SessionWindow struct {
	SessionID      string
	Fingerprints   []string    // 最近 N 笔的指纹列表
	Timestamps     []time.Time // 对应的时间戳
	TotalBytes     int64       // 累计附件字节数
	RequestCount   int         // 请求计数
	FirstRequestAt time.Time   // 首次请求时间
}
