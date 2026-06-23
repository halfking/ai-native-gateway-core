package domain

// SessionContext 封装会话粘性领域的上下文。
type SessionContext struct {
	Key         string
	StickyKey   string
	StickyModel string
	StickySlot  string
	IsNew       bool
	ReusedKey   bool
}
