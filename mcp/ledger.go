package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// KeyRecord 是账本视角的凭证记录（网关只看这个视图）。
type KeyRecord struct {
	// Found 账本里是否有这把 key。
	Found bool
	// Active 是否处于有效状态（未吊销/未过期）。
	Active bool
	// ID 凭证 ID。
	ID string
	// TenantID / OrgID / UserID / AgentID 归因维度。
	TenantID string
	OrgID    string
	UserID   string
	AgentID  string
	// Scopes 角色位。
	Scopes []string
	// Bindings 一 key 多后端单边禁用。
	Bindings map[string]bool
	// AllowedTools / DeniedTools 「key × 工具」允许表。
	AllowedTools []string
	DeniedTools  []string
	// AllowedAdapters 允许访问的适配器（空表示全部）。
	AllowedAdapters []string
	// Enforce 是否强制执行允许表（false 表示该 key 从未受限）。
	Enforce bool
	// ExpiresAt 过期时间。
	ExpiresAt *time.Time
}

// Ledger 是凭证账本抽象。
//
// 两种实现：
//   - LocalLedger：网关自身即权威账本（独立部署 / 无外部 IAM 的场景）。
//   - RemoteLedger：对接外部账本（如 ACC），保留既有 tools/mcp-gateway 的
//     「网关只做执法点、账本另有权威」的语义，且**保持 fail-open**：
//     外部账本不可达时放行并审计，不因账本故障打断数据面。
type Ledger interface {
	// Lookup 按明文 key 查询凭证记录。
	Lookup(ctx context.Context, apiKey string) (*KeyRecord, error)
	// Remote 报告该账本是否为外部账本（影响 fail-open 策略与审计口径）。
	Remote() bool
}

// ---------------------------------------------------------------------------
// 本地账本
// ---------------------------------------------------------------------------

// LocalLedger 以 Store 为权威的本地账本。
type LocalLedger struct {
	store Store
	now   func() time.Time
}

// NewLocalLedger 构造本地账本。
func NewLocalLedger(store Store) *LocalLedger {
	return &LocalLedger{store: store, now: time.Now}
}

// Remote 实现 Ledger。
func (l *LocalLedger) Remote() bool { return false }

// Lookup 实现 Ledger。
func (l *LocalLedger) Lookup(ctx context.Context, apiKey string) (*KeyRecord, error) {
	if apiKey == "" {
		return &KeyRecord{Found: false}, nil
	}
	hash := HashSecret(apiKey)
	keys, err := l.store.ListKeys(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range keys {
		k := keys[i]
		if k.Hash != hash {
			continue
		}
		return recordFromKey(&k, l.now()), nil
	}
	return &KeyRecord{Found: false}, nil
}

func recordFromKey(k *Key, now time.Time) *KeyRecord {
	return &KeyRecord{
		Found:           true,
		Active:          k.Active(now),
		ID:              k.ID,
		TenantID:        k.TenantID,
		OrgID:           k.OrgID,
		UserID:          k.UserID,
		AgentID:         k.AgentID,
		Scopes:          k.Scopes,
		Bindings:        k.Bindings,
		AllowedTools:    k.AllowedTools,
		DeniedTools:     k.DeniedTools,
		AllowedAdapters: k.AllowedAdapters,
		Enforce:         k.ToolAllowEnforce,
		ExpiresAt:       k.ExpiresAt,
	}
}

// ---------------------------------------------------------------------------
// 远端账本（ACC 兼容）
// ---------------------------------------------------------------------------

// RemoteLedger 对接外部账本（ACC 的 mcp-keys 接口）。
//
// 语义与既有 tools/mcp-gateway/src/bindings.js + tool-allow.js 对齐：
//   - 远端不可达 / 超时 / 返回非 200 → **fail-open**（放行 + 审计）。
//   - 远端明确回答「key 不在账本」→ 放行（交由后端自己校验）。
//   - 远端回答 inactive → 401。
//   - 远端回答 enforce=false → 放行（空白名单＝从未受限）。
type RemoteLedger struct {
	baseURL string
	token   string
	client  *http.Client
	now     func() time.Time

	mu       sync.Mutex
	cache    map[string]remoteCacheEntry
	cacheTTL time.Duration
	timeout  time.Duration
}

type remoteCacheEntry struct {
	rec   *KeyRecord
	until time.Time
}

// NewRemoteLedger 构造远端账本。
//
// token 为内部共享密钥（对应 ACC 的 X-Internal-Token）；为空时不做查询
// （等价于执法关闭，与既有实现的 fail-open 一致）。
func NewRemoteLedger(baseURL, token string, cacheTTL, timeout time.Duration) *RemoteLedger {
	if cacheTTL <= 0 {
		cacheTTL = 10 * time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &RemoteLedger{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		client:   &http.Client{Timeout: timeout},
		now:      time.Now,
		cache:    map[string]remoteCacheEntry{},
		cacheTTL: cacheTTL,
		timeout:  timeout,
	}
}

// Remote 实现 Ledger。
func (l *RemoteLedger) Remote() bool { return true }

// Lookup 实现 Ledger（远端故障时返回 fail-open 记录）。
func (l *RemoteLedger) Lookup(ctx context.Context, apiKey string) (*KeyRecord, error) {
	if apiKey == "" {
		return &KeyRecord{Found: false}, nil
	}
	if l.token == "" || l.baseURL == "" {
		// 未配置外部账本：执法关闭（放行）。
		return &KeyRecord{Found: false}, nil
	}

	l.mu.Lock()
	if entry, ok := l.cache[apiKey]; ok && l.now().Before(entry.until) {
		l.mu.Unlock()
		return entry.rec, nil
	}
	l.mu.Unlock()

	rec, err := l.fetch(ctx, apiKey)
	if err != nil {
		// fail-open：远端不可达 → 放行（Found=false 表示交由后端校验）。
		return &KeyRecord{Found: false}, err
	}

	l.mu.Lock()
	l.cache[apiKey] = remoteCacheEntry{rec: rec, until: l.now().Add(l.cacheTTL)}
	l.mu.Unlock()
	return rec, nil
}

func (l *RemoteLedger) fetch(ctx context.Context, apiKey string) (*KeyRecord, error) {
	endpoint := l.baseURL + "/api/internal/mcp-keys/bindings?key=" + url.QueryEscape(apiKey)
	reqCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Token", l.token)
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// ★ 404 有**两种**含义，既有实现为此分了两个计数器（语义不能混）：
		//   · 远端**没有这条路由**（版本没部署）⇒ 整个执法面是坏的
		//     ⇒ tool_allow_acc_route_missing_total；
		//   · 404 的**形状不认得** ⇒ 我们其实没弄清楚这把 key 在不在账本
		//     ⇒ 不能当成「查过了，没问题」⇒ tool_allow_acc_404_unrecognized_total。
		//   两者都 fail-open（转发面不因审计面坏掉而中断），但必须喊出来。
		if resp.StatusCode == http.StatusNotFound {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			if isLedgerRouteMissing(body) {
				toolAllowAccRouteMissingTotal.Inc()
				return nil, fmt.Errorf("mcp: remote ledger route missing (enforcement plane not deployed): %s", truncate(string(body), 200))
			}
			toolAllowAcc404UnrecognizedTotal.Inc()
			return nil, fmt.Errorf("mcp: remote ledger returned unrecognized 404: %s", truncate(string(body), 200))
		}
		return nil, fmt.Errorf("mcp: remote ledger status %d", resp.StatusCode)
	}
	var payload struct {
		Found            bool            `json:"found"`
		Active           bool            `json:"active"`
		ID               string          `json:"id"`
		TenantID         string          `json:"tenant_id"`
		OrgID            string          `json:"org_id"`
		UserID           string          `json:"user_id"`
		AgentID          string          `json:"agent_id"`
		Scopes           []string        `json:"scopes"`
		Bindings         map[string]bool `json:"bindings"`
		AllowedTools     []string        `json:"allowed_tools"`
		DeniedTools      []string        `json:"denied_tools"`
		AllowedAdapters  []string        `json:"allowed_adapters"`
		ToolAllowEnforce bool            `json:"tool_allow_enforce"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("mcp: decode remote ledger: %w", err)
	}
	return &KeyRecord{
		Found:           payload.Found,
		Active:          payload.Active,
		ID:              payload.ID,
		TenantID:        payload.TenantID,
		OrgID:           payload.OrgID,
		UserID:          payload.UserID,
		AgentID:         payload.AgentID,
		Scopes:          payload.Scopes,
		Bindings:        payload.Bindings,
		AllowedTools:    payload.AllowedTools,
		DeniedTools:     payload.DeniedTools,
		AllowedAdapters: payload.AllowedAdapters,
		Enforce:         payload.ToolAllowEnforce,
	}, nil
}

// isLedgerRouteMissing 判断 404 响应体是否是「远端没有这条路由」的形状。
//
// ★ 判据取既有 tool-allow.js 的同款启发式：响应体里出现路由缺失类标记
//
//	（no route / route not found / handler not found …）⇒ 认定是**路由缺失**
//	（执法面未部署）；否则一律算「形状不认得」⇒ 单独计数、单独审计。
func isLedgerRouteMissing(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, marker := range []string{
		"no route", "route not found", "not found: route", "handler not found",
		"no handler", "not_found_route", "route_missing",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// ErrLedgerUnavailable 表示外部账本不可达（调用方据此记审计，不阻断转发）。
var ErrLedgerUnavailable = errors.New("mcp: remote ledger unavailable")
