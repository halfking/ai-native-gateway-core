package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// KeyPrefix 是网关签发的 key 明文前缀。
const KeyPrefix = "sk-mcp-"

// HashSecret 计算明文的 SHA-256 十六进制摘要（账本只存摘要）。
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// GenerateSecret 生成一条新的 key 明文。
func GenerateSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mcp: generate secret: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(buf), nil
}

// Identity 是一次调用的身份视图。
type Identity struct {
	// KeyID 凭证 ID。
	KeyID string
	// TenantID / OrgID / UserID / AgentID 归因维度。
	TenantID string
	OrgID    string
	UserID   string
	AgentID  string
	// Scopes 角色位。
	Scopes []string
	// Record 账本记录。
	Record *KeyRecord
	// Admin 是否为控制面管理员。
	Admin bool
}

// HasScope 判断身份是否具备 scope（mcp.admin 隐含全部）。
func (i *Identity) HasScope(scope string) bool {
	if i == nil {
		return false
	}
	if i.Admin {
		return true
	}
	for _, s := range i.Scopes {
		if s == scope || s == ScopeAdmin {
			return true
		}
	}
	return false
}

// extractCredential 从请求头提取凭证明文。
//
// 支持 `Authorization: Bearer <token>` 与 `X-Api-Key: <token>` 两种形态
// （与既有 tools/mcp-gateway 的 extract-key 口径一致）。
func extractCredential(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Api-Key")); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return ""
	}
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return auth
}

// authenticate 解析并校验调用身份。
//
// 顺序：
//  1. 控制面管理令牌（AdminToken）→ 管理员身份。
//  2. 账本查询：远端账本不可达时 fail-open（记录为无绑定身份，交由上游校验）。
//  3. key 不存在 → 401；key 失效/吊销/过期 → 401。
func (g *Gateway) authenticate(ctx context.Context, r *http.Request) (*Identity, error) {
	token := extractCredential(r)
	if token == "" {
		return nil, ErrUnauthorized
	}
	if g.cfg.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(g.cfg.AdminToken)) == 1 {
		return &Identity{
			Admin:    true,
			Scopes:   []string{ScopeAdmin, ScopeRead, ScopeExecute},
			TenantID: g.cfg.DefaultTenantID,
		}, nil
	}

	rec, err := g.ledger.Lookup(ctx, token)
	if err != nil {
		// 远端账本不可达 ⇒ fail-open（放行），但要留下审计痕迹。
		keyChecksTotal.WithLabelValues("ledger_unavailable").Inc()
		// ★ 遗留旧名计数器：既有 tool-allow 与 bindings **各自**查一次账本，
		//   查不到就各自记一次 lookup_error ⇒ 本实现共用一次查询，故两个族
		//   在同一点位各自自增（量级与既有一致）。
		toolAllowLookupErrorsTotal.Inc()
		bindingsLookupErrorsTotal.Inc()
		g.audit.Record(AuditEvent{
			Kind:    AuditKindKey,
			Outcome: OutcomeError,
			Reason:  "ledger_unavailable",
			Detail:  map[string]any{"error": err.Error()},
		})
		return &Identity{TenantID: g.cfg.DefaultTenantID}, nil
	}
	if rec == nil || !rec.Found {
		keyChecksTotal.WithLabelValues("unknown").Inc()
		// ★ 遗留旧名计数器：**远端**账本明确回答「不在账本」⇒ 既有记
		//   lookup_miss（并放行交由后端校验）。本地账本的「不存在」是正常
		//   401，不是账本 miss ⇒ 只在远端模式下计数。
		if g.ledger.Remote() {
			toolAllowLookupMissesTotal.Inc()
			bindingsLookupMissesTotal.Inc()
		}
		return nil, ErrUnauthorized
	}
	if !rec.Active {
		keyChecksTotal.WithLabelValues("inactive").Inc()
		bindingsInactiveTotal.Inc() // 遗留旧名计数器（既有 bindings.js 同名）
		// ★ 既有 bindings.js 对 inactive/revoked 返回 401 + **-32001**
		//   ⇒ 用专属哨兵把「失效」与「不存在」分开。
		return nil, ErrCredentialInactive
	}
	keyChecksTotal.WithLabelValues("ok").Inc()

	tenant := rec.TenantID
	if tenant == "" {
		tenant = g.cfg.DefaultTenantID
	}
	return &Identity{
		KeyID:    rec.ID,
		TenantID: tenant,
		OrgID:    rec.OrgID,
		UserID:   rec.UserID,
		AgentID:  rec.AgentID,
		Scopes:   rec.Scopes,
		Record:   rec,
	}, nil
}

// touchKey 更新凭证最后使用时间（best-effort，不阻断调用）。
func (g *Gateway) touchKey(ctx context.Context, id *Identity) {
	if id == nil || id.KeyID == "" || g.store == nil {
		return
	}
	_ = g.store.TouchKey(ctx, id.TenantID, id.KeyID, g.now())
}

// IssueKey 签发一条凭证，返回明文（只在签发/轮换时可见一次）。
func (g *Gateway) IssueKey(ctx context.Context, spec KeySpec) (*Key, string, error) {
	if g.store == nil {
		return nil, "", fmt.Errorf("mcp: store not configured")
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, "", err
	}
	now := g.now()
	key := Key{
		ID:               "key_" + HashSecret(secret + now.Format(time.RFC3339Nano))[:16],
		TenantID:         spec.TenantID,
		OrgID:            spec.OrgID,
		UserID:           spec.UserID,
		Name:             spec.Name,
		Prefix:           secret[:len(KeyPrefix)+6],
		Hash:             HashSecret(secret),
		Status:           StatusActive,
		Scopes:           spec.Scopes,
		AllowedTools:     spec.AllowedTools,
		DeniedTools:      spec.DeniedTools,
		AllowedAdapters:  spec.AllowedAdapters,
		Bindings:         spec.Bindings,
		AgentID:          spec.AgentID,
		ToolAllowEnforce: spec.ToolAllowEnforce,
		ExpiresAt:        spec.ExpiresAt,
		CreatedAt:        now,
	}
	if len(key.Scopes) == 0 {
		key.Scopes = []string{ScopeRead, ScopeExecute}
	}
	if err := g.store.PutKey(ctx, key); err != nil {
		return nil, "", err
	}
	g.audit.Record(AuditEvent{
		Kind:     AuditKindKey,
		Outcome:  OutcomeAllowed,
		TenantID: key.TenantID,
		OrgID:    key.OrgID,
		UserID:   key.UserID,
		KeyID:    key.ID,
		AgentID:  key.AgentID,
		Reason:   "issued",
	})
	return &key, secret, nil
}

// KeySpec 是签发凭证的入参。
//
// ★ JSON 标签必须与 Key 的出参字段名一致（snake_case）：控制面直接把它
// 当作请求体解码（见 control.go 的 handleCreateKey）。没有标签时
// encoding/json 只能按 Go 字段名做**大小写不敏感**匹配 ⇒ 客户端按
// 仓内其它接口的惯例传 "allowed_tools" / "scopes" 会被**静默丢弃**
// （曾经如此：scopes 为空 ⇒ 落回默认档，看起来"能用"其实没生效）。
type KeySpec struct {
	TenantID         string          `json:"tenant_id,omitempty"`
	OrgID            string          `json:"org_id,omitempty"`
	UserID           string          `json:"user_id,omitempty"`
	AgentID          string          `json:"agent_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	Scopes           []string        `json:"scopes,omitempty"`
	AllowedTools     []string        `json:"allowed_tools,omitempty"`
	DeniedTools      []string        `json:"denied_tools,omitempty"`
	AllowedAdapters  []string        `json:"allowed_adapters,omitempty"`
	Bindings         map[string]bool `json:"bindings,omitempty"`
	ToolAllowEnforce bool            `json:"tool_allow_enforce,omitempty"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
}

// RevokeKey 吊销凭证（离职回收的落点）。
func (g *Gateway) RevokeKey(ctx context.Context, tenantID, keyID string) error {
	key, err := g.store.GetKey(ctx, tenantID, keyID)
	if err != nil {
		return err
	}
	if key == nil {
		return ErrNotFound
	}
	now := g.now()
	key.Status = StatusRevoked
	key.RevokedAt = &now
	if err := g.store.PutKey(ctx, *key); err != nil {
		return err
	}
	g.limiter.Reset(keyID)
	g.audit.Record(AuditEvent{
		Kind:     AuditKindKey,
		Outcome:  OutcomeAllowed,
		TenantID: key.TenantID,
		OrgID:    key.OrgID,
		UserID:   key.UserID,
		KeyID:    key.ID,
		Reason:   "revoked",
	})
	return nil
}

// RotateKey 轮换凭证：吊销旧 key 并签发新 key（返回新明文）。
func (g *Gateway) RotateKey(ctx context.Context, tenantID, keyID string) (*Key, string, error) {
	old, err := g.store.GetKey(ctx, tenantID, keyID)
	if err != nil {
		return nil, "", err
	}
	if old == nil {
		return nil, "", ErrNotFound
	}
	if err := g.RevokeKey(ctx, tenantID, keyID); err != nil {
		return nil, "", err
	}
	spec := KeySpec{
		TenantID:         old.TenantID,
		OrgID:            old.OrgID,
		UserID:           old.UserID,
		AgentID:          old.AgentID,
		Name:             old.Name,
		Scopes:           old.Scopes,
		AllowedTools:     old.AllowedTools,
		DeniedTools:      old.DeniedTools,
		AllowedAdapters:  old.AllowedAdapters,
		Bindings:         old.Bindings,
		ToolAllowEnforce: old.ToolAllowEnforce,
		ExpiresAt:        old.ExpiresAt,
	}
	return g.IssueKey(ctx, spec)
}
