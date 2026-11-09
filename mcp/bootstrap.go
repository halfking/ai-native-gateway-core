package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 密钥引导面（D2/D3）的 Prometheus 指标（指标名与既有 tools/mcp-gateway 对齐）。
var (
	keysApplyTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keys_apply_total",
		Help: "MCP key bootstrap apply requests.",
	})
	keysApplyReplayTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keys_apply_replay_total",
		Help: "MCP key bootstrap apply requests served from the idempotency cache.",
	})
	keysApplyRateLimitedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keys_apply_rate_limited_total",
		Help: "MCP key bootstrap requests rejected by rate limiting.",
	})
	keysClaimTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keys_claim_total",
		Help: "MCP key bootstrap claim requests.",
	})
	keysStatusTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "keys_status_total",
		Help: "MCP key bootstrap status requests.",
	})
)

// BootstrapConfig 是密钥引导面的配置。
//
// 语义与既有 tools/mcp-gateway/src/keys.js 对齐：
//   - apply：免认证、幂等（idempotencyKey）、IP/设备双维限流、策略自动批准。
//   - status：安全状态（无明文 key / 无 token）。
//   - claim：一次性领取（不缓存、不重放）。
type BootstrapConfig struct {
	// Enabled 是否开放免认证引导面。
	//
	// 默认关闭：免认证面意味着「任何人都能来申请凭证」，必须由企业显式开启
	// （并配合 IP/设备限流与人工审批策略）。
	Enabled bool
	// ApplyIPLimit 每 IP 每 10 分钟申请上限。
	ApplyIPLimit int
	// ApplyDeviceLimit 每 deviceName 每 10 分钟申请上限。
	ApplyDeviceLimit int
	// ReadIPLimit status/claim 每 IP 每分钟上限。
	ReadIPLimit int
	// IdempotencyTTL 幂等缓存时长。
	IdempotencyTTL time.Duration
	// AutoApprove 是否自动批准申请（对应既有实现的 MCP_KEYS_AUTO_APPROVE）。
	AutoApprove bool
	// AutoApproveDevices 自动批准的设备名单（空表示不按设备区分）。
	AutoApproveDevices []string
	// TenantID 引导面签发的凭证所属租户。
	TenantID string
}

// BootstrapService 实现密钥引导面。
type BootstrapService struct {
	gateway *Gateway
	cfg     BootstrapConfig

	mu          sync.Mutex
	idempotency map[string]bootstrapCacheEntry
	applyIP     *windowCounter
	applyDevice *windowCounter
	readIP      *windowCounter
}

type bootstrapCacheEntry struct {
	status int
	body   map[string]any
	until  time.Time
}

// windowCounter 是固定窗口计数器。
type windowCounter struct {
	mu      sync.Mutex
	entries map[string]*windowEntry
}

type windowEntry struct {
	count   int
	resetAt time.Time
}

func newWindowCounter() *windowCounter {
	return &windowCounter{entries: map[string]*windowEntry{}}
}

// hit 计数并返回是否放行与剩余等待。
func (c *windowCounter) hit(key string, limit int, window time.Duration) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || now.After(entry.resetAt) {
		entry = &windowEntry{resetAt: now.Add(window)}
		c.entries[key] = entry
	}
	entry.count++
	if len(c.entries) > 10000 {
		for k, v := range c.entries {
			if now.After(v.resetAt) {
				delete(c.entries, k)
			}
		}
	}
	if entry.count > limit {
		return false, time.Until(entry.resetAt)
	}
	return true, 0
}

// NewBootstrapService 构造引导面服务。
func NewBootstrapService(g *Gateway, cfg BootstrapConfig) *BootstrapService {
	if cfg.ApplyIPLimit <= 0 {
		cfg.ApplyIPLimit = 10
	}
	if cfg.ApplyDeviceLimit <= 0 {
		cfg.ApplyDeviceLimit = 3
	}
	if cfg.ReadIPLimit <= 0 {
		cfg.ReadIPLimit = 30
	}
	if cfg.IdempotencyTTL <= 0 {
		cfg.IdempotencyTTL = 15 * time.Minute
	}
	if cfg.TenantID == "" {
		cfg.TenantID = g.cfg.DefaultTenantID
	}
	return &BootstrapService{
		gateway:     g,
		cfg:         cfg,
		idempotency: map[string]bootstrapCacheEntry{},
		applyIP:     newWindowCounter(),
		applyDevice: newWindowCounter(),
		readIP:      newWindowCounter(),
	}
}

// ApplyRequest 是免认证申请请求。
type ApplyRequest struct {
	// IdempotencyKey 幂等键（重放返回同一申请）。
	IdempotencyKey string `json:"idempotencyKey"`
	// DeviceName 设备名（用于设备维限流与自动批准策略）。
	DeviceName string `json:"deviceName"`
	// Name 凭证用途名。
	Name string `json:"name"`
	// AllowedTools 申请的工具范围（空表示按分类默认）。
	AllowedTools []string `json:"allowedTools"`
	// AgentID 绑定智能体。
	AgentID string `json:"agentId"`
}

// ApplyResult 是申请结果。
type ApplyResult struct {
	OK        bool   `json:"ok"`
	RequestID string `json:"requestId,omitempty"`
	// ClaimToken 一次性领取令牌（用于后续 claim）。
	ClaimToken string `json:"claimToken,omitempty"`
	// Status 申请状态：approved / pending。
	Status string `json:"status,omitempty"`
	// Error 失败原因。
	Error string `json:"error,omitempty"`
	// RetryAfterSeconds 限流时的等待秒数。
	RetryAfterSeconds int `json:"retryAfterSeconds,omitempty"`
}

// Apply 处理一次申请。
//
// 顺序与既有实现一致：**先双维限流（防爆破枚举 idempotencyKey），后幂等命中**。
func (b *BootstrapService) Apply(ctx context.Context, req ApplyRequest, ip string) (ApplyResult, int) {
	keysApplyTotal.Inc()

	if ok, wait := b.applyIP.hit(ip, b.cfg.ApplyIPLimit, 10*time.Minute); !ok {
		keysApplyRateLimitedTotal.Inc()
		return ApplyResult{OK: false, Error: "rate_limit_exceeded", RetryAfterSeconds: int(wait.Seconds()) + 1}, http.StatusTooManyRequests
	}
	if req.DeviceName != "" {
		if ok, wait := b.applyDevice.hit(req.DeviceName, b.cfg.ApplyDeviceLimit, 10*time.Minute); !ok {
			keysApplyRateLimitedTotal.Inc()
			return ApplyResult{OK: false, Error: "rate_limit_exceeded", RetryAfterSeconds: int(wait.Seconds()) + 1}, http.StatusTooManyRequests
		}
	}

	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey != "" {
		idempotencyKey = storeKey(ip, req.DeviceName) + "\x00" + idempotencyKey
	}
	if idempotencyKey != "" {
		b.mu.Lock()
		entry, ok := b.idempotency[idempotencyKey]
		b.mu.Unlock()
		if ok && time.Now().Before(entry.until) {
			keysApplyReplayTotal.Inc()
			return ApplyResult{
				OK:         true,
				RequestID:  stringField(entry.body, "requestId"),
				ClaimToken: stringField(entry.body, "claimToken"),
				Status:     stringField(entry.body, "status"),
			}, entry.status
		}
	}

	random, err := GenerateSecret()
	if err != nil {
		return ApplyResult{OK: false, Error: "token_generation_failed"}, http.StatusInternalServerError
	}
	claimToken := "clm_" + strings.TrimPrefix(random, KeyPrefix)
	requestID := "req_" + HashSecret(claimToken)[:16]
	spec := KeySpec{TenantID: b.cfg.TenantID, Name: req.Name, AgentID: req.AgentID,
		Scopes: []string{ScopeRead, ScopeExecute}, AllowedTools: req.AllowedTools}

	record := BootstrapRecord{
		Kind:           "apply",
		Token:          requestID,
		KeyID:          "",
		Status:         "pending",
		TenantID:       b.cfg.TenantID,
		Device:         req.DeviceName,
		IP:             ip,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		ClaimTokenHash: HashSecret(claimToken),
		KeySpec:        &spec,
	}

	autoApprove := b.cfg.AutoApprove
	if autoApprove && len(b.cfg.AutoApproveDevices) > 0 {
		autoApprove = false
		for _, d := range b.cfg.AutoApproveDevices {
			if d == req.DeviceName {
				autoApprove = true
				break
			}
		}
	}

	result := ApplyResult{OK: true, RequestID: requestID, ClaimToken: claimToken, Status: "pending"}
	if autoApprove {
		key, secret, err := b.gateway.IssueKey(ctx, spec)
		if err != nil {
			return ApplyResult{OK: false, Error: "key_issue_failed"}, http.StatusInternalServerError
		}
		record.KeyID = key.ID
		record.Plaintext = secret
		record.Status = "approved"
		result.Status = "approved"
	}
	if err := b.gateway.Store().PutBootstrap(ctx, record); err != nil {
		return ApplyResult{OK: false, Error: "storage_failed"}, http.StatusInternalServerError
	}

	if idempotencyKey != "" {
		b.mu.Lock()
		b.idempotency[idempotencyKey] = bootstrapCacheEntry{
			status: http.StatusOK,
			body: map[string]any{
				"requestId":  requestID,
				"claimToken": claimToken,
				"status":     result.Status,
			},
			until: time.Now().Add(b.cfg.IdempotencyTTL),
		}
		b.mu.Unlock()
	}
	return result, http.StatusOK
}

// Status 返回申请状态（**不含明文**）。
func (b *BootstrapService) Status(ctx context.Context, requestID, ip string) (map[string]any, int) {
	keysStatusTotal.Inc()
	if ok, _ := b.readIP.hit(ip, b.cfg.ReadIPLimit, time.Minute); !ok {
		return map[string]any{"ok": false, "error": "rate_limit_exceeded"}, http.StatusTooManyRequests
	}
	record, err := b.gateway.Store().GetBootstrap(ctx, "apply", requestID)
	if err != nil {
		return map[string]any{"ok": false, "error": "storage_failed"}, http.StatusInternalServerError
	}
	if record == nil {
		return map[string]any{"ok": false, "error": "not_found"}, http.StatusNotFound
	}
	return map[string]any{
		"ok":        true,
		"requestId": record.Token,
		"status":    record.Status,
		"device":    record.Device,
		"createdAt": record.CreatedAt,
		"claimed":   record.Plaintext == "",
	}, http.StatusOK
}

// Claim 一次性领取明文凭证与后端绑定。
//
// 领取后立即清空存储的明文（**不缓存、不重放**）。
func (b *BootstrapService) Claim(ctx context.Context, requestID, claimToken, ip string) (map[string]any, int) {
	keysClaimTotal.Inc()
	if ok, _ := b.readIP.hit(ip, b.cfg.ReadIPLimit, time.Minute); !ok {
		return map[string]any{"ok": false, "error": "rate_limit_exceeded"}, http.StatusTooManyRequests
	}
	if requestID == "" || claimToken == "" {
		return map[string]any{"ok": false, "error": "requestId and claimToken are required"}, http.StatusBadRequest
	}
	record, err := b.gateway.Store().ClaimBootstrap(ctx, requestID, HashSecret(claimToken), time.Now())
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return map[string]any{"ok": false, "error": "not_found"}, http.StatusNotFound
		case errors.Is(err, errClaimed):
			return map[string]any{"ok": false, "error": "already_claimed"}, http.StatusConflict
		case errors.Is(err, errClaimToken), errors.Is(err, errNotApproved):
			return map[string]any{"ok": false, "error": err.Error()}, http.StatusForbidden
		default:
			return map[string]any{"ok": false, "error": "storage_failed"}, http.StatusInternalServerError
		}
	}

	// 后端绑定：网关是统一入口，所有适配器都经同一数据面地址可达。
	bindings := map[string]string{}
	adapters, err := b.gateway.Store().ListAdapters(ctx, record.TenantID)
	if err == nil {
		for _, a := range adapters {
			if !a.Enabled {
				continue
			}
			bindings[a.Name] = "/mcp"
		}
	}
	return map[string]any{
		"ok":       true,
		"apiKey":   record.Plaintext,
		"keyId":    record.KeyID,
		"bindings": bindings,
	}, http.StatusOK
}

// ApproveApplication 人工批准一笔申请（管理员动作）。
func (b *BootstrapService) ApproveApplication(ctx context.Context, requestID, name string) (map[string]any, int) {
	return b.approveApplicationFor(ctx, requestID, name, nil)
}

func (b *BootstrapService) approveApplicationFor(ctx context.Context, requestID, name string, issuer *Identity) (map[string]any, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	record, err := b.gateway.Store().GetBootstrap(ctx, "apply", requestID)
	if err != nil {
		return map[string]any{"ok": false, "error": "storage_failed"}, http.StatusInternalServerError
	}
	if record == nil {
		return map[string]any{"ok": false, "error": "not_found"}, http.StatusNotFound
	}
	if record.Status != "pending" {
		return map[string]any{"ok": false, "error": "not_pending", "status": record.Status}, http.StatusConflict
	}
	spec := KeySpec{TenantID: record.TenantID, Scopes: []string{ScopeRead, ScopeExecute}}
	if record.KeySpec != nil {
		spec = *record.KeySpec
		spec.TenantID = record.TenantID
	}
	if name != "" {
		spec.Name = name
	}
	if issuer != nil && !issuer.Admin {
		_, scopeErr := delegateScopesOK(issuer, spec.Scopes)
		if scopeErr != nil || !delegationWithinIssuer(issuer, spec) {
			return map[string]any{"ok": false, "error": "delegation_escalation"}, http.StatusForbidden
		}
	}
	key, secret, err := b.gateway.IssueKey(ctx, spec)
	if err != nil {
		return map[string]any{"ok": false, "error": "key_issue_failed"}, http.StatusInternalServerError
	}
	record.KeyID = key.ID
	record.Plaintext = secret
	record.Status = "approved"
	record.UpdatedAt = time.Now()
	if err := b.gateway.Store().PutBootstrap(ctx, *record); err != nil {
		return map[string]any{"ok": false, "error": "storage_failed"}, http.StatusInternalServerError
	}
	return map[string]any{"ok": true, "requestId": requestID, "status": record.Status}, http.StatusOK
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// ClientIP 提取客户端 IP（信任 X-Forwarded-For 的第一跳）。
func ClientIP(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := splitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	return host
}

func splitHostPort(addr string) (string, string, error) {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return addr, "", fmt.Errorf("no port in %q", addr)
	}
	return addr[:idx], addr[idx+1:], nil
}
