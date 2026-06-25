// store.go —— session 领域的"存储抽象"层。
//
// 把 Manager 适配成 SessionStore 接口，让 Hook 层只依赖最小化接口，
// 不需要知道 redis 细节。
package session

import "context"

// SessionStore 是 Hook 层使用的最小化会话存储抽象。
// 真实实现是 Manager（基于 redis），但 Hook 只关心 Get(ctx, id)。
type SessionStore interface {
	Get(ctx context.Context, sessionID string) (*Session, error)
}

// 编译期检查：*Manager 实现 SessionStore
var _ SessionStore = (*Manager)(nil)
