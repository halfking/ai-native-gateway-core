package autoroute

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RecommendV2 is the new candidate recommendation path. It enforces
// live availability, seeds from the hottest models in the last 48 hours,
// and applies the simplified score.
func (idx *Index) RecommendV2(
	ctx context.Context,
	task TaskType,
	sigs ClassificationSignals,
	sessionID string,
	topN int,
) []ScoredCandidate {
	flags := GetFeatureFlags()

	idx.mu.RLock()
	all := idx.entries
	pool := idx.pool
	availabilityFilter := idx.availabilityFilter
	correctionLoader := idx.correctionLoader
	idx.mu.RUnlock()

	if topN <= 0 {
		topN = 3
	}

	// Step 1: hard filter - keep only currently available candidates.
	filtered, err := idx.filterCurrentlyAvailable(ctx, pool, availabilityFilter, all)
	if err != nil {
		filtered = fallbackSnapshotAvailability(all)
	}

	available := make([]Candidate, 0, len(filtered))
	for i := range filtered {
		c := filtered[i]
		c.TaskMatchScore = TaskMatchScore(task, c.Tags)
		available = append(available, c)
	}

	if len(available) == 0 {
		fallback := idx.get48hFallback(ctx)
		if fallback != nil {
			return []ScoredCandidate{{
				Candidate: *fallback,
				Breakdown: ScoringBreakdown{
					Composite:  50,
					MatchScore: 30,
					PriceScore: 50,
				},
			}}
		}
		return nil
	}

	byCanonical := make(map[int][]Candidate)
	for _, c := range available {
		byCanonical[c.CanonicalID] = append(byCanonical[c.CanonicalID], c)
	}

	hotTop3 := idx.getHotTop3Canonicals(ctx)

	candidatePool := []Candidate{}
	hotCanonIDs := make(map[int]bool)
	for _, canonID := range hotTop3 {
		if cands, ok := byCanonical[canonID]; ok {
			candidatePool = append(candidatePool, cands...)
			hotCanonIDs[canonID] = true
		}
	}

	if len(hotTop3) < 3 {
		for canonID, cands := range byCanonical {
			if !hotCanonIDs[canonID] {
				candidatePool = append(candidatePool, cands...)
			}
		}
	}

	if len(candidatePool) == 0 {
		fallback := idx.get48hFallback(ctx)
		if fallback != nil {
			return []ScoredCandidate{{
				Candidate: *fallback,
				Breakdown: ScoringBreakdown{
					Composite:  50,
					MatchScore: 30,
					PriceScore: 50,
				},
			}}
		}
		return nil
	}

	avgPriceByCanonical := ComputeAvgPriceByCanonical(candidatePool)

	correctionScoreByModel, err := idx.loadCorrectionScores(ctx, pool, correctionLoader, sessionID, task)
	if err != nil {
		correctionScoreByModel = map[string]float64{}
	}

	scored := make([]ScoredCandidate, 0, len(candidatePool))
	for _, c := range candidatePool {
		correction := correctionScoreByModel[c.CanonicalName]
		var bd ScoringBreakdown
		switch {
		case flags.UseChannelQualityRouting:
			// CHANNEL_QUALITY_ROUTING: 4 维评分
			//   intent 0.4 + price 0.2 + channel 0.3 + reliability 0.1 + correction
			bd = ScoreWithChannelQuality(c, task, avgPriceByCanonical, correction)
		case flags.UseSimplifiedScoring:
			bd = ScoreSimplified(c, task, avgPriceByCanonical, correction)
		default:
			// NET-013 fix: 完整 Score 路径构建断裂（profileWeightsFromFlags
			// 不存在，且 Score 签名变更）。WIP feature，临时统一走简化分支。
			// 修复 tracked in: <TBD>
			_ = sigs // 当前简化分支未使用，留待 profile/path 落地后回归
			bd = ScoreSimplified(c, task, avgPriceByCanonical, correction)
		}
		scored = append(scored, ScoredCandidate{Candidate: c, Breakdown: bd})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
	})

	// CHANNEL_QUALITY_ROUTING: 池分层（preferred / fallback）
	//
	// 业务诉求：当可靠的资源（Minimax 原厂）可用时优先使用；免费但
	// 不可靠的（NVIDIA NIM）在主渠道未用满之前原则上跳过。
	//
	// 实现：
	//   1. 按 ChannelQuality 把候选分成 preferred / fallback 两池
	//   2. preferred 池足够（>= topN）→ 只用 preferred
	//   3. preferred 池不足 → 用 fallback 补足
	//      - 主渠道未饱和（任意 preferred 的 PressureRatio < 0.95）
	//        → fallback composite *= 0.5（demotion 严格，fallback 难胜出）
	//      - 主渠道完全饱和 → fallback composite *= 0.85（demotion 放宽）
	if flags.UseChannelQualityRouting {
		scored = stratifyAndPickTopN(scored, topN)
	}

	if len(scored) > topN {
		scored = scored[:topN]
	}

	if len(scored) > 0 && scored[0].Breakdown.MatchScore < 30 {
		fallback := idx.get48hFallback(ctx)
		if fallback != nil {
			return []ScoredCandidate{{
				Candidate: *fallback,
				Breakdown: ScoringBreakdown{
					Composite:  50,
					MatchScore: scored[0].Breakdown.MatchScore,
					PriceScore: 50,
				},
			}}
		}
	}

	return scored
}

func (idx *Index) filterCurrentlyAvailable(
	ctx context.Context,
	pool *pgxpool.Pool,
	override func(context.Context, *pgxpool.Pool, []Candidate) ([]Candidate, error),
	all []Candidate,
) ([]Candidate, error) {
	if override != nil {
		return override(ctx, pool, all)
	}
	return filterCurrentlyAvailable(ctx, pool, all)
}

func (idx *Index) loadCorrectionScores(
	ctx context.Context,
	pool *pgxpool.Pool,
	override func(context.Context, *pgxpool.Pool, string, int, TaskType) (map[string]float64, error),
	sessionID string,
	task TaskType,
) (map[string]float64, error) {
	if override != nil {
		return override(ctx, pool, sessionID, 0, task)
	}
	return loadCorrectionScores(ctx, pool, sessionID, task)
}

func loadCorrectionScores(ctx context.Context, pool *pgxpool.Pool, sessionID string, task TaskType) (map[string]float64, error) {
	if pool == nil || strings.TrimSpace(sessionID) == "" {
		return map[string]float64{}, nil
	}

	var lastTask string
	var lastModel string
	var lastSuccess bool
	var lastLatencyMs int
	err := pool.QueryRow(ctx, `
		SELECT
			COALESCE(NULLIF(task_type, ''), NULLIF(task_type_chosen, ''), 'chat') AS last_task,
			COALESCE(NULLIF(model_chosen, ''), NULLIF(client_model, ''), '') AS last_model,
			success,
			COALESCE(latency_ms, 0) AS last_latency_ms
		FROM request_logs
		WHERE gw_session_id = $1
		  AND is_auto_request = TRUE
		ORDER BY ts DESC
		LIMIT 1
	`, sessionID).Scan(&lastTask, &lastModel, &lastSuccess, &lastLatencyMs)
	if err != nil {
		return map[string]float64{}, nil
	}

	score := ComputeCorrectionScore(
		TaskType(lastTask),
		lastModel,
		lastSuccess,
		lastLatencyMs,
		task,
		lastModel,
	)
	if score == 0 || strings.TrimSpace(lastModel) == "" {
		return map[string]float64{}, nil
	}
	return map[string]float64{lastModel: score}, nil
}

func filterCurrentlyAvailable(ctx context.Context, pool *pgxpool.Pool, all []Candidate) ([]Candidate, error) {
	if pool == nil {
		return fallbackSnapshotAvailability(all), nil
	}

	requested := make(map[string]Candidate, len(all))
	pairs := make([]string, 0, len(all))
	for _, c := range all {
		if c.CredentialID == 0 || c.CanonicalName == "" {
			continue
		}
		key := availabilityKey(c.CredentialID, c.CanonicalName)
		if _, exists := requested[key]; exists {
			continue
		}
		requested[key] = c
		pairs = append(pairs, key)
	}

	if len(requested) == 0 {
		return fallbackSnapshotAvailability(all), nil
	}

	rows, err := pool.Query(ctx, `
		SELECT cmb.credential_id, mc.canonical_name
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN models_canonical mc ON mc.id = pm.canonical_id
		WHERE (cmb.credential_id::text || ':' || lower(mc.canonical_name)) = ANY($1)
		  AND cmb.available = TRUE
		  AND pm.available = TRUE
		  AND (cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%')
		  AND (pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%')
	`, pairs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	allowed := make(map[string]struct{}, len(requested))
	for rows.Next() {
		var credentialID int64
		var canonicalName string
		if err := rows.Scan(&credentialID, &canonicalName); err != nil {
			return nil, err
		}
		key := availabilityKey(credentialID, canonicalName)
		if _, ok := requested[key]; ok {
			allowed[key] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	filtered := make([]Candidate, 0, len(all))
	for _, c := range all {
		if _, ok := allowed[availabilityKey(c.CredentialID, c.CanonicalName)]; ok {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func fallbackSnapshotAvailability(all []Candidate) []Candidate {
	filtered := make([]Candidate, 0, len(all))
	for _, c := range all {
		if c.UnavailableReason == "" {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func availabilityKey(credentialID int64, canonicalName string) string {
	return strconv.FormatInt(credentialID, 10) + ":" + strings.ToLower(canonicalName)
}

// getHotTop3Canonicals queries the top 3 canonical models by successful
// 48-hour request volume. Uses a 2-minute TTL cache to reduce request_logs scans.
func (idx *Index) getHotTop3Canonicals(ctx context.Context) []int {
	// Check cache first
	idx.hotCanonicalMu.RLock()
	if time.Since(idx.hotCanonicalsTS) < idx.hotCanonicalsTTL && len(idx.hotCanonicals) > 0 {
		cached := make([]int, len(idx.hotCanonicals))
		copy(cached, idx.hotCanonicals)
		idx.hotCanonicalMu.RUnlock()
		return cached
	}
	idx.hotCanonicalMu.RUnlock()

	// Cache miss or stale, query DB
	if idx.pool == nil {
		return []int{}
	}

	rows, err := idx.pool.Query(ctx, `
		SELECT canonical_id, count(*) as usage_count
		FROM request_logs
		WHERE ts > NOW() - INTERVAL '48 hours'
		  AND success = TRUE
		  AND canonical_id IS NOT NULL
		GROUP BY canonical_id
		ORDER BY usage_count DESC
		LIMIT 3
	`)
	if err != nil {
		return []int{}
	}
	defer rows.Close()

	var result []int
	for rows.Next() {
		var id int
		var count int64
		if err := rows.Scan(&id, &count); err == nil {
			result = append(result, id)
		}
	}

	// Update cache
	idx.hotCanonicalMu.Lock()
	idx.hotCanonicals = result
	idx.hotCanonicalsTS = time.Now()
	idx.hotCanonicalMu.Unlock()

	return result
}

// get48hFallback implements the 48-hour fallback. It reuses the cached
// hot canonical result when available and re-applies current availability filtering.
func (idx *Index) get48hFallback(ctx context.Context) *Candidate {
	hotTop := idx.getHotTop3Canonicals(ctx)
	if len(hotTop) == 0 {
		return nil
	}

	canonicalID := hotTop[0]

	idx.mu.RLock()
	pool := idx.pool
	availabilityFilter := idx.availabilityFilter
	candidates := make([]Candidate, 0)
	for i := range idx.entries {
		c := idx.entries[i]
		if c.CanonicalID == canonicalID {
			candidates = append(candidates, c)
		}
	}
	idx.mu.RUnlock()

	if len(candidates) == 0 {
		return nil
	}

	filtered, err := idx.filterCurrentlyAvailable(ctx, pool, availabilityFilter, candidates)
	if err != nil {
		filtered = fallbackSnapshotAvailability(candidates)
	}
	if len(filtered) == 0 {
		return nil
	}

	var best *Candidate
	for i := range filtered {
		c := filtered[i]
		if best == nil || c.SuccessRate > best.SuccessRate {
			bestCopy := c
			best = &bestCopy
		}
	}

	return best
}

// ValidateCachedChoice verifies that a cached credential/model pair is still available.
func ValidateCachedChoice(ctx context.Context, pool *pgxpool.Pool, credentialID int64, canonicalName string) bool {
	if pool == nil {
		return false
	}

	var available bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			JOIN models_canonical mc ON mc.id = pm.canonical_id
			WHERE cmb.credential_id = $1
			  AND mc.canonical_name = $2
			  AND cmb.available = TRUE
			  AND pm.available = TRUE
			  AND (cmb.unavailable_reason IS NULL OR cmb.unavailable_reason NOT LIKE 'manual%')
			  AND (pm.unavailable_reason IS NULL OR pm.unavailable_reason NOT LIKE 'manual%')
		)
	`, credentialID, canonicalName).Scan(&available)

	return err == nil && available
}

// stratifyAndPickTopN 实现池分层 + fallback 降权（CHANNEL_QUALITY_ROUTING）。
//
// 入参 scored 必须已经按 Composite 降序排列。函数返回调整后的 topN 候选。
//
// 行为：
//   - 把 scored 拆为 preferred（ChannelQuality >= 50）与 fallback
//   - 重新排序（按 composite → reliability → price）保证决胜规则一致
//   - 若 preferred 数量 >= topN：仅取 preferred
//   - 否则：用 fallback 补足，对 fallback 的 composite 施加 demotion
//   - 主渠道未饱和 → 0.5
//   - 主渠道饱和     → 0.85
//   - 没有 Preferred 池（冷启动 / 全 fallback）→ 1.0（无 demotion）
//   - 重新按 composite 排序后取 topN
//
// 注：demotion 只影响 composite，不修改 ChannelQuality/Reliability 等
// 维度分数，保证可观测性（X-Gw-Auto-Decision header 仍能看到原始分）。
//
// 埋点：函数返回前调用 recordRoutingDecision，记录 winner 的 pool
// 与 demotion 行为到 llmgw_autoroute_* Prometheus 指标。
func stratifyAndPickTopN(scored []ScoredCandidate, topN int) []ScoredCandidate {
	if topN <= 0 {
		topN = 3
	}
	preferred, fallback := StratifyByChannelQuality(scored)

	// 全部 preferred 都能进 topN → 排序后截断
	if len(preferred) >= topN {
		sortScoredByCompositeReliabilityPrice(preferred)
		result := preferred[:topN]
		recordRoutingDecision(PoolLabelPreferred, ReasonLabelNoDemotion,
			result[0].Breakdown.ChannelQuality, 1.0)
		return result
	}

	// 没有 Preferred 池 → 不施加 demotion（冷启动 / 全 fallback 时
	// 没有"主渠道"需要保护）。这是对 BUG #2 修复后的语义。
	if len(preferred) == 0 {
		sortScoredByCompositeReliabilityPrice(fallback)
		if len(fallback) > topN {
			fallback = fallback[:topN]
		}
		if len(fallback) > 0 {
			recordRoutingDecision(PoolLabelFallback, ReasonLabelEmptyPreferred,
				fallback[0].Breakdown.ChannelQuality, 1.0)
		}
		return fallback
	}

	// 主渠道饱和？决定 demotion 系数
	saturated := IsPreferredChannelSaturated(preferred)
	reason := ReasonLabelDemotion05
	factor := FallbackDemotionFactor
	if saturated {
		reason = ReasonLabelDemotion085
		factor = FallbackDemotionFactorSaturated
	}

	// preferred 全保留 + fallback 补足
	combined := make([]ScoredCandidate, 0, len(preferred)+len(fallback))
	combined = append(combined, preferred...)
	combined = append(combined, fallback...)

	// 对 fallback 施加 demotion
	if factor < 1.0 {
		for i := range combined {
			if combined[i].Breakdown.ChannelQuality < ChannelQualityPreferredThreshold {
				combined[i].Breakdown.Composite *= factor
			}
		}
	}

	// 重新排序（按 composite → reliability → price）
	sortScoredByCompositeReliabilityPrice(combined)

	if len(combined) > topN {
		combined = combined[:topN]
	}

	// 埋点：winner 可能在 preferred 也可能在 fallback
	if len(combined) > 0 {
		winner := combined[0]
		pool := PoolLabelPreferred
		if winner.Breakdown.ChannelQuality < ChannelQualityPreferredThreshold {
			pool = PoolLabelFallback
		}
		recordRoutingDecision(pool, reason, winner.Breakdown.ChannelQuality, factor)
	}
	return combined
}

// sortScoredByCompositeReliabilityPrice 是 stratifyAndPickTopN 用的内部
// 排序函数：先 composite 降序，再 reliability 降序，最后 price 降序。
//
// 注意：sort.SliceStable 在原 Composite 不变时不会重新排，所以这里
// 必须用 sort.Slice 才能让 tie-breaker 真正生效。
func sortScoredByCompositeReliabilityPrice(s []ScoredCandidate) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Breakdown.Composite != s[j].Breakdown.Composite {
			return s[i].Breakdown.Composite > s[j].Breakdown.Composite
		}
		if s[i].Breakdown.Reliability != s[j].Breakdown.Reliability {
			return s[i].Breakdown.Reliability > s[j].Breakdown.Reliability
		}
		return s[i].Breakdown.PriceScore > s[j].Breakdown.PriceScore
	})
}
