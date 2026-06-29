package autoroute

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Index is the in-memory cache of the credential_model_index 5-min rolled-up
// snapshot. Refreshed every 5 minutes by bg/auto_index_refresher.go.
//
// The cache is read-mostly: Recommend() is the hot path called on every
// model=auto request. Refresh() is called from the background worker.
//
// Concurrency:
//   - mu protects the entries + lastRefresh fields
//   - recommend snapshots the entries pointer under RLock so concurrent
//     requests don't block on a refresh
type Index struct {
	mu          sync.RWMutex
	entries     []Candidate // flat list of all candidates from last refresh
	byCanonical map[int][]*Candidate
	lastRefresh time.Time

	// hotCanonicalCache caches the 48h hot Top 3 canonicals to reduce
	// request_logs scans. Protected by hotCanonicalMu. TTL: 2 minutes.
	hotCanonicalMu   sync.RWMutex
	hotCanonicals    []int
	hotCanonicalsTS  time.Time
	hotCanonicalsTTL time.Duration

	// correctionLoader allows tests and alternate implementations to
	// override how last-task correction is loaded for RecommendV2.
	correctionLoader func(context.Context, *pgxpool.Pool, string, int, TaskType) (map[string]float64, error)

	// availabilityFilter allows tests and alternate implementations to
	// override the live availability check used by RecommendV2.
	availabilityFilter func(context.Context, *pgxpool.Pool, []Candidate) ([]Candidate, error)

	// Pool is the optional PG pool for on-demand refresh when the cache
	// is stale (older than staleThreshold). nil disables on-demand refresh.
	pool *pgxpool.Pool

	// staleThreshold controls when on-demand refresh kicks in.
	// Default: 10 minutes (2x the refresh interval).
	staleThreshold time.Duration
}

// NewIndex constructs an empty index. Call Refresh once before first use.
func NewIndex() *Index {
	return &Index{
		byCanonical:      make(map[int][]*Candidate),
		staleThreshold:   10 * time.Minute,
		hotCanonicalsTTL: 2 * time.Minute,
	}
}

// SetPool wires the PG pool used by on-demand refresh.
func (idx *Index) SetPool(pool *pgxpool.Pool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.pool = pool
}

// LastRefresh returns the time of the last successful refresh (zero if
// never refreshed). Used by admin API and tests.
func (idx *Index) LastRefresh() time.Time {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.lastRefresh
}

// Snapshot returns a defensive copy of the current candidates.
// Used by admin API to render the current index state.
func (idx *Index) Snapshot() []Candidate {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]Candidate, len(idx.entries))
	copy(out, idx.entries)
	return out
}

// Recommend returns the top-N candidates for the given task type, scored
// against the given profile. Uses a 3-tier funnel (需求 #6):
//
// L1: Hot pool (tier=primary) + popularity sort → top 20
// L2: 8-dim scoring + composite sort → top N (default 3)
// L3: Fallback (tier=secondary/fallback) if L1 < 3 →补充到 3 个
//
// Cohort-level baselines (price P75, speed P95) are computed from the
// L1 hot pool so per-candidate scoring is apples-to-apples.
//
// Filtering:
//   - Only routable candidates (unavailable_reason == "")
//   - L1: only tier=primary, sorted by popularity_score desc, top 20
//   - L2: 8-dim scoring on L1 results
//   - L3: if L2 results < 3, add tier=secondary/fallback to reach 3
//
// If the cohort is empty after filtering, returns nil (caller should
// fall back to the default model).
func (idx *Index) Recommend(task TaskType, sigs ClassificationSignals, profile Profile, topN int) []ScoredCandidate {
	idx.mu.RLock()
	all := idx.entries
	idx.mu.RUnlock()

	if topN <= 0 {
		topN = 3 // 默认返回 top-3
	}

	// L1: 热门池过滤（tier=primary）
	primaryPool := make([]Candidate, 0, len(all))
	secondaryPool := make([]Candidate, 0, len(all))
	fallbackPool := make([]Candidate, 0, len(all))

	for i := range all {
		c := all[i]
		// 跳过不可路由的候选
		if c.UnavailableReason != "" {
			continue
		}

		// 计算 TaskMatchScore（用于后续评分）
		c.TaskMatchScore = TaskMatchScore(task, c.Tags)

		// 按 tier 分池（需求 #6：三级路由）
		switch c.Tier {
		case "primary":
			primaryPool = append(primaryPool, c)
		case "secondary":
			secondaryPool = append(secondaryPool, c)
		case "fallback":
			fallbackPool = append(fallbackPool, c)
		default:
			// 未设置 tier → 默认归入 secondary
			secondaryPool = append(secondaryPool, c)
		}
	}

	// L1: 热门池按 popularity_score 排序，取 top 20
	sort.SliceStable(primaryPool, func(i, j int) bool {
		return primaryPool[i].PopularityScore > primaryPool[j].PopularityScore
	})
	hotPoolSize := 20
	if len(primaryPool) > hotPoolSize {
		primaryPool = primaryPool[:hotPoolSize]
	}

	// 如果热门池为空，降级到 secondary + fallback 全量
	if len(primaryPool) == 0 {
		primaryPool = append(secondaryPool, fallbackPool...)
	}

	if len(primaryPool) == 0 {
		return nil // 无候选可路由
	}

	// L2: 8 维评分（在热门池上计算）
	costCtx := computeCostContext(primaryPool)
	scored := make([]ScoredCandidate, 0, len(primaryPool))
	for _, c := range primaryPool {
		bd := Score(c, sigs, task, profile, costCtx)
		scored = append(scored, ScoredCandidate{Candidate: c, Breakdown: bd})
	}

	// 按 composite 降序排序
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
	})

	// 取 top-N
	if len(scored) > topN {
		scored = scored[:topN]
	}

	// L3: 兜底机制 - 如果 L2 结果 < 3，从 secondary/fallback 补充
	if len(scored) < 3 {
		fallbackCands := append(secondaryPool, fallbackPool...)
		// 过滤掉已在 scored 中的候选（避免重复）
		existingIDs := make(map[int64]bool)
		for _, sc := range scored {
			existingIDs[sc.Candidate.CredentialID] = true
		}

		fallbackFiltered := make([]Candidate, 0, len(fallbackCands))
		for _, c := range fallbackCands {
			if !existingIDs[c.CredentialID] {
				c.TaskMatchScore = TaskMatchScore(task, c.Tags)
				fallbackFiltered = append(fallbackFiltered, c)
			}
		}

		// 评分 fallback 候选
		if len(fallbackFiltered) > 0 {
			fbCostCtx := computeCostContext(fallbackFiltered)
			for _, c := range fallbackFiltered {
				bd := Score(c, sigs, task, profile, fbCostCtx)
				scored = append(scored, ScoredCandidate{Candidate: c, Breakdown: bd})
			}
			// 重新排序（包含 fallback）
			sort.SliceStable(scored, func(i, j int) bool {
				return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
			})
			// 保留 top-3
			if len(scored) > 3 {
				scored = scored[:3]
			}
		}
	}

	return scored
}

// computeCostContext derives the cohort baselines used for normalisation.
//
// PriceP75: 75th-percentile blended price (1:1 input:output assumption)
//   - We use a hand-rolled percentile since the cohort is tiny (<= 50)
//   - Zero-cost candidates are excluded from the percentile so free
//     models don't drag the baseline to 0.
//
// SpeedP95: maximum P95 latency across the cohort (worst case)
//   - Used as the "ceiling" so faster candidates score higher.
func computeCostContext(cands []Candidate) CostContext {
	prices := make([]float64, 0, len(cands))
	speeds := make([]int, 0, len(cands))
	for _, c := range cands {
		blended := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
		if blended > 0 {
			prices = append(prices, blended)
		}
		if c.P95LatencyMs > 0 {
			speeds = append(speeds, c.P95LatencyMs)
		}
	}
	ctx := CostContext{}
	if len(prices) > 0 {
		sort.Float64s(prices)
		ctx.PriceP75 = percentile(prices, 0.75)
	}
	if len(speeds) > 0 {
		sort.Ints(speeds)
		ctx.SpeedP95 = float64(speeds[len(speeds)-1]) // max
	}
	return ctx
}

// percentile returns the p-th percentile (0-1) of a sorted slice.
// Uses nearest-rank method with ceil (rounds up so p=0.5 of 10 items
// returns the 5th item, which is the standard interpretation).
// Caller must pre-sort.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// ceil(n * p)
	n := float64(len(sorted))
	rank := int(n*p + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Refresh reloads the entire credential_model_index snapshot from the
// most recent 5-min bucket. Called by bg/auto_index_refresher.go every
// 5 minutes and by SetPool on first start.
//
// Failure handling: returns the error but leaves the old snapshot in
// place (don't fail-closed on a transient DB blip).
//
// Implementation note: uses a single SQL query joining credentials,
// models_canonical, and credential_model_index. Returns a flat list
// of Candidate structs. Tags are parsed from JSONB.
func (idx *Index) Refresh(ctx context.Context) error {
	if idx.pool == nil {
		return fmt.Errorf("autoroute index: PG pool not set")
	}
	rows, err := idx.pool.Query(ctx, refreshIndexSQL)
	if err != nil {
		return fmt.Errorf("query credential_model_index: %w", err)
	}
	defer rows.Close()

	out := make([]Candidate, 0, 64)
	for rows.Next() {
		c, scanErr := scanIndexRow(rows)
		if scanErr != nil {
			// Skip the bad row but keep going — partial refresh
			// is better than no refresh.
			continue
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		return fmt.Errorf("iterate credential_model_index: %w", rows.Err())
	}

	// Build byCanonical lookup
	byCanon := make(map[int][]*Candidate, 16)
	for i := range out {
		c := &out[i]
		byCanon[c.CanonicalID] = append(byCanon[c.CanonicalID], c)
	}

	idx.mu.Lock()
	idx.entries = out
	idx.byCanonical = byCanon
	idx.lastRefresh = time.Now()
	idx.mu.Unlock()
	return nil
}

// refreshIndexSQL is the single query that materialises the index snapshot.
//
// Source tables:
//   - credential_model_index : latest 5-min bucket
//   - models_canonical       : canonical model attributes (tags, context_window)
//   - credentials            : provider_id, label, lifecycle
//
// Filter:
//   - Only the most recent bucket per (credential_id, raw_model)
//   - Skip disabled credentials / suspended lifecycle
//   - Skip zero-context models (likely misconfigured)
//
// TODO(billing_currency): convert CNY to USD before scoring when the
// cohort contains both currencies. See HasMixedPrices for the placeholder.
const refreshIndexSQL = `
WITH latest_bucket AS (
    SELECT credential_id, raw_model, MAX(bucket) AS bucket
    FROM credential_model_index
    GROUP BY credential_id, raw_model
)
SELECT
    cmi.credential_id,
    cmi.raw_model,
    cmi.canonical_id,
    mc.canonical_name,
    mc.tags,
    mc.context_window,
    cmi.billing_mode,
    cmi.unit_price_in_per_1m,
    cmi.unit_price_out_per_1m,
    cmi.success_rate,
    cmi.p95_latency_ms,
    cmi.active_sessions,
    cmi.concurrency_limit,
    cmb.routing_tier,
    CASE
      WHEN cmb.available IS NOT TRUE THEN COALESCE(cmb.unavailable_reason, 'binding_unavailable')
      WHEN pm.available IS NOT TRUE THEN COALESCE(pm.unavailable_reason, 'provider_model_unavailable')
      ELSE COALESCE(cmb.unavailable_reason, pm.unavailable_reason, '')
    END AS unavailable_reason,
    -- CHANNEL_QUALITY_ROUTING: 通道质量路由所需字段
    --
    -- 加载 providers.category / providers.kind，用于驱动
    -- scoreChannelQuality。JOIN 路径：credentials → providers。
    -- cr.provider_id 必存在（NOT NULL FK），所以是 INNER JOIN；但
    -- 保留 LEFT 是为了兼容历史脏数据。
    COALESCE(p.category, '')  AS provider_category,
    COALESCE(p.kind, 'cloud') AS provider_kind,
    -- IsFree 派生：满足以下任一即视为免费（与 deriveIsFree() 一致）
    --   - billing_mode 在 free 类
    --   - cost_tier='free'（来自 models_canonical）
    --   - 价格为 0（in + out 都为 0）
    CASE
      WHEN LOWER(COALESCE(cmi.billing_mode, '')) IN ('free','token_plan','code_plan','agent_plan','monthly')
        OR LOWER(COALESCE(mc.cost_tier, '')) = 'free'
        OR (COALESCE(cmi.unit_price_in_per_1m, 0) + COALESCE(cmi.unit_price_out_per_1m, 0)) = 0
      THEN TRUE ELSE FALSE
    END AS is_free,
    -- CHANNEL_QUALITY_ROUTING: models_canonical.cost_tier 也单独加载
    -- 让 deriveIsFree() 的 CostTier 分支在 Go 侧能正确生效。
    mc.cost_tier AS cost_tier
FROM credential_model_index cmi
JOIN latest_bucket lb
  ON lb.credential_id = cmi.credential_id
 AND lb.raw_model     = cmi.raw_model
 AND lb.bucket        = cmi.bucket
JOIN credentials cr ON cr.id = cmi.credential_id
LEFT JOIN providers p ON p.id = cr.provider_id
LEFT JOIN provider_models pm
  ON pm.provider_id = cr.provider_id
 AND pm.raw_model_name = cmi.raw_model
LEFT JOIN credential_model_bindings cmb
  ON cmb.credential_id = cmi.credential_id
 AND cmb.provider_model_id = pm.id
LEFT JOIN models_canonical mc ON mc.id = cmi.canonical_id
WHERE COALESCE(cr.lifecycle_status, 'active') != 'suspended'
  AND COALESCE(cr.status, 'active') NOT IN ('disabled')
ORDER BY cmi.canonical_id, cmi.score_smart DESC
`

// scanIndexRow decodes one row from the refresh query into a Candidate.
// Uses string-typed SQL scanning then parses tags and computes
// pressure_ratio from active_sessions / concurrency_limit.
//
// Tags handling: models_canonical.tags is a TEXT[] (PostgreSQL array,
// OID 1009). pgx returns it as []string when the destination is []string
// (or pgtype array). We use a []string destination and skip the
// JSONB parser — TEXT[] is not JSONB.
func scanIndexRow(rows interface {
	Scan(dest ...any) error
}) (Candidate, error) {
	var c Candidate
	var tags []string
	var ctxWindow *int
	// v2.0.4 fix: unit_price / success_rate / canonical_id / canonical_name /
	// billing_mode can be NULL in credential_model_index when the source
	// rows have no matching model_offers entry. Use pointers to allow NULL.
	var canonicalID *int
	var canonicalName *string
	var billingMode *string
	var priceIn, priceOut, successRate *float64
	var routingTier *int16
	var unavailableReason *string
	// CHANNEL_QUALITY_ROUTING: 新增字段用 *string/*bool 以允许 NULL
	// （早期迁移期间 providers 关联缺失时不会让整个 refresh 失败）。
	var providerCategory *string
	var providerKind *string
	var isFree *bool
	var costTier *string
	if err := rows.Scan(
		&c.CredentialID, &c.RawModel, &canonicalID,
		&canonicalName, &tags, &ctxWindow,
		&billingMode,
		&priceIn, &priceOut,
		&successRate, &c.P95LatencyMs,
		&c.ActiveSessions, &c.ConcurrencyLimit,
		&routingTier, &unavailableReason,
		&providerCategory, &providerKind, &isFree,
		&costTier,
	); err != nil {
		return c, err
	}
	if ctxWindow != nil {
		c.ContextWindow = *ctxWindow
	}
	if canonicalID != nil {
		c.CanonicalID = *canonicalID
	}
	if canonicalName != nil {
		c.CanonicalName = *canonicalName
	}
	if billingMode != nil {
		c.BillingMode = *billingMode
	}
	if priceIn != nil {
		c.UnitPriceInPer1M = *priceIn
	}
	if priceOut != nil {
		c.UnitPriceOutPer1M = *priceOut
	}
	if successRate != nil {
		c.SuccessRate = *successRate
	}
	if routingTier != nil {
		switch {
		case *routingTier <= 1:
			c.Tier = "primary"
		case *routingTier <= 3:
			c.Tier = "secondary"
		default:
			c.Tier = "fallback"
		}
		c.PopularityScore = 100 - float64(*routingTier)
		if c.PopularityScore < 0 {
			c.PopularityScore = 0
		}
		if c.PopularityScore > 100 {
			c.PopularityScore = 100
		}
		if c.Tier == "primary" {
			c.PopularityScore += c.SuccessRate * 10
		}
		if c.PopularityScore > 100 {
			c.PopularityScore = 100
		}
		if c.PopularityScore < 0 {
			c.PopularityScore = 0
		}
		if c.PopularityScore > 100 {
			c.PopularityScore = 100
		}
	}
	if unavailableReason != nil {
		c.UnavailableReason = strings.TrimSpace(*unavailableReason)
	}
	// CHANNEL_QUALITY_ROUTING: 加载 provider category / kind / is_free
	//
	// 三者均为可空：providers LEFT JOIN 在 cr.provider_id 异常时会
	// 返回 NULL row；此时回退到中性的"unknown"语义，scoreChannelQuality
	// 会给出 base=40，避免拉黑候选。
	if providerCategory != nil {
		c.ProviderCategory = strings.TrimSpace(*providerCategory)
	}
	if providerKind != nil {
		c.ProviderKind = strings.TrimSpace(*providerKind)
	}
	if isFree != nil {
		c.IsFree = *isFree
	}
	if costTier != nil {
		c.CostTier = strings.TrimSpace(*costTier)
	}
	c.Tags = tags
	// PressureRatio: 0 when concurrency_limit is 0 (unknown → no penalty)
	if c.ConcurrencyLimit > 0 {
		c.PressureRatio = float64(c.ActiveSessions) / float64(c.ConcurrencyLimit)
	}
	return c, nil
}

// parseTagsJSONB parses a PostgreSQL JSONB array literal of strings
// into a []string. Returns nil on parse error or empty input.
//
// Example input: `["reasoning","code","agent"]`
// Example input: `null` → returns nil
//
// Uses encoding/json (canonical, robust to escape sequences).
func parseTagsJSONB(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
