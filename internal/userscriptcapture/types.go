// types.go — capture 摄取的请求/响应模型（P0-3 M1，ADR D2）。
//
// 请求是 ASM capture-batch-v1 的超集：messages[].content（正文）替换
// body_ref（入站不带 ref，由本 API 签发）；其余字段逐字段对齐 ASM
// 契约（contracts/userscript-capture.md 最小消息表）。
package userscriptcapture

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion 与 ASM 摄取端同版（capture-batch-v1）。
const SchemaVersion = "1.0"

const (
	// MaxBatchSize 对齐 ASM（1..100 messages）。
	MaxBatchSize = 100
	// MaxMessageBytes 对齐 ASM（单条 content ≤100000）。
	MaxMessageBytes = 100000
	// MaxEnvelopeBytes 批次 envelope 上限（对齐 ASM DefaultMaxBodyBytes 2MiB）。
	MaxEnvelopeBytes = 2 << 20
)

// Batch 是一次摄取请求体。
type Batch struct {
	SchemaVersion    string    `json:"schema_version"`
	SourceSystem     string    `json:"source_system"`
	ClientSessionKey string    `json:"client_session_key"`
	CorrelationID    string    `json:"correlation_id,omitempty"`
	Messages         []Message `json:"messages"`
}

// Message 是批次内一条消息（含正文）。
type Message struct {
	SourceConversationID string         `json:"source_conversation_id"`
	SourceMessageID      string         `json:"source_message_id"`
	ParentMessageID      *string        `json:"parent_message_id,omitempty"`
	Sequence             int64          `json:"sequence"`
	ContentRevision      int            `json:"content_revision"`
	Role                 string         `json:"role"`
	OccurredAt           time.Time      `json:"occurred_at"`
	IdempotencyKey       string         `json:"idempotency_key"`
	Content              string         `json:"content"`
	Metadata             map[string]any `json:"metadata,omitempty"`
}

// MessageAck 是单条消息的处理结果（响应内）。
type MessageAck struct {
	SourceMessageID string `json:"source_message_id"`
	BodyRef         string `json:"body_ref"`
	Duplicate       bool   `json:"duplicate,omitempty"`
}

// Ack 是批次响应。
type Ack struct {
	BatchID          string       `json:"batch_id"`
	Accepted         int          `json:"accepted"`
	Duplicates       int          `json:"duplicates"`
	CanonicalSession string       `json:"canonical_session,omitempty"` // M3：经存在性校验的 X-Gw-Session-Id 标注
	Messages         []MessageAck `json:"messages"`
}

// Validate 校验批次结构（对齐 ASM 契约的必填与上限）。
func (b *Batch) Validate() error {
	if b.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %q (got %q)", SchemaVersion, b.SchemaVersion)
	}
	if strings.TrimSpace(b.SourceSystem) == "" {
		return errors.New("source_system is required")
	}
	if strings.TrimSpace(b.ClientSessionKey) == "" {
		return errors.New("client_session_key is required")
	}
	if len(b.Messages) < 1 || len(b.Messages) > MaxBatchSize {
		return fmt.Errorf("messages must be 1..%d (got %d)", MaxBatchSize, len(b.Messages))
	}
	seen := map[string]struct{}{}
	for i := range b.Messages {
		m := &b.Messages[i]
		if strings.TrimSpace(m.SourceConversationID) == "" {
			return fmt.Errorf("messages[%d].source_conversation_id is required", i)
		}
		if strings.TrimSpace(m.SourceMessageID) == "" {
			return fmt.Errorf("messages[%d].source_message_id is required", i)
		}
		if strings.TrimSpace(m.IdempotencyKey) == "" {
			return fmt.Errorf("messages[%d].idempotency_key is required", i)
		}
		if _, dup := seen[m.IdempotencyKey]; dup {
			return fmt.Errorf("messages[%d].idempotency_key duplicated in batch", i)
		}
		seen[m.IdempotencyKey] = struct{}{}
		switch m.Role {
		case "user", "assistant", "system", "tool":
		default:
			return fmt.Errorf("messages[%d].role %q invalid", i, m.Role)
		}
		if m.ContentRevision < 1 {
			return fmt.Errorf("messages[%d].content_revision must be >= 1", i)
		}
		if len(m.Content) > MaxMessageBytes {
			return fmt.Errorf("messages[%d].content exceeds %d bytes", i, MaxMessageBytes)
		}
	}
	return nil
}

// BodyRefPrefix 是 capture 正文定位符命名空间（ADR D4）。
const BodyRefPrefix = "internal://capture/"
