// hook.go —— authentication 领域的 Pipeline Hook 适配层。
//
// APIKeyAuthHook 从 envelope.Metadata["api_key"] 取出 key，
// 用 Verifier.Verify 验证后注入到 envelope.APIKey。
package authentication

import (
	"context"
	"errors"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// APIKeyAuthHook 验证请求中的 API key。
type APIKeyAuthHook struct {
	verifier *Verifier
}

// NewAPIKeyAuthHook 创建一个新的 APIKeyAuthHook。
func NewAPIKeyAuthHook(v *Verifier) *APIKeyAuthHook {
	return &APIKeyAuthHook{verifier: v}
}

// Name 返回 Hook 名称。
func (h *APIKeyAuthHook) Name() string { return "auth.api_key" }

// Priority 返回 Hook 优先级。
func (h *APIKeyAuthHook) Priority() int { return 100 }

// Enabled 在 envelope 非 nil 且 APIKey 未注入时启用。
func (h *APIKeyAuthHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.APIKey == nil
}

// Execute 验证 Metadata["api_key"] 字段并注入到 envelope.APIKey。
func (h *APIKeyAuthHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil {
		return nil
	}
	rawKey := env.Metadata["api_key"]
	if rawKey == nil {
		return errors.New("auth: api_key missing from metadata")
	}
	keyStr, ok := rawKey.(string)
	if !ok {
		return errors.New("auth: api_key must be string")
	}
	if keyStr == "" {
		return errors.New("auth: api_key is empty")
	}
	verified, err := h.verifier.Verify(ctx, keyStr)
	if err != nil {
		return err
	}
	env.APIKey = &domain.PipelineAPIKey{
		ID:       strconv.Itoa(verified.ID),
		Key:      keyStr,
		TenantID: verified.TenantID,
		Enabled:  verified.Status != "revoked" && verified.Status != "disabled",
	}
	env.Authenticated = true
	return nil
}

// OnError 认证失败必须上报。
func (h *APIKeyAuthHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil {
		env.Error = err
	}
	return err
}

// 编译期检查: APIKeyAuthHook 实现了 pipeline.Hook
var _ pipeline.Hook = (*APIKeyAuthHook)(nil)
