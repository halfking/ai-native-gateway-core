package identity

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/eventbus"
)

// ClientIdentifiedEvent 客户端已识别事件。
type ClientIdentifiedEvent struct {
	IdentityHash string
	VirtualIP    string
	VirtualMAC   string
	TenantID     string
	OccurredAt   time.Time
}

// Type 返回事件类型字符串。
func (e *ClientIdentifiedEvent) Type() string { return "client.identified" }

// Timestamp 返回事件时间戳。
func (e *ClientIdentifiedEvent) Timestamp() time.Time { return e.OccurredAt }

// 编译期检查：ClientIdentifiedEvent 实现 eventbus.Event
var _ eventbus.Event = (*ClientIdentifiedEvent)(nil)

// AuthenticatedEvent 认证通过事件。
type AuthenticatedEvent struct {
	IdentityHash string
	APIKeyID     string
	TenantID     string
	OccurredAt   time.Time
}

// Type 返回事件类型字符串。
func (e *AuthenticatedEvent) Type() string { return "client.authenticated" }

// Timestamp 返回事件时间戳。
func (e *AuthenticatedEvent) Timestamp() time.Time { return e.OccurredAt }

// 编译期检查：AuthenticatedEvent 实现 eventbus.Event
var _ eventbus.Event = (*AuthenticatedEvent)(nil)
