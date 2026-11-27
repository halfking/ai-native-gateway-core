package mcp

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 本文件实现 ADR 2026-11-27（MCP Gateway × memora 联邦）§4 D2a：
// auth_mode=llm_gateway 的出站凭据——网关作为 memora service 调用方，
// 动态签发短 TTL service JWT（memora 入站唯一凭据形态，见其
// internal/middleware/v2jwt.go ServiceJWTVerifier：HS256，必需 claims
// iss/aud/sub/tenant_id/scope/exp，缺一即 401 fail-closed）。
//
// scope 安全边界（ADR §4）：签发 scope 恒为只读集合；write 扩入必须
// 显式传入且经 SensitiveScope 校验拒绝高危域。

// ServiceJWTIssuer 为 llm_gateway 模式适配器签发出站 service JWT。
type ServiceJWTIssuer struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// ServiceJWTTLTTL 是签发 token 的默认有效期。
const ServiceJWTTLTTL = 5 * time.Minute

// NewServiceJWTIssuer 构造签发器。secret 为空 ⇒ 返回错误（fail-closed：
// 未配置密钥的签发器不允许存在，避免装配面误以为已具备注入能力）。
func NewServiceJWTIssuer(secret, issuer, audience string, ttl time.Duration) (*ServiceJWTIssuer, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("mcp: service jwt secret empty (set %s)", EnvRegistryJWTSecret)
	}
	if issuer == "" {
		issuer = "llm-gateway-mcp"
	}
	if ttl <= 0 {
		ttl = ServiceJWTTLTTL
	}
	return &ServiceJWTIssuer{
		secret:   []byte(secret),
		issuer:   issuer,
		audience: audience,
		ttl:      ttl,
		now:      time.Now,
	}, nil
}

// memora mcpserver 工具目录（catalog.go scope 常量）到签发 scope 的静态映射。
// 与 memora 侧逐字对齐：签发超过此集合的 scope 视为装配错误。
var memoraReadOnlyScopes = []string{
	"memory.read",
	"knowledge.read",
	"task.read",
	"toolcall.read",
	"capability.read",
	"fact.read",
	"project.read",
}

// WriteScope 是 memora 唯一的写域（memory_ingest 工具所需）。
const WriteScope = "memory.write"

// ScopeMCPWrite 是网关侧的写授权位（正交，不派生、不被 mcp.admin 之外的位隐含）：
// 只有 key 显式携带它且目标工具/适配器敏感度非 high 时，llm_gateway 出站
// JWT 才扩入 memory.write（ADR §4：write 默认不可达）。
const ScopeMCPWrite = "mcp.write"

// writeGranted 判定本次调用是否可在出站 service JWT 中扩入 write scope。
// 条件（ADR §4，三者同时满足）：
//  1. 调用方 key **显式**携带 mcp.write——不走 HasScope（它会被 mcp.admin
//     隐含），写授权必须显式给到这把 key；
//  2. 工具未被标记 RequireApproval（需审批的工具不走 scope 扩入通道）；
//  3. 工具与适配器敏感度均非 high。
func (g *Gateway) writeGranted(id *Identity, tool *Tool, adapter Adapter) bool {
	if id == nil {
		return false
	}
	explicitWrite := false
	for _, s := range id.Scopes {
		if s == ScopeMCPWrite {
			explicitWrite = true
			break
		}
	}
	if !explicitWrite {
		return false
	}
	if tool != nil {
		if tool.RequireApproval || tool.Sensitivity == SensitivityHigh {
			return false
		}
	}
	if adapter.Sensitivity == SensitivityHigh {
		return false
	}
	return true
}

// newCallID 生成 tools/call 的关联键（ADR §6）：时间有序、48bit 随机尾，
// 形如 mcp-<ms-hex>-<rand-hex>；无外部依赖，进程内唯一性足够（对账键，
// 不是身份凭据）。
func newCallID() string {
	var b [6]byte
	if _, err := crand.Read(b[:]); err != nil {
		// 随机源失败退化为纳秒计数（仍时间有序，唯一性弱化但可用）。
		return fmt.Sprintf("mcp-%013x", time.Now().UnixNano())
	}
	return fmt.Sprintf("mcp-%013x%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

// Issue 按调用方身份签发一枚 service JWT。
//
// tenant 取调用方 mcp key 的租户（RLS 归因主体）；writeGranted 仅在
// 调用方 key 具备显式 write 授权时为 true（ADR §4：默认不可达）。
func (s *ServiceJWTIssuer) Issue(tenantID string, writeGranted bool) (string, error) {
	scopes := make([]string, len(memoraReadOnlyScopes))
	copy(scopes, memoraReadOnlyScopes)
	if writeGranted {
		scopes = append(scopes, WriteScope)
	}
	now := s.now()
	claims := jwt.MapClaims{
		"iss":       s.issuer,
		"aud":       s.audience,
		"sub":       "llm-gateway-mcp",
		"tenant_id": tenantID,
		"actor_id":  "llm-gateway-mcp",
		"scope":     strings.Join(scopes, " "),
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       now.Add(s.ttl).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("mcp: sign service jwt: %w", err)
	}
	return signed, nil
}

// ScopesFor 报告将签发的 scope 集合（观测/测试用，不签发）。
func (s *ServiceJWTIssuer) ScopesFor(writeGranted bool) []string {
	scopes := make([]string, 0, len(memoraReadOnlyScopes)+1)
	scopes = append(scopes, memoraReadOnlyScopes...)
	if writeGranted {
		scopes = append(scopes, WriteScope)
	}
	return scopes
}
