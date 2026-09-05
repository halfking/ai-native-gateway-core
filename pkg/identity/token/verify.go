package token

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// VerifyMultiIssuer 用链式多 issuer 列表验证 JWT。
//
// 工作流程：
//  1. 先用 jwt.Parse 验签（强制 HS256）+ exp
//  2. 提取 iss claim
//  3. 在 issuers 列表中查找匹配的 Name 与 Secret
//  4. 若所有 issuer 都不匹配，返回明确错误（不泄露是哪个字段失败）
//  5. 验 aud：claims.AudienceStrings 与 expectedAud 任一匹配即通过；expectedAud 为空则跳过
//
// 返回标准化的 Claims（包含 Extra 透传字段）。
func VerifyMultiIssuer(raw string, issuers []Issuer, expectedAud string) (*Claims, error) {
	if len(raw) == 0 {
		return nil, errors.New("identity-go: empty token")
	}
	if len(issuers) == 0 {
		return nil, errors.New("identity-go: empty issuer allowlist")
	}
	if strings.TrimSpace(expectedAud) == "" {
		return nil, errors.New("identity-go: expected audience required")
	}

	// 收集允许的 secret（用于 jwt.Parse 的 keyfunc）
	allowedSecrets := make(map[string][]byte, len(issuers))
	issuerNames := make(map[string]struct{}, len(issuers))
	for _, iss := range issuers {
		allowedSecrets[iss.Name] = iss.Secret
		issuerNames[iss.Name] = struct{}{}
	}

	parsed, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		// 强制 HS256（拒绝 alg=none 等攻击）
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("identity-go: unexpected signing method %v", t.Header["alg"])
		}
		// 取出 iss claim
		issRaw, ok := t.Claims.(jwt.MapClaims)["iss"]
		if !ok {
			return nil, errors.New("identity-go: missing iss in token header")
		}
		issName, _ := issRaw.(string)
		secret, ok := allowedSecrets[issName]
		if !ok {
			return nil, fmt.Errorf("identity-go: issuer %q not in allowlist", issName)
		}
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))

	if err != nil {
		return nil, fmt.Errorf("identity-go: parse/verify failed: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("identity-go: token marked invalid")
	}

	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("identity-go: claims type assertion failed")
	}

	// 验证 issuer 在白名单
	issName, _ := mc["iss"].(string)
	if _, ok := issuerNames[issName]; !ok {
		return nil, fmt.Errorf("identity-go: issuer %q not allowed", issName)
	}

	// audience 是 resource-server 边界，必须显式匹配。
	if !audienceMatches(mc["aud"], expectedAud) {
		return nil, fmt.Errorf("identity-go: audience mismatch (expected %q)", expectedAud)
	}

	// 转换为标准 Claims
	out := &Claims{}
	out.Issuer = issName
	if sub, ok := mc["sub"].(string); ok {
		out.Subject = sub
	}
	if aud, ok := mc["aud"].(string); ok {
		out.Audience = aud
	} else if audArr, ok := mc["aud"].([]any); ok {
		// JWT 库会反序列化为 []string；保险起见兼容 []any
		parts := make([]string, 0, len(audArr))
		for _, v := range audArr {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			out.Audience = parts[0]
		}
	}
	if v, ok := mc["user_id"].(string); ok {
		out.UserID = v
	}
	if v, ok := mc["tenant_id"].(string); ok {
		out.TenantID = v
	}
	if v, ok := mc["scope"].(string); ok {
		out.Scope = v
	}
	if roles, ok := mc["roles"].([]any); ok {
		out.Roles = make([]string, 0, len(roles))
		for _, r := range roles {
			if s, ok := r.(string); ok {
				out.Roles = append(out.Roles, s)
			}
		}
	}
	if v, ok := mc["iat"].(float64); ok {
		out.IssuedAt = int64(v)
	}
	if v, ok := mc["exp"].(float64); ok {
		out.ExpiresAt = int64(v)
	}
	if v, ok := mc["nbf"].(float64); ok {
		out.NotBefore = int64(v)
	}
	if err := out.Valid(time.Now()); err != nil {
		return nil, err
	}

	// 透传 Extra
	known := map[string]struct{}{
		"iss": {}, "sub": {}, "aud": {},
		"user_id": {}, "tenant_id": {}, "roles": {}, "scope": {},
		"iat": {}, "exp": {}, "nbf": {},
	}
	extras := make(map[string]any)
	for k, v := range mc {
		if _, isKnown := known[k]; !isKnown {
			extras[k] = v
		}
	}
	if len(extras) > 0 {
		out.Extra = extras
	}

	return out, nil
}

// audienceMatches 支持单值、[]string、[]any 三种 JWT audience 表达方式。
func audienceMatches(raw any, expected string) bool {
	switch v := raw.(type) {
	case string:
		return v == expected
	case []string:
		for _, s := range v {
			if s == expected {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	}
	return false
}
