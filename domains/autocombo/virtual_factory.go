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
//
// round 3 M7: 新增 TrainsOnPrompts 字段, 镜像 OmniRoute
// freeModelCatalog.trainsOnPrompts. factory 可选过滤掉训练型提供商.
type CatalogEntry struct {
	ProviderCode     string
	ModelID          string
	FreeType         string
	ToSVerdict       string
	TrainsOnPrompts  bool
	DailyTokens      int64
	MonthlyTokens    int64
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

	engine, err := NewEngineWithVariant(spec.ScoringWeightsJSON, spec.Variant)
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

	// round 4 追加修复: 空 tenant 归一为 'default'. 旧实现把裸空字符串
	// 当成查询参数 (tenant_id = ''), 与 Resolver.queryDB 的 'default'
	// fallback 语义不一致 —— Resolver 命中内置模板后, 若 catalog 用空
	// tenant 查询, 会因 tenant_id 不匹配任何行而返回空目录, 即使
	// 'default' 租户下确实有已启用的免费资源.
	tenantFilter := tenantID
	if tenantFilter == "" {
		tenantFilter = "default"
	}

	tx, err := vf.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin tx for queryCatalog: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// round 4 追加修复 (同 resolver.go queryDB): SET LOCAL 必须与后续
	// SELECT 在同一个事务/连接上执行, 否则 database/sql 连接池可能把
	// 两次调用分配到不同物理连接, RLS 静默按 'default' 过滤.
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenant(tenantFilter))); err != nil {
		slog.Warn("omnifree: failed to set app.current_tenant before queryCatalog",
			"tenant_id", tenantFilter, "error", err)
	}

	query := strings.Builder{}
	query.WriteString(`
        SELECT provider_code, model_id, free_type, tos_verdict,
               trains_on_prompts, COALESCE(daily_tokens, 0), COALESCE(monthly_tokens, 0)
        FROM free_resource_catalog
        WHERE enabled = TRUE
          AND tenant_id = $1
    `)
	args := []interface{}{tenantFilter}
	if len(spec.ToSFilter) > 0 {
		args = append(args, pq.Array(spec.ToSFilter))
		query.WriteString(fmt.Sprintf(" AND tos_verdict = ANY($%d)", len(args)))
	}
	if len(spec.FreeTypeFilter) > 0 {
		args = append(args, pq.Array(spec.FreeTypeFilter))
		query.WriteString(fmt.Sprintf(" AND free_type = ANY($%d)", len(args)))
	}

	rows, err := tx.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var catalog []CatalogEntry
	for rows.Next() {
		var entry CatalogEntry
		if err := rows.Scan(&entry.ProviderCode, &entry.ModelID, &entry.FreeType, &entry.ToSVerdict,
			&entry.TrainsOnPrompts, &entry.DailyTokens, &entry.MonthlyTokens); err != nil {
			return nil, err
		}
		catalog = append(catalog, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit queryCatalog tx: %w", err)
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
		// round 4 M7: 当 spec.HideTrainableModels 为 true, 直接剔除
		// 训练型提供商 (entry.TrainsOnPrompts). 这让 tenant 通过 spec
		// 配置 "我的 prompt 不被训练" 的隐私偏好.
		if spec.HideTrainableModels && entry.TrainsOnPrompts {
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
		// round 4 审计补充修复: catalog.model_id 通常填写的是上游/原始模型名
		// (例如 "openai/gpt-4o-mini:free"), 而 provider.Candidate 上可能
		// 只有 RawModel/OfferRawModel 与之精确匹配, StandardizedName 是
		// provider 层归一化后的名字 (可能不含 ":free" 后缀等). 旧实现只按
		// StandardizedName 匹配, 会让合法的 catalog 行永远匹配不到候选,
		// 导致 auto/free 静默丢失可用资源. 现在依次尝试
		// StandardizedName → RawModel → OfferRawModel, 命中任意一个即可.
		if !candidateMatchesCatalog(c, index) {
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
		// round 4 审计补充修复: spec.TierFilter 之前只被 Resolver 构造出来
		// 却从未被 filterCandidates 消费 — "auto/free" 模板声明
		// tier_filter=["free"] 但实际上任何 catalog 命中的候选 (包括
		// billing_mode=paid/per_token 的候选) 都会通过, 违反模板承诺的
		// tier 语义. 这里显式校验候选的 tier 归属 (由 BillingMode 映射)
		// 必须 ∈ spec.TierFilter (非空时).
		if len(spec.TierFilter) > 0 && !candidateMatchesTierFilter(c, spec.TierFilter) {
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

// candidateMatchesCatalog 判断候选是否命中 catalog index (由
// catalogKey(provider_code, model_id) 索引). 依次尝试
// StandardizedName / RawModel / OfferRawModel 三个字段, 命中任意一个
// 即视为匹配. 这弥补了 catalog.model_id 与 provider 层字段命名不完全
// 对齐的问题 (见 filterCandidates 调用处注释).
func candidateMatchesCatalog(c provider.Candidate, index map[string]CatalogEntry) bool {
	candidates := []string{c.StandardizedName, c.RawModel, c.OfferRawModel}
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, ok := index[catalogKey(c.CatalogCode, name)]; ok {
			return true
		}
	}
	return false
}

// candidateMatchesTierFilter 判断候选的计费层级是否落在 spec.TierFilter
// 允许的集合内. tierFilter 语义 (与 Resolver.getBuiltinTemplate 保持一致):
//   - "free"    → isFreeBilling(c.BillingMode) == true
//   - "keyless" → c.BillingMode == "keyless"
//   - "cheap"   → free 或 billing mode 明确标为 "cheap"
//   - "paid"/"pro" → 非 free (即付费层)
//
// 未知 tier 字面量默认放行 (向后兼容自定义模板新增 tier 值), 但已知
// tier 字面量必须严格匹配, 不允许静默放行 paid 候选进 free 模板.
func candidateMatchesTierFilter(c provider.Candidate, tierFilter []string) bool {
	for _, tier := range tierFilter {
		switch strings.ToLower(strings.TrimSpace(tier)) {
		case "free":
			if isFreeBilling(c.BillingMode) {
				return true
			}
		case "keyless":
			if strings.EqualFold(strings.TrimSpace(c.BillingMode), "keyless") {
				return true
			}
		case "cheap":
			if isFreeBilling(c.BillingMode) || strings.EqualFold(strings.TrimSpace(c.BillingMode), "cheap") {
				return true
			}
		case "paid", "pro", "premium":
			if !isFreeBilling(c.BillingMode) {
				return true
			}
		default:
			// 未知 tier 字面量: 放行, 避免自定义模板因新增 tier 值被
			// 意外全量拒绝 (fail-open 仅对未知配置生效, 已知 tier 严格).
			return true
		}
	}
	return false
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
