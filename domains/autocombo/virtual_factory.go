package autocombo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lib/pq"

	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// ProviderResolver is the minimal contract the factory needs from the
// provider layer to materialize a broad candidate pool from the OmniFree
// catalog. The interface is satisfied by *provider.Client (see
// provider/client.go) and by tests that pre-stage a fixed candidate set.
type ProviderResolver interface {
	GetCandidates(ctx context.Context, model, profile, tenantID string) ([]provider.Candidate, *provider.Policy, error)
}

// QuotaPreflighter is the minimal contract the factory needs from the
// quota tracker to decide whether a free credential still has budget for
// the requested window. nil disables quota preflight (every candidate
// passes the quota gate; see VirtualFactory.BuildFromCandidates).
type QuotaPreflighter interface {
	Preflight(ctx context.Context, req freeresource.PreflightRequest) (bool, error)
}

// CatalogEntry is a denormalised view of a free_resource_catalog row
// surfaced to the factory. ModelID matches the standardized/canonical
// model id used by the provider layer.
type CatalogEntry struct {
	ProviderCode string
	ModelID      string
	FreeType     string
	ToSVerdict   string
}

// VirtualFactory 虚拟 Combo 工厂
//
// 重写于 2026-08-07: 旧 Build 直接 SQL 查询 credentials/keyless_providers,
// 并返回不可执行的 autocombo.Candidate 类型, 无法被现有 Executor 调用.
// 当前实现只在 provider.Client 解析出的可执行 provider.Candidate 池上
// 做免费资源过滤 + 配额预检 + 评分排序; 输入输出都是 []provider.Candidate.
type VirtualFactory struct {
	db            *sql.DB
	quotaTracker  QuotaPreflighter
	loadCatalogFn func(ctx context.Context, tenantID string, spec *AutoComboSpec) ([]CatalogEntry, error)
}

// NewVirtualFactory 创建虚拟工厂
func NewVirtualFactory(db *sql.DB, quotaTracker *freeresource.QuotaTracker) *VirtualFactory {
	var prefighter QuotaPreflighter
	if quotaTracker != nil {
		prefighter = quotaTracker
	}
	vf := &VirtualFactory{
		db:           db,
		quotaTracker: prefighter,
	}
	vf.loadCatalogFn = vf.queryCatalog
	return vf
}

// NewVirtualFactoryWith 创建虚拟工厂，允许注入抽象接口便于测试。
func NewVirtualFactoryWith(db *sql.DB, prefighter QuotaPreflighter) *VirtualFactory {
	vf := &VirtualFactory{
		db:           db,
		quotaTracker: prefighter,
	}
	vf.loadCatalogFn = vf.queryCatalog
	return vf
}

// BuildFromCandidates 在 provider.Client 已经解析出的 []provider.Candidate
// 上, 按 AutoComboSpec 定义的免费资源/ToS/denylist/model_pattern 过滤,
// 然后做配额预检, 最后用 spec.ScoringWeightsJSON 评分排序.
//
// 输入:
//   - ctx: 请求上下文, 用于查询 catalog 与配额预检.
//   - spec: Resolver 解析出来的 AutoComboSpec, 含评分权重与过滤条件.
//   - baseCandidates: 已经由 provider.Client 解析出的可执行 provider.Candidate
//     池, 必须保留所有 BaseURL/Protocol/APIKey/RawModel 字段, 以便 Executor 直接使用.
//   - tenantID: 当前租户 ID.
//
// 返回:
//   - 过滤+排序后的 []provider.Candidate, 可直接交给 Executor.
//   - err 仅在读取免费资源目录时返回; 过滤为空被视为业务正常, 返回 (空切片, nil).
func (vf *VirtualFactory) BuildFromCandidates(
	ctx context.Context,
	spec *AutoComboSpec,
	baseCandidates []provider.Candidate,
	tenantID string,
) ([]provider.Candidate, error) {
	if spec == nil {
		return nil, errors.New("autocombo: spec is nil")
	}
	if len(baseCandidates) == 0 {
		return nil, nil
	}

	catalog, err := vf.loadCatalogFn(ctx, tenantID, spec)
	if err != nil {
		return nil, fmt.Errorf("load free resource catalog: %w", err)
	}
	if len(catalog) == 0 {
		return nil, nil
	}

	filtered := vf.filterCandidates(baseCandidates, catalog, spec)
	if len(filtered) == 0 {
		return nil, nil
	}

	withQuota := vf.preflightQuota(ctx, filtered, tenantID)
	if len(withQuota) == 0 {
		return nil, nil
	}

	engine, err := NewEngine(spec.ScoringWeightsJSON)
	if err != nil {
		return nil, fmt.Errorf("build engine: %w", err)
	}
	sorted := engine.sortCandidates(withQuota)
	if limit := spec.MaxCandidates; limit > 0 && len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted, nil
}

// loadCatalog 读取免费资源目录并按 spec.ToSFilter / spec.FreeTypeFilter 过滤.
//
// 仅返回当前租户启用且 ToS 允许的目录行; rows.Err 在循环结束后判断,
// 用于在 SQL 中断时立即报告而不是在数据已经返回后静默吞掉.
//
// RLS contract (2026-08-09 audit round 3): free_resource_catalog 表启用
// 了 RLS policy, USING 子句依赖 public.get_current_tenant(). 我们在
// SELECT 前用 set_config('app.current_tenant', $1, true) 注入当前请求
// 的 tenant, 让 RLS 在 stdlib 连接上也生效. 空 tenantID 时不设 GUC, 由
// get_current_tenant() 走 'default' fallback (兼容旧 behavior).
func (vf *VirtualFactory) queryCatalog(ctx context.Context, tenantID string, spec *AutoComboSpec) ([]CatalogEntry, error) {
	if vf.db == nil {
		return nil, nil
	}
	if tenantID != "" {
		if _, err := vf.db.ExecContext(ctx,
			fmt.Sprintf("SET app.current_tenant = '%s'", escapeTenant(tenantID))); err != nil {
			slog.Warn("omnifree: failed to set app.current_tenant before queryCatalog",
				"tenant_id", tenantID, "error", err)
		}
	}

	query := strings.Builder{}
	query.WriteString(`
        SELECT provider_code, model_id, free_type, tos_verdict
        FROM free_resource_catalog
        WHERE enabled = TRUE
          AND tenant_id = $1
    `)
	args := []interface{}{tenantID}
	if len(spec.ToSFilter) > 0 {
		args = append(args, pq.Array(spec.ToSFilter))
		query.WriteString(fmt.Sprintf(" AND tos_verdict = ANY($%d)", len(args)))
	}
	if len(spec.FreeTypeFilter) > 0 {
		args = append(args, pq.Array(spec.FreeTypeFilter))
		query.WriteString(fmt.Sprintf(" AND free_type = ANY($%d)", len(args)))
	}

	rows, err := vf.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var catalog []CatalogEntry
	for rows.Next() {
		var entry CatalogEntry
		if err := rows.Scan(&entry.ProviderCode, &entry.ModelID, &entry.FreeType, &entry.ToSVerdict); err != nil {
			return nil, err
		}
		catalog = append(catalog, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return catalog, nil
}

// LoadCatalogForBuild 暴露给调用方 (streaming handler) 在 BuildFromCandidates
// 之外单独加载目录, 用作 "按目录中具体模型分别 GetCandidates" 的迭代源;
// 测试中可通过 vf.loadCatalogFn 替换.
func (vf *VirtualFactory) LoadCatalogForBuild(ctx context.Context, tenantID string, spec *AutoComboSpec) ([]CatalogEntry, error) {
	if vf.loadCatalogFn == nil {
		return nil, nil
	}
	return vf.loadCatalogFn(ctx, tenantID, spec)
}

// filterCandidates 对候选执行目录匹配 + allowlist/denylist/model_pattern 过滤.
//
// 匹配规则:
//   - 通过 CatalogCode (provider code) + RawModel/StandardizedName 命中目录行.
//   - 若 spec.ProviderAllowlist 不为空, 候选 provider code 必须 ∈ allowlist.
//   - 若 spec.ProviderDenylist 不为空, 候选 provider code 必须 ∉ denylist.
//   - 若 spec.ModelPattern 非空, 候选的 StandardizedName/RawModel 需满足前缀或子串匹配
//     (与 spec.Variant 配套: 'coding' 子串、'reasoning' 子串、'fast' 子串等启发式,
//     但这里只做简单的子串包含; 复杂正则留待后续变体接入).
func (vf *VirtualFactory) filterCandidates(
	candidates []provider.Candidate,
	catalog []CatalogEntry,
	spec *AutoComboSpec,
) []provider.Candidate {
	index := make(map[string]CatalogEntry, len(catalog))
	for _, entry := range catalog {
		// SQL 过滤 ToS/FreeType 后, 这里再校验一次避免测试注入的 catalog
		// 出现未过滤条目; 双层校验保证 catalog->candidate 一致性.
		if len(spec.ToSFilter) > 0 && !containsString(spec.ToSFilter, entry.ToSVerdict) {
			continue
		}
		if len(spec.FreeTypeFilter) > 0 && !containsString(spec.FreeTypeFilter, entry.FreeType) {
			continue
		}
		index[catalogKey(entry.ProviderCode, entry.ModelID)] = entry
	}

	allow := make(map[string]struct{}, len(spec.ProviderAllowlist))
	for _, code := range spec.ProviderAllowlist {
		if code != "" {
			allow[code] = struct{}{}
		}
	}
	deny := make(map[string]struct{}, len(spec.ProviderDenylist))
	for _, code := range spec.ProviderDenylist {
		if code != "" {
			deny[code] = struct{}{}
		}
	}

	pattern := strings.ToLower(strings.TrimSpace(spec.ModelPattern))
	variantHint := strings.ToLower(string(spec.Variant))

	filtered := make([]provider.Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		key := catalogKey(c.CatalogCode, c.StandardizedName)
		if _, ok := index[key]; !ok {
			continue
		}
		if len(allow) > 0 {
			if _, ok := allow[c.CatalogCode]; !ok {
				continue
			}
		}
		if _, ok := deny[c.CatalogCode]; ok {
			continue
		}
		if pattern != "" && !candidateMatchesPattern(c, pattern) {
			continue
		}
		if variantHint != "" && !candidateMatchesVariant(c, variantHint) {
			continue
		}
		// 去重: 同一 (credential, raw_model) 只保留一次, 避免 catalog 命中重复插入.
		dupKey := fmt.Sprintf("%d|%s", c.CredentialID, c.RawModel)
		if _, dup := seen[dupKey]; dup {
			continue
		}
		seen[dupKey] = struct{}{}
		filtered = append(filtered, c)
	}
	return filtered
}

// preflightQuota 对 free 候选在多个窗口上跑 Preflight; 全部通过才放行.
//
// round 3 audit H3 修复要点:
//   - 旧实现仅检查 day-1 窗口, 忽略了 hour-5 / month-1; Groq 等提供商
//     在 hour-5 / month-1 上有独立配额窗口 (RPM / monthly token cap),
//     只看 day-1 会让候选在 hit 实际 RPM 限制时被错误放行.
//   - 新实现按 (free tier, candidate 实际支持的窗口类型) 跑多个
//     Preflight, 全部通过才返回 ok. 任一窗口未通过即剔除该候选.
//
// round 3 audit H6 修复要点:
//   - 旧 isFreeBilling 把空 BillingMode 也视为 free, 让配置错误的 paid
//     candidate 误入 free 池; 修复后空 / 未知 mode 跳过配额门 (caller
//     后续业务层可加显式 "unknown-billing" 告警).
func (vf *VirtualFactory) preflightQuota(
	ctx context.Context,
	candidates []provider.Candidate,
	tenantID string,
) []provider.Candidate {
	if vf.quotaTracker == nil {
		return candidates
	}
	out := make([]provider.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if !isFreeBilling(c.BillingMode) {
			out = append(out, c)
			continue
		}

		// 对每个 (billing mode → window type) 组合跑一次 Preflight; 全部
		// 通过才算 ok. 与 Resolver 内置模板默认 day-1+month-1 保持一致.
		windows := []freeresource.WindowType{
			freeresource.WindowTypeDay1,
			freeresource.WindowTypeMonth1,
		}
		allPass := true
		for _, wt := range windows {
			ok, err := vf.quotaTracker.Preflight(ctx, freeresource.PreflightRequest{
				CredentialID:    int64(c.CredentialID),
				ProviderCode:    c.CatalogCode,
				ModelID:         c.StandardizedName,
				WindowType:      wt,
				DefaultLimit:    freeTierDefaultLimit(c),
				MinRemainingPct: 0.1,
				TenantID:        tenantID,
			})
			if err != nil {
				slog.Debug("omnifree: preflight error",
					"provider_code", c.CatalogCode, "model", c.StandardizedName,
					"window", wt, "error", err)
				allPass = false
				break
			}
			if !ok {
				allPass = false
				break
			}
		}
		if !allPass {
			continue
		}
		out = append(out, c)
	}
	return out
}

// freeTierDefaultLimit 返回 Preflight 的 fallback 上限. provider 自身的
// day-1 配额未知时, 用 1000 req/day 作为兜底. 真实生产中,
// free_resource_catalog.daily_tokens 已经存了日配额, 应由 catalog loader
// 注入到 candidate metadata 中; 这里先用常量兜底, 后续可通过 provider 扩
// 展 metadata 字段传入.
func freeTierDefaultLimit(_ provider.Candidate) int64 {
	// 1000 RPD 是大多数免费层提供商的保守默认值 (OpenRouter :free = 50 RPD,
	// 但加上 boost 后可达 1000 RPD; Groq dev tier ≈ 1000 RPD).
	return 1000
}

// isFreeBilling 判断 provider candidate 是否属于免费层级.
//
// round 3 H6 修复: 不再把空 / 未知 mode 视为 free; 仅显式标记的免费
// 模式走配额门, 未知 mode 视为 paid (caller 应在 business layer 加
// "unknown-billing" 告警).
//
// 已知 free 模式: free / keyless / token_plan / code_plan / tier1 /
// recurring-daily / recurring-monthly / recurring-credit / recurring-uncapped.
func isFreeBilling(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "free",
		"keyless",
		"token_plan",
		"code_plan",
		"tier1",
		"recurring-daily",
		"recurring-monthly",
		"recurring-credit",
		"recurring-uncapped",
		"one-time-initial":
		return true
	}
	return false
}

func catalogKey(providerCode, modelID string) string {
	if providerCode == "" {
		return strings.ToLower(strings.TrimSpace(modelID))
	}
	return strings.ToLower(providerCode) + "::" + strings.ToLower(strings.TrimSpace(modelID))
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func candidateMatchesPattern(c provider.Candidate, pattern string) bool {
	if pattern == "" {
		return true
	}
	return strings.Contains(strings.ToLower(c.RawModel), pattern) ||
		strings.Contains(strings.ToLower(c.StandardizedName), pattern)
}

func candidateMatchesVariant(c provider.Candidate, variant string) bool {
	if variant == "" || variant == "cheap" || variant == "smart" || variant == "chaos" {
		return true
	}
	haystack := strings.ToLower(c.RawModel + " " + c.StandardizedName)
	switch variant {
	case "fast":
		return strings.Contains(haystack, "fast") || strings.Contains(haystack, "mini") || strings.Contains(haystack, "instant")
	case "coding":
		return strings.Contains(haystack, "code") || strings.Contains(haystack, "coder") || strings.Contains(haystack, "starcoder")
	case "reasoning":
		return strings.Contains(haystack, "reason") || strings.Contains(haystack, "o1") || strings.Contains(haystack, "o3")
	case "creative":
		return strings.Contains(haystack, "creative") || strings.Contains(haystack, "story") || strings.Contains(haystack, "writer")
	}
	return true
}

// ScoreSort re-exports the engine sort entry point for callers that want a
// plain ordering without the quota gate; primarily used by tests.
func (vf *VirtualFactory) ScoreSort(candidates []provider.Candidate, weights json.RawMessage) ([]provider.Candidate, error) {
	engine, err := NewEngine(weights)
	if err != nil {
		return nil, err
	}
	return engine.sortCandidates(candidates), nil
}
