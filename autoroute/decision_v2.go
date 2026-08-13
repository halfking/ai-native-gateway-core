package autoroute

import (
	"context"
	"log/slog"
	"time"
)

// DecideV2 是新的决策逻辑，集成了：
//  1. 会话缓存可用性重校验
//  2. 调用 RecommendV2 进行候选推荐
//  3. 改进的审计与日志
//
// 通过 Feature Flag 控制是否启用。
func (d *Decider) DecideV2(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 类型断言：获取具体的 *Index 类型以访问 V2 方法
	idx, ok := d.index.(*Index)
	if !ok {
		// 如果不是 *Index 类型（比如测试时的 stub），回退到旧逻辑
		slog.Warn("autoroute.v2: index is not *Index type, falling back to v1")
		return d.Decide(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	}

	flags := GetFeatureFlags()
	enabledFeatures := activeFeatureNames(flags)

	// Step 0: 会话缓存检查（仅在启用重校验时保留该分支）
	if flags.UseCacheRevalidation && sessionID != "" && d.intentCache != nil {
		// 2026-07-27 concurrency fix: use IncrementHit so the read-modify-write
		// of HitCount happens under a single write lock (Get→HitCount++→Put
		// raced across concurrent requests on the same session).
		if cached, ok := d.intentCache.IncrementHit(sessionID); ok {
			if !shouldReclassify(cached.TaskType, sigs, cached.HitCount) {
				// 新增：验证缓存的模型是否仍可用
				// 2026-07-27 concurrency fix: read idx.pool under RLock
				// (SetPool writes it under the write lock). Snapshot the
				// pointer, release, then run the availability check off-lock.
				idx.mu.RLock()
				pool := idx.pool
				idx.mu.RUnlock()
				if pool != nil && ValidateCachedChoice(ctx, pool, cached.CredentialID, cached.ChosenModel) {
					// 2026-08-11 fix: ValidateCachedChoice only checks DB-level
					// availability + 'manual%' reasons. It does NOT consult the
					// in-memory OverrideStore, so an admin ban applied AFTER the
					// session was cached would keep serving the banned model
					// until cache eviction. Re-check the ban set here; if the
					// cached choice is now banned, fall through to reclassify
					// (same path as "no longer available") instead of reusing.
					bannedByOverride := false
					if d.overrideStore != nil {
						task := string(cached.TaskType)
						prof := string(cached.Profile)
						if d.overrideStore.GetBans(task, prof)[cached.ChosenModel] {
							bannedByOverride = true
						}
					}
					if !bannedByOverride {
						slog.Info("autoroute.v2: reusing cached decision (revalidated)",
							"session_id", sessionID,
							"cached_model", cached.ChosenModel,
							"task_type", cached.TaskType,
						)
						decision := &Decision{
							ChosenModel:        cached.ChosenModel,
							ChosenCredentialID: cached.CredentialID,
							ChosenRawModel:     cached.ChosenModel,
							TaskType:           cached.TaskType,
							Confidence:         cached.Confidence,
							Profile:            cached.Profile,
							Classifier:         "session_cache_v2",
							Reason:             "reused session intent (revalidated)",
							EnabledFeatures:    enabledFeatures,
							CacheReused:        true,
							DecidedAt:          time.Now(),
							RoutingSource:      "session_cache",
						}
						d.populateShadow(ctx, sigs, decision)
						return decision, nil
					}
					// Cached choice is banned by an admin override — clear the
					// stale cache entry and reclassify.
					d.intentCache.Invalidate(sessionID)
					slog.Info("autoroute.v2: cached choice banned by override, reclassifying",
						"session_id", sessionID,
						"cached_model", cached.ChosenModel,
					)
				} else {
					// 可用性校验失败，清除缓存并重新决策
					d.intentCache.Invalidate(sessionID)
					slog.Info("autoroute.v2: cached choice no longer available, reclassifying",
						"session_id", sessionID,
						"cached_model", cached.ChosenModel,
					)
				}
			} else {
				slog.Info("autoroute.v2: task type changed, reclassifying",
					"session_id", sessionID,
					"cached_task", cached.TaskType,
					"new_signals", sigs,
				)
			}
		}
	}

	// Step 1: 解析 profile
	profile := d.resolveProfile(ctx, apiKeyID, headerProfile)

	// Step 2: 任务分类
	cls, err := d.classify(ctx, sigs, taskHint)
	if err != nil {
		// 分类失败，使用默认 chat
		cls = &Classification{
			Primary:    TaskChat,
			Confidence: 0.3,
			Classifier: "default",
			Reason:     "classification failed: " + err.Error(),
		}
		slog.Warn("autoroute.v2: classification failed, using default chat",
			"error", err,
		)
	}

	// Step 3: 候选推荐（使用新逻辑）
	// Work-type preferences and explicit pins must see the configured candidate,
	// not only the initial TopN. Keep the normal small window unless either
	// constraint is present, then trim back to TopN after applying it.
	task := string(cls.Primary)
	prof := string(profile)
	resultTopN := d.TopN
	if resultTopN <= 0 {
		resultTopN = 3
	}
	candidateTopN := resultTopN
	keepFullCandidateSet := d.workTypeRouteStore != nil && d.workTypeRouteStore.HasRoutes(task)
	if d.overrideStore != nil && len(d.overrideStore.GetPins(task, prof)) > 0 {
		keepFullCandidateSet = true
	}
	if keepFullCandidateSet {
		if poolSize := len(idx.Snapshot()); poolSize > candidateTopN {
			candidateTopN = poolSize
		}
	}

	// requestID 来自请求 context（由 maybeResolveAuto 注入），用于 affinity
	// 的 explore 分桶。缺失时 explore 退化为「不探索」，affinity 仍可应用。
	recommended := idx.RecommendV2WithHints(ctx, cls.Primary, sigs, profile, sessionID, candidateTopN, DecisionHints{
		RequestID:        requestIDFromContext(ctx),
		ApiKeyID:         apiKeyID,
		FullCandidateSet: keepFullCandidateSet,
	})

	// Step 3a (M2): 路由来源标签。V2 不调用 defaultRoutingStore（explicit_default
	// 是 V1 的隐式 tag 路径；V2 用 channel-quality routing 取代），所以 V2 的
	// 来源只有 implicit_tag / override_pin / session_cache 三种。
	// 之前 V2 完全不填 RoutingSource，导致 V1/V2 的审计/日志不一致——此处补齐。
	routingSource := "implicit_tag"

	// Apply operator overrides around work-type preferences. Bans are applied
	// first, boosts only affect the remaining candidates, and pins are applied
	// last so an explicit pin remains the strongest routing constraint.
	if d.overrideStore != nil {
		recommended = d.overrideStore.FilterBanned(recommended, task, prof)
	}

	beforeBoostWinner := ""
	if len(recommended) > 0 {
		beforeBoostWinner = recommended[0].Candidate.CanonicalName
	}
	if d.workTypeRouteStore != nil {
		recommended = d.workTypeRouteStore.ApplyBoost(recommended, task)
	}
	boostChangedWinner := len(recommended) > 0 && recommended[0].Candidate.CanonicalName != beforeBoostWinner

	beforePinWinner := ""
	if len(recommended) > 0 {
		beforePinWinner = recommended[0].Candidate.CanonicalName
	}
	if d.overrideStore != nil {
		recommended = d.overrideStore.PromotePins(recommended, task, prof)
	}
	pinChangedWinner := len(recommended) > 0 && recommended[0].Candidate.CanonicalName != beforePinWinner
	if pinChangedWinner {
		routingSource = "override_pin"
	} else if boostChangedWinner {
		routingSource = "work_type_route"
	}
	if len(recommended) > resultTopN {
		recommended = recommended[:resultTopN]
	}

	// Step 4: 检查是否有候选
	if len(recommended) == 0 {
		slog.Warn("autoroute.v2: no candidates match task type",
			"task_type", cls.Primary,
			"signals", sigs,
		)
		return nil, ErrNoCandidates
	}

	winner := recommended[0]

	// Detect whether the winner came from the 48h popularity fallback.
	// RecommendV2's fallback path always returns a single candidate with
	// the exact breakdown {Composite: 50, MatchScore <= 30, PriceScore: 50}.
	fallbackUsed := isFallbackWinner(recommended)

	// Step 5: 构建决策
	decision := &Decision{
		ChosenModel:        winner.Candidate.CanonicalName,
		ChosenCredentialID: winner.Candidate.CredentialID,
		ChosenRawModel:     winner.Candidate.RawModel,
		TaskType:           cls.Primary,
		Confidence:         cls.Confidence,
		Profile:            profile,
		Classifier:         cls.Classifier + "_v2",
		Reason:             cls.Reason,
		CandidatesTopN:     recommended,
		EnabledFeatures:    enabledFeatures,
		FallbackUsed:       fallbackUsed,
		DecidedAt:          time.Now(),
		RoutingSource:      routingSource,
	}
	d.populateShadow(ctx, sigs, decision)

	slog.Info("autoroute.v2: decision made",
		"chosen_model", decision.ChosenModel,
		"task_type", decision.TaskType,
		"confidence", decision.Confidence,
		"intent_match_score", winner.Breakdown.MatchScore,
		"price_score", winner.Breakdown.PriceScore,
		"channel_quality", winner.Breakdown.ChannelQuality,
		"reliability", winner.Breakdown.Reliability,
		"composite_score", winner.Breakdown.Composite,
		"channel_category", winner.Candidate.ProviderCategory,
		"channel_is_free", winner.Candidate.IsFree,
	)

	// CHANNEL_QUALITY_ROUTING: 路由决策结构化日志
	// 让日志聚合系统（loki / elasticsearch）可按 pool 拆分统计。
	poolLabel := "preferred"
	if winner.Breakdown.ChannelQuality < ChannelQualityPreferredThreshold {
		poolLabel = "fallback"
	}
	slog.Info("autoroute.routing.pool",
		"task_type", decision.TaskType,
		"chosen_model", decision.ChosenModel,
		"pool", poolLabel,
		"channel_quality", winner.Breakdown.ChannelQuality,
		"channel_category", winner.Candidate.ProviderCategory,
		"channel_is_free", winner.Candidate.IsFree,
	)

	// Step 6: 缓存决策
	if sessionID != "" && d.intentCache != nil {
		d.intentCache.Put(sessionID, CachedIntent{
			TaskType:     decision.TaskType,
			ChosenModel:  decision.ChosenModel,
			CredentialID: decision.ChosenCredentialID,
			Profile:      decision.Profile,
			Confidence:   decision.Confidence,
			Classifier:   decision.Classifier,
		})
	}

	return decision, nil
}

// ErrNoCandidates 是当没有可用候选时返回的错误
var ErrNoCandidates = &DecisionError{
	Code:    "no_candidates",
	Message: "no candidates match task type after filtering",
}

// DecisionError 是决策过程中的结构化错误
type DecisionError struct {
	Code    string
	Message string
}

func (e *DecisionError) Error() string {
	return e.Message
}
