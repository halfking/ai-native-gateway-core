package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/httpx"
)

// ---------------------------------------------------------------------------
// 控制面 scope 鉴权：业务 key 按细粒度能力位访问控制面
//
// 现状（改造前）：控制面**只认管理令牌** ⇒ 网关签发的业务 key（哪怕带
// mcp.admin scope）对控制面毫无作用；key 的 scope 与控制面完全脱节。
//
// 本文件把鉴权扩成「管理令牌 OR 持有对应 scope 的 key」，并给出**逐条可
// 断言**的失败口径。
//
// ★ 契约纪律（为什么这里没有任何 JSON-RPC 错误码）：控制面是**普通 HTTP
//   管理 API**（响应体形如 {"error": "..."}），不是 JSON-RPC 面 ⇒ 本机制
//   **不占用任何 JSON-RPC 错误码**；既有的 -32000~-32007 与既有响应体逐字
//   不动，既有码的取值范围不受影响。
//
// ★ 失败口径（HTTP 状态 + 响应体）：
//
//	无凭据，且管理令牌已配置              ⇒ 401 {"error":"unauthorized"}
//	凭据无效（既非管理令牌也非有效 key）  ⇒ 401 {"error":"unauthorized"}
//	无凭据/凭据无效，且管理令牌**未配置** ⇒ 503 {"error":"control plane disabled: admin token not configured"}
//	凭据有效但 scope 不足                 ⇒ 403 {"error":"forbidden","required_scope":"…","granted_scopes":[…]}
//	账本不可达（判不出 key 是否有效）      ⇒ 503 {"error":"control plane unavailable: credential ledger unreachable"}
//	key 访问其它租户                       ⇒ 403 {"error":"forbidden","reason":"tenant_mismatch"}
//
// ★ 与数据面的**刻意差异**：数据面对账本故障保持既有的 fail-open（放行、交由
//   上游校验）；控制面必须 **fail-closed** —— 管理动作（注册/变更/签发）在
//   身份无法判定时放行，等于把「谁有权改网关」交给网络位置来回答。
// ---------------------------------------------------------------------------

// controlIdentityKey 是控制面身份在请求上下文里的键。
type controlIdentityKey struct{}

// withControlIdentity 把已通过鉴权的身份挂到请求上下文。
//
// 供后续 handler 读取（租户收口、签发时的权限放大防护）。
func withControlIdentity(r *http.Request, id *Identity) *http.Request {
	if id == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), controlIdentityKey{}, id))
}

// controlIdentity 取请求上下文里的控制面身份（没有 ⇒ nil）。
func controlIdentity(r *http.Request) *Identity {
	if r == nil {
		return nil
	}
	id, _ := r.Context().Value(controlIdentityKey{}).(*Identity)
	return id
}

// requestedTenant 取请求显式指定的租户（头或查询参数）。
func requestedTenant(r *http.Request) string {
	tenant := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenant == "" {
		tenant = strings.TrimSpace(r.URL.Query().Get("tenant"))
	}
	return tenant
}

// authorize 校验控制面访问资格。
//
// requiredScope 是该路由所需的能力位；管理令牌恒定放行（超集）。
// 返回的身份会被挂进请求上下文，供 handler 做租户收口与签发防护。
func (c *ControlPlane) authorize(w http.ResponseWriter, r *http.Request, requiredScope string) (*Identity, bool) {
	token := strings.TrimSpace(extractCredential(r))
	if token == "" {
		// 管理令牌既有形态：X-Admin-Token（Authorization/X-Api-Key 优先）。
		token = strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	}
	adminToken := c.gateway.cfg.AdminToken
	if token == "" {
		c.writeAuthFailure(w, adminToken)
		return nil, false
	}

	// ① 管理令牌：全权（既有口径逐字保留）。
	if adminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminToken)) == 1 {
		return &Identity{
			Admin:    true,
			Scopes:   adminScopes(),
			TenantID: c.gateway.cfg.DefaultTenantID,
		}, true
	}

	// ② 业务 key：走与数据面**同一套**账本执法（同一份凭证、同一套失效语义）。
	probe := r.Clone(r.Context())
	probe.Header.Set("X-Api-Key", token)
	id, err := c.gateway.authenticate(r.Context(), probe)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrCredentialInactive) {
			c.writeAuthFailure(w, adminToken)
			return nil, false
		}
		c.writeLedgerUnavailable(w)
		return nil, false
	}
	if id == nil || id.KeyID == "" {
		// 账本不可达 ⇒ authenticate 按既有的 fail-open 语义放行并返回空身份
		// ⇒ 控制面必须 fail-closed（不放行）。
		c.writeLedgerUnavailable(w)
		return nil, false
	}

	// ③ 熔断开关：关掉后控制面**只认管理令牌**（回退到改造前口径）。
	if c.gateway.cfg.ControlScopeDisabled {
		c.writeForbidden(w, requiredScope, id.Scopes, "control_scope_disabled")
		return nil, false
	}

	// ④ scope 判定。
	if !id.HasScope(requiredScope) {
		c.writeForbidden(w, requiredScope, id.Scopes, "scope_denied")
		return nil, false
	}

	// ⑤ 租户收口：业务 key 只能操作自己所属租户；跨租户请求直接拒绝。
	if req := requestedTenant(r); req != "" && req != id.TenantID {
		c.writeTenantMismatch(w, id.TenantID)
		return nil, false
	}

	// ⑥ 记一次「最后使用时间」（best-effort，不阻断访问）⇒ 运维能看出
	//    「哪把业务 key 正在用控制面」。
	c.gateway.touchKey(r.Context(), id)

	return id, true
}

// scoped 给 handler 套上「管理令牌 OR 带 requiredScope 的 key」校验。
func (c *ControlPlane) scoped(requiredScope string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := c.authorize(w, r, requiredScope)
		if !ok {
			return
		}
		h(w, withControlIdentity(r, id))
	}
}

// writeAuthFailure 写「凭据不对」的失败响应。
//
// ★ 未配置管理令牌 ⇒ 503「控制面整体不可用」（既有口径逐字保留：控制面
//
//	未配置管理令牌时不得出现任何可用入口）。
func (c *ControlPlane) writeAuthFailure(w http.ResponseWriter, adminToken string) {
	if adminToken == "" {
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, "application/json", map[string]any{
			"error": "control plane disabled: admin token not configured",
		})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusUnauthorized, "application/json", map[string]any{
		"error": "unauthorized",
	})
}

// writeForbidden 写「身份有效但权限不足」的失败响应（403）。
func (c *ControlPlane) writeForbidden(w http.ResponseWriter, requiredScope string, granted []string, reason string) {
	body := map[string]any{
		"error":          "forbidden",
		"reason":         reason,
		"required_scope": requiredScope,
	}
	if len(granted) > 0 {
		body["granted_scopes"] = granted
	}
	_ = httpx.WriteJSON(w, http.StatusForbidden, "application/json", body)
	c.gateway.audit.Record(AuditEvent{
		Kind:    AuditKindAuthz,
		Outcome: OutcomeDenied,
		Reason:  reason,
		Detail:  map[string]any{"required_scope": requiredScope, "granted_scopes": granted},
	})
}

// writeTenantMismatch 写「跨租户访问」的失败响应（403）。
func (c *ControlPlane) writeTenantMismatch(w http.ResponseWriter, tenantID string) {
	_ = httpx.WriteJSON(w, http.StatusForbidden, "application/json", map[string]any{
		"error":     "forbidden",
		"reason":    "tenant_mismatch",
		"tenant_id": tenantID,
	})
}

// writeLedgerUnavailable 写「账本不可达 ⇒ 无法判定身份」的失败响应（503）。
func (c *ControlPlane) writeLedgerUnavailable(w http.ResponseWriter) {
	_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, "application/json", map[string]any{
		"error": "control plane unavailable: credential ledger unreachable",
	})
}

// ---------------------------------------------------------------------------
// 权限放大防护（委派边界）
//
// 场景：一把 key 能调用 POST /keys（签发凭证）。若不设防，它就能签出一把
// 权限比自己更大的 key（例如自己只有 mcp.register，却签出 mcp.admin）⇒
// 「拿到凭证即可提权」。⇒ 签发与轮换都按**签发者自身的 scope 集合**收口。
// ---------------------------------------------------------------------------

// delegateScopesOK 校验「签发者能否签出这把 key」。
//
// 规则：
//   - 管理令牌（issuer.Admin）不受限（超集，本来就能签任意 scope）。
//   - 业务 key：被签发的**每个** scope 都必须是签发者**自己持有**的 scope。
//     （签发者持有 mcp.admin 时视为全权 —— 与 HasScope 的超集语义一致。）
//   - scopes 省略时沿用既有默认档 [mcp.read, mcp.execute]（IssueKey 的既有
//     默认行为，逐字不变）⇒ 默认档同样要过这道收口。
//
// 返回值：超出签发者权限的那个 scope（空串表示通过）。
func delegateScopesOK(issuer *Identity, requested []string) (string, error) {
	if issuer == nil {
		return "", errors.New("mcp: no issuer identity")
	}
	if issuer.Admin {
		return "", nil
	}
	granted := requested
	if len(granted) == 0 {
		granted = []string{ScopeRead, ScopeExecute}
	}
	for _, s := range granted {
		if !issuer.HasScope(s) {
			return s, fmt.Errorf("scope %q exceeds issuer permissions", s)
		}
	}
	return "", nil
}

// keyScopesWithinIssuer 报告目标 key 的 scope 集合是否都在签发者权限内。
//
// 用于**轮换**：轮换会返回新 key 的明文 ⇒ 若目标 key 的权限高于轮换者，
// 轮换者就等于拿到了一把更 powerful 的凭证 ⇒ 必须收口。
func keyScopesWithinIssuer(issuer *Identity, key *Key) bool {
	if issuer == nil || issuer.Admin {
		return true
	}
	if key == nil {
		return false
	}
	for _, s := range key.Scopes {
		if !issuer.HasScope(s) {
			return false
		}
	}
	return true
}
