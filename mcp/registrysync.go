package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件实现 ADR 2026-11-27（MCP Gateway × memora 联邦）§3 D1：
// 拉取式目录联邦。memora registry（/api/v2/mcp/servers）是**目录真源**，
// 网关 Store 是**运行时+治理真源**——同步只创建/更新 registry 拥有的字段，
// 绝不覆盖人工治理字段（Manual 标记），删除走软删（Enabled=false）。
//
// 故障语义（对齐 G3 不变式精神）：同步失败退避重试，期间沿用上次快照——
// 目录联邦故障不得影响数据面。

// RegistrySyncConfig 是联邦同步器配置（env.go 解析）。
type RegistrySyncConfig struct {
	// Enabled 总开关（默认 false，fail-closed）。
	Enabled bool
	// URL memora 基址（如 http://memora:8080）。
	URL string
	// Token 调 /api/v2 的 service JWT。
	Token string
	// Interval 轮询间隔（默认 60s；失败退避 30s→300s 封顶）。
	Interval time.Duration
}

// RegistryJWTConfig 是出站 service JWT 签发配置（ADR §4 D2a）。
type RegistryJWTConfig struct {
	// Secret 签名密钥（= memora JWT_SECRET 同值）。空 ⇒ 不装配签发器。
	Secret string
	// Issuer iss（默认 llm-gateway-mcp）。
	Issuer string
	// Audience aud（必填，校验见 LoadEnvConfig）。
	Audience string
}

// registryServer 镜像 memora mcpregistry.Server 的 JSON 形状
// （memora internal/mcpregistry/service.go：id/name/transport/endpoint/
// auth_mode/auth_ref/tools/enabled/tenant 归属经响应外层）。
// 只取同步需要的字段，未知字段忽略 ⇒ memora 侧加字段不破坏网关同步。
type registryServer struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Transport   string    `json:"transport"`
	Endpoint    string    `json:"endpoint"`
	AuthMode    string    `json:"auth_mode"`
	AuthRef     string    `json:"auth_ref,omitempty"`
	Description string    `json:"description,omitempty"`
	Tools       []string  `json:"tools"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type registryListResponse struct {
	Servers  []registryServer `json:"servers"`
	Count    int              `json:"count"`
	Scope    string           `json:"scope"`
	TenantID string           `json:"tenant_id,omitempty"`
}

// 同步条目处置结果（指标与 /mcp/control/registry/servers 展示用）。
const (
	SyncOutcomeOK          = "ok"            // 已创建或更新
	SyncOutcomeSkipStdio   = "skip_stdio"    // stdio 传输：网关不代理（ADR §5 D3c）
	SyncOutcomeSkipAuthRef = "skip_auth_ref" // api_key_ref 且凭据未解析：登记缺口不注册
	SyncOutcomeDisabled    = "disabled"      // registry 侧 disabled ⇒ 网关软删保位
)

// RegistrySyncStatus 是同步器对外状态（/mcp/control/registry/status）。
type RegistrySyncStatus struct {
	Enabled        bool      `json:"enabled"`
	LastSync       time.Time `json:"last_sync,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	NextRetryAfter time.Time `json:"next_retry_after,omitempty"`
	SnapshotCount  int       `json:"snapshot_count"`
	// Entries 是最近一次成功同步的逐条处置（含跳过原因）。
	Entries []RegistryEntryView `json:"entries,omitempty"`
}

// RegistryEntryView 是联邦条目视图（原始 registry 字段 + 映射结果）。
type RegistryEntryView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Transport     string `json:"transport"`
	AuthMode      string `json:"auth_mode"`
	Endpoint      string `json:"endpoint,omitempty"`
	Enabled       bool   `json:"enabled"`
	Outcome       string `json:"outcome"`
	AdapterName   string `json:"adapter_name,omitempty"`
	TenantID      string `json:"tenant_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
	// CredentialResolved 报告凭据通道状态（api_key_ref 恒 false，ADR §8 R3）。
	CredentialResolved bool `json:"credential_resolved"`
}

// RegistrySyncer 周期拉取 memora registry 并映射进网关 Store。
type RegistrySyncer struct {
	cfg    RegistrySyncConfig
	store  Store
	client *http.Client
	logger interface {
		Warn(msg string, args ...any)
		Info(msg string, args ...any)
		Error(msg string, args ...any)
	}

	mu          sync.Mutex
	lastSync    time.Time
	lastError   string
	nextRetry   time.Time
	entries     []RegistryEntryView
	snapshotN   int
	backoffStep int

	now func() time.Time
}

// NewRegistrySyncer 构造同步器（调用方负责 goroutine 驱动 Run）。
func NewRegistrySyncer(cfg RegistrySyncConfig, store Store) *RegistrySyncer {
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	return &RegistrySyncer{
		cfg:    cfg,
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
}

// Status 返回当前状态快照（控制面只读消费）。
func (s *RegistrySyncer) Status() RegistrySyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := RegistrySyncStatus{
		Enabled:        s.cfg.Enabled,
		LastSync:       s.lastSync,
		LastError:      s.lastError,
		NextRetryAfter: s.nextRetry,
		SnapshotCount:  s.snapshotN,
	}
	if s.entries != nil {
		st.Entries = append([]RegistryEntryView(nil), s.entries...)
	}
	return st
}

// Run 阻塞驱动同步循环（ctx 取消即退出）。失败退避 30s 起步、每次×2、300s 封顶；
// 成功后回到正常间隔。错误记录本身在 SyncOnce 内完成，这里只按成败定等待。
func (s *RegistrySyncer) Run(ctx context.Context) {
	interval := s.cfg.Interval
	backoff := 30 * time.Second
	timer := time.NewTimer(0) // 启动即首拉
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := s.SyncOnce(ctx); err != nil {
			wait := backoff
			backoff *= 2
			if backoff > 300*time.Second {
				backoff = 300 * time.Second
			}
			timer.Reset(wait)
			continue
		}
		backoff = 30 * time.Second
		timer.Reset(interval)
	}
}

// SyncOnce 执行一轮同步：拉取 → 映射 → 落 Store → 刷新状态。
//
// 任何失败返回 error 且 **不改动 Store**（沿用上次快照语义）；
// last_error 由本方法直接记录（Run 与手动触发同口径）。
func (s *RegistrySyncer) SyncOnce(ctx context.Context) error {
	servers, tenantID, err := s.fetch(ctx)
	if err != nil {
		s.setError(err)
		return err
	}

	entries := make([]RegistryEntryView, 0, len(servers))
	toUpsert := make([]Adapter, 0, len(servers))
	seen := map[string]bool{}

	for _, rs := range servers {
		view := RegistryEntryView{
			ID:        rs.ID,
			Name:      rs.Name,
			Transport: rs.Transport,
			AuthMode:  rs.AuthMode,
			Endpoint:  rs.Endpoint,
			Enabled:   rs.Enabled,
		}
		if !rs.Enabled {
			view.Outcome = SyncOutcomeDisabled
			view.AdapterName = adapterNameFor(rs)
			view.TenantID = tenantID
			entries = append(entries, view)
			if a, err := s.store.GetAdapter(ctx, tenantID, adapterNameFor(rs)); err == nil && a != nil {
				// 软删保位（ADR §3）：保留治理字段，只翻 Enabled。
				a.Enabled = false
				a.UpdatedAt = s.now()
				toUpsert = append(toUpsert, *a)
			}
			continue
		}
		if rs.Transport == "stdio" {
			view.Outcome = SyncOutcomeSkipStdio
			view.SkippedReason = "gateway does not proxy stdio upstreams (ADR D3c)"
			entries = append(entries, view)
			continue
		}
		if rs.AuthMode == "api_key_ref" {
			// ADR §8 R3：ref 解析属部署面，本车道登记不做；不注册（注册了
			// 也是必失败的黑洞），看板可见缺口。
			view.Outcome = SyncOutcomeSkipAuthRef
			view.SkippedReason = "credential unresolved (api_key_ref; deployment-plane concern, ADR R3)"
			view.CredentialResolved = false
			entries = append(entries, view)
			continue
		}

		adapter := s.mapAdapter(rs, tenantID)
		// 人工治理字段保护（ADR §3 D1）：Manual=true 的既有条目只同步
		// registry 拥有的骨架字段（Endpoint/Enabled/描述），不覆盖治理字段。
		if existing, err := s.store.GetAdapter(ctx, tenantID, adapter.Name); err == nil && existing != nil && existing.Manual {
			adapter = *existing
			adapter.Endpoint = strings.TrimSpace(rs.Endpoint)
			adapter.Enabled = rs.Enabled
			adapter.UpdatedAt = s.now()
		}
		view.Outcome = SyncOutcomeOK
		view.AdapterName = adapter.Name
		view.TenantID = adapter.TenantID
		view.CredentialResolved = adapter.AuthMode != AuthModeAPIKey || adapter.AuthSecret != ""
		seen[adapter.Name] = true
		entries = append(entries, view)
		toUpsert = append(toUpsert, adapter)
	}

	// 落 Store（先适配器；工具清单由数据面 tools/list 的 AutoRegister 通道
	// 按需拉取，ADR §5 D3b——同步器不做第二套工具登记）。
	for _, a := range toUpsert {
		if err := s.store.PutAdapter(ctx, a); err != nil {
			s.setError(err)
			return fmt.Errorf("mcp: registrysync put adapter %q: %w", a.Name, err)
		}
	}

	s.mu.Lock()
	s.lastSync = s.now()
	s.snapshotN = len(servers)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	s.entries = entries
	s.mu.Unlock()
	s.setError(nil)
	return nil
}

// fetch 调 memora GET /api/v2/mcp/servers（租户级口径，无 X-Project-ID）。
// memora 响应 {"servers":[...],"count":N,"scope":"tenant"}；租户标识从
// 响应 tenant_id 字段或 JWT claim 侧来——网关侧统一落 default 租户之下，
// 除非 memora 响应显式携带（ListTenant 口径目前不带 tenant_id 字段 ⇒ default）。
func (s *RegistrySyncer) fetch(ctx context.Context) ([]registryServer, string, error) {
	url := strings.TrimRight(s.cfg.URL, "/") + "/api/v2/mcp/servers?limit=200"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("mcp: registrysync build request: %w", err)
	}
	req.Header.Set("Authorization", authHeaderFor(s.cfg.Token))
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("mcp: registrysync fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("mcp: registrysync fetch: http %d", resp.StatusCode)
	}
	var body struct {
		registryListResponse
		TenantID string `json:"tenant_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", fmt.Errorf("mcp: registrysync decode: %w", err)
	}
	tenant := body.TenantID
	if tenant == "" {
		tenant = "default"
	}
	return body.Servers, tenant, nil
}

// mapAdapter 把 registry 条目映射为网关 Adapter（ADR §5）：
//   - Name：registry name 规范化为适配器名（网关命名空间前缀）；
//   - AuthMode：llm_gateway/none 直映射；api_key_ref 已在调用方过滤；
//   - Capability：固定 memory（memora 是记忆域上游）；
//   - Labels["project"]：registry 侧 project 归属暂不可从租户级口径取得，
//     留待 per-project 同步（D3a 预留，不阻塞主通道）。
func (s *RegistrySyncer) mapAdapter(rs registryServer, tenantID string) Adapter {
	name := adapterNameFor(rs)
	mode := AuthModeNone
	switch rs.AuthMode {
	case "llm_gateway":
		mode = AuthModeLLMGateway
	case "api_key_ref":
		mode = AuthModeAPIKey
	}
	return Adapter{
		Name:        name,
		TenantID:    tenantID,
		DisplayName: rs.Name,
		Description: rs.Description,
		Endpoint:    strings.TrimSpace(rs.Endpoint),
		AuthMode:    mode,
		Capability:  "memory",
		Enabled:     rs.Enabled,
		Labels:      map[string]string{"source": "registry", "registry_id": rs.ID},
		UpdatedAt:   s.now(),
	}
}

// adapterNameFor 由 registry 条目推导适配器名。registry 的 id 形如
// "mcp-<name-dashed>"（genServerID）；优先用 id（幂等稳定），退回 name 规范化。
func adapterNameFor(rs registryServer) string {
	n := strings.TrimSpace(rs.ID)
	if n == "" {
		n = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(rs.Name), " ", "-"))
	}
	return strings.TrimPrefix(n, "mcp-")
}

// authHeaderFor 拼出 Authorization 头值（"Bearer "+token）。
// 独立成函数以避免字面量拼接在注释/字符串场景下的歧义。
func authHeaderFor(token string) string {
	return "Bearer" + " " + token
}

// setError 记录最近一次同步成败（nil=成功，清错误并推进 lastSync）。
func (s *RegistrySyncer) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastError = err.Error()
		s.nextRetry = s.now().Add(30 * time.Second)
		return
	}
	s.lastError = ""
	s.nextRetry = time.Time{}
}
