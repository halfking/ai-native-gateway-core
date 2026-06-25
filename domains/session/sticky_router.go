// sticky_router.go —— session 领域的"粘性路由器"。
//
// 同一会话优先使用上次凭据，避免在多凭据场景下做不必要的切换。
// 新会话 / 无记录返回空字符串（调用方回退到普通路由）。
package session

import "context"

// StickyRouter 粘性路由器 - 同一会话优先使用上次凭据。
type StickyRouter struct {
	store SessionStore
}

// NewStickyRouter 创建一个新的粘性路由器。
func NewStickyRouter(store SessionStore) *StickyRouter {
	return &StickyRouter{store: store}
}

// GetPreferredCredential 返回会话上次使用的凭据 ID。
// 新会话/无记录返回空字符串（调用方应回退到普通路由）。
func (r *StickyRouter) GetPreferredCredential(ctx context.Context, gwSessionID string) (string, error) {
	if gwSessionID == "" {
		return "", nil
	}
	sess, err := r.store.Get(ctx, gwSessionID)
	if err != nil {
		return "", nil // 新会话，无偏好
	}
	return sess.LastCredentialID, nil
}

// SetPreferredCredential 记录会话使用的凭据 ID。
// （粘性路由的"写"入口，由 routing 阶段在选完凭据后调用）
func (r *StickyRouter) SetPreferredCredential(ctx context.Context, gwSessionID, credentialID string) error {
	if gwSessionID == "" {
		return nil
	}
	sess, err := r.store.Get(ctx, gwSessionID)
	if err != nil {
		return nil // 会话不存在时不创建
	}
	sess.LastCredentialID = credentialID
	// 真正的写回（持久化到 redis）由 Manager.UpdateCacheInfo 风格的
	// 方法承担。本层只更新内存中的 *Session；调用方应根据需要
	// 通过 manager.UpdateXXX 触发持久化。
	return nil
}
