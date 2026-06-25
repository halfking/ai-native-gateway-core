// hook.go —— session 领域的 Pipeline Hook 适配层。
//
// SessionLoaderHook 从 store 加载会话并注入到 envelope。
package session

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// SessionLoaderHook 从 store 加载会话并注入到 envelope。
type SessionLoaderHook struct {
	store  SessionStore
	router *StickyRouter
}

// NewSessionLoaderHook 创建一个新的 SessionLoaderHook。
func NewSessionLoaderHook(store SessionStore, router *StickyRouter) *SessionLoaderHook {
	return &SessionLoaderHook{store: store, router: router}
}

// Name 返回 Hook 名称。
func (h *SessionLoaderHook) Name() string { return "session.load" }

// Priority 返回 Hook 优先级。
func (h *SessionLoaderHook) Priority() int { return 200 }

// Enabled 在 envelope 非 nil 且 SessionID 非空时启用。
func (h *SessionLoaderHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.SessionID != ""
}

// Execute 从 store 加载会话并把"凭据偏好"注入 envelope.Metadata。
func (h *SessionLoaderHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil {
		return nil
	}
	sess, err := h.store.Get(ctx, env.SessionID)
	if err != nil {
		// 找不到会话不算错误 - 继续走匿名会话
		return nil
	}
	// 注入凭据偏好（粘性路由）
	if h.router != nil {
		if cred, _ := h.router.GetPreferredCredential(ctx, env.SessionID); cred != "" {
			if env.Metadata == nil {
				env.Metadata = make(map[string]any)
			}
			env.Metadata["preferred_credential"] = cred
		}
	}
	_ = sess // 暂时保留：将来会用到（e.g. 注入 session 元数据）
	return nil
}

// OnError session 加载失败可降级，不向上传递错误。
func (h *SessionLoaderHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// 编译期检查: SessionLoaderHook 实现了 pipeline.Hook
var _ pipeline.Hook = (*SessionLoaderHook)(nil)
