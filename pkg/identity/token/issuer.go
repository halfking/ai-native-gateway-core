package token

import (
	"fmt"
	"os"
	"strings"
)

// Issuer 描述一个可被验证的 JWT 颁发方。
//
// 同一 secret 可签发多种 issuer（共享对称密钥场景），
// 或每个 issuer 配独立 secret（隔离场景）。VerifyMultiIssuer 接受混搭。
type Issuer struct {
	Name   string // iss claim 期望值，如 "redclaw"
	Secret []byte // HS256 共享密钥
}

// String 安全地遮蔽 secret 字节，便于日志。
func (i Issuer) String() string {
	return fmt.Sprintf("Issuer{Name:%q, SecretLen:%d}", i.Name, len(i.Secret))
}

// Allowlist 从 env 字符串解析 issuer 列表。
//
// env 格式（逗号分隔）：
//
//	"redclaw,memora,llm-gateway,pocket,acc"
//
// ai-session-manager 不在此列表中：它与 llm-gateway-go 共用用户 issuer、tenant
// 和 scope，不拥有独立的用户身份 issuer。
//
// 对每个 issuer name 复用同一个 sharedSecret（HS256 共享密钥模式）。
func Allowlist(envValue string, sharedSecret []byte) ([]Issuer, error) {
	if len(sharedSecret) < 32 {
		return nil, fmt.Errorf("identity-go: shared secret too short (need >=32 bytes, got %d)", len(sharedSecret))
	}
	raw := strings.TrimSpace(envValue)
	if raw == "" {
		return nil, fmt.Errorf("identity-go: empty issuer allowlist")
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]Issuer, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue // 静默去重
		}
		seen[name] = struct{}{}
		out = append(out, Issuer{Name: name, Secret: sharedSecret})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("identity-go: allowlist contains no valid issuer")
	}
	return out, nil
}

// LoadSharedSecret 从 env 读取共享密钥。
// 推荐变量名：IDENTITY_SHARED_SECRET。
// 必须 >= 32 字节以满足 HS256 安全性。
func LoadSharedSecret(envName string) ([]byte, error) {
	v := strings.TrimSpace(os.Getenv(envName))
	if v == "" {
		return nil, fmt.Errorf("identity-go: env %s not set", envName)
	}
	if len(v) < 32 {
		return nil, fmt.Errorf("identity-go: env %s must be >= 32 bytes (got %d)", envName, len(v))
	}
	return []byte(v), nil
}
