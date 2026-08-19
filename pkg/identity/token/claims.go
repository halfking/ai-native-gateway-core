// Package token 提供跨项目 JWT 签发与多 issuer 验证能力。
//
// 设计目标：
//   - HS256 共享密钥 + 跨项目 issuer 白名单 = 跨项目互信（无需 OIDC server）
//   - 不强求统一 Claims 字段命名；通过 Extra map 透传项目特定字段
//   - 校验侧强制 iss/aud/exp/sig 四件套，补齐 openpocket/llm-gateway-go/acc-go 的 P0 gap
package token

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims 是跨项目统一的 JWT 声明视图。
//
// 必填字段（强校验）：Issuer / Subject / Audience / Exp
// 可选字段（弱校验）：UserID / TenantID / Roles / Scope
// 透传字段：Extra —— 各项目原有 Claims（user_id/workspace_id/isAdmin 等）原样保留
type Claims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience string `json:"aud,omitempty"` // 单值或逗号分隔字符串；详见 AudienceStrings

	// 语义映射（来自 sub / project-specific claims）
	UserID   string   `json:"user_id,omitempty"`
	TenantID string   `json:"tenant_id,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Scope    string   `json:"scope,omitempty"`

	// 时间字段
	IssuedAt  int64 `json:"iat,omitempty"`
	ExpiresAt int64 `json:"exp,omitempty"`
	NotBefore int64 `json:"nbf,omitempty"`

	// 透传字段：项目特定 claims 不被丢
	Extra map[string]any `json:"-"`
}

// AudienceStrings 返回 Claims 的规范化 audience 列表。
func (c *Claims) AudienceStrings() []string {
	if strings.TrimSpace(c.Audience) == "" {
		return nil
	}
	parts := strings.Split(c.Audience, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// Valid 在指定时间点上验证 claims 时序合法性。
func (c *Claims) Valid(now time.Time) error {
	if c.Issuer == "" {
		return errors.New("identity-go: missing iss")
	}
	if c.Subject == "" {
		return errors.New("identity-go: missing sub")
	}
	if len(c.AudienceStrings()) == 0 {
		return errors.New("identity-go: missing aud")
	}
	if c.ExpiresAt == 0 {
		return errors.New("identity-go: missing exp")
	}
	nowSec := now.Unix()
	if nowSec >= c.ExpiresAt {
		return errors.New("identity-go: token expired")
	}
	if c.NotBefore != 0 && nowSec < c.NotBefore {
		return errors.New("identity-go: token not yet valid")
	}
	return nil
}

// MarshalJSON 序列化时把 Extra 也合并进顶层 JSON。
func (c *Claims) MarshalJSON() ([]byte, error) {
	type alias Claims
	a := alias(*c)
	m := make(map[string]any)
	b, _ := json.Marshal(a)
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range c.Extra {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON 反序列化时把"非标准字段"装进 Extra。
func (c *Claims) UnmarshalJSON(data []byte) error {
	type alias Claims
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// 先解析标准字段
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*c = Claims(a)
	// 把剩下的字段装进 Extra
	known := map[string]struct{}{
		"iss": {}, "sub": {}, "aud": {},
		"user_id": {}, "tenant_id": {}, "roles": {}, "scope": {},
		"iat": {}, "exp": {}, "nbf": {},
	}
	extras := make(map[string]any)
	for k, v := range raw {
		if _, isKnown := known[k]; !isKnown {
			var anyVal any
			if err := json.Unmarshal(v, &anyVal); err == nil {
				extras[k] = anyVal
			}
		}
	}
	if len(extras) > 0 {
		c.Extra = extras
	}
	return nil
}
