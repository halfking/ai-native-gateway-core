// Package capabilitygate 实现请求面的能力检查（W4.2）：从 API Key 的
// capabilities 清单判断某会话能力是否已授予。fail-closed：任何不确定
// 情况（未注册能力、清单非法）一律拒绝。
//
// 注册表来源是 internal/capability（单一事实源）；本包只做「Key 声明 →
// 请求裁决」这最后一跳，方便 ASM handler 与中间件复用，也可整包被假实现替换。
package capabilitygate

import (
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/capability"
)

// Gate 是能力裁决入口。生产实现持有 capability.Registry；测试可注入假实现。
type Gate struct {
	registry *capability.Registry
}

// NewGate 基于内置能力清单构造。
func NewGate() *Gate {
	return &Gate{registry: capability.NewRegistry()}
}

// Grant 表示一个 Key 的能力授权视图（W4.2 落库后由仓储层还原）。
type Grant struct {
	// KeyID 是 api_keys.id，用于审计日志。
	KeyID int64
	// Capabilities 是该 Key 已被授予的能力 ID 列表。
	Capabilities []string
}

// Check 校验 grant 是否覆盖 capabilityID。
//   - grant 为 nil（Key 不存在）→ 拒绝；
//   - capabilityID 未注册 → 拒绝（防止 handler 拼错能力名被静默放过）；
//   - 授予清单未通过注册表 ValidateSet（含依赖闭包）→ 拒绝；
//   - 否则放行。
func (g *Gate) Check(grant *Grant, capabilityID string) error {
	if grant == nil {
		return fmt.Errorf("capability %s: no api key", capabilityID)
	}
	if _, err := g.registry.Lookup(capabilityID); err != nil {
		return err
	}
	if err := g.registry.ValidateSet(grant.Capabilities); err != nil {
		return fmt.Errorf("api key %d capabilities invalid: %w", grant.KeyID, err)
	}
	for _, id := range grant.Capabilities {
		if strings.TrimSpace(id) == capabilityID {
			return nil
		}
	}
	return fmt.Errorf("api key %d lacks capability %s", grant.KeyID, capabilityID)
}
