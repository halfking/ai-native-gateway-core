package autoroute

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// DecideV2 是新的决策逻辑，集成了：
//  1. 会话缓存可用性重校验
//  2. 调用 RecommendV2 进行候选推荐
//  3. 改进的审计与日志
//  4. P2.2 optimizer 插件集成（PreClassify/PostClassify/RecommendModel/RecordFeedback）
//
// 通过 Feature Flag 控制是否启用。
func (d *Decider) DecideV2(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
	// F-7: 监控路由决策耗时
	defer recordDecisionLatency(time.Now())

	if ctx == nil {
		ctx = context.Background()
	}

	// P2.2: attach request metadata so optimizer hooks can read
	// userID/session/client without changing the interface signatures.
	if d.optimizer != nil {
		ctx = routingopt.WithRequestMeta(ctx, routingopt.RequestMeta{
			UserID:     apiKeyID,
			SessionID:  sessionID,
			ClientType: sigs.ClientType,
		})
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
	requestedWorkType := workTypeFromContext(ctx)
	if requestedWorkType != "" {
		if l1, ok := d.ResolveWorkType(requestedWorkType); ok {
			taskHint = l1
		} else {
			requestedWorkType = ""
		}
	}
	if flags.UseCacheRevalidation && sessionID != "" && d.intentCache != nil {
		// 2026-07-27 concurrency fix: use IncrementHit so the read-modify-write
		// of HitCount happens under a single write lock (Get→HitCount++→Put
		// raced across concurrent requests on the same session).
		if cached, ok := d.intentCache.IncrementHit(sessionID); ok {
			// R48: 会话角色变化即身份变化，缓存结论失效重判（仅 flag 开启时）。
			roleMismatch := d.roleRoutingActive() &&
				normalizeAgentRole(cached.Role) != normalizeAgentRole(sigs.AgentRole)
			if roleMismatch {
				d.intentCache.Invalidate(sessionID)
				slog.Info("autoroute.v2: session role changed, reclassifying",
					"session_id", sessionID,
					"cached_role", cached.Role,
					"new_role", sigs.AgentRole,
				)
			} else if cached.WorkType != requestedWorkType {
				d.intentCache.Invalidate(sessionID)
			} else if !shouldReclassify(cached.TaskType, sigs, cached.HitCount) {
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
						// R48: 缓存命中也带角色/任务类型审计字段（flag 开启时）；
						// 旧缓存缺 kind 时归一为 unknown，保持审计形态一致。
						if d.roleRoutingActive() {
							decision.SessionRole = string(normalizeAgentRole(cached.Role))
							decision.TaskKind = string(normalizeTaskKind(cached.Kind))
						}
						d.annotateTreatment(ctx, apiKeyID, decision)
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

	// P2.2: PreClassify plugin hook (enhance signals before classification)
	if d.optimizer != nil {
		enhanced, err := d.optimizer.PreClassify(ctx, &sigs)
		if err != nil {
			slog.WarnContext(ctx, "optimizer.PreClassify failed, using original signals",
				"err", err, "api_key_id", apiKeyID)
		} else if enhanced != nil {
			// Extract Original signals from the enhanced wrapper.
			// The interface{} type is used to avoid circular dependency;
			// real implementation returns *routingopt.EnhancedSignals with GetOriginal() method.
			if e, ok := enhanced.(interface{ GetOriginal() interface{} }); ok {
				if orig := e.GetOriginal(); orig != nil {
					if origSigs, ok := orig.(*ClassificationSignals); ok {
						sigs = *origSigs
					}
				}
			}
		}
	}

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

	// P2.2: PostClassify plugin hook (adjust confidence after classification)
	if d.optimizer != nil {
		adjustedConf, err := d.optimizer.PostClassify(ctx, string(cls.Primary), cls.Confidence)
		if err != nil {
			slog.WarnContext(ctx, "optimizer.PostClassify failed, using original confidence",
				"err", err, "task_type", cls.Primary, "confidence", cls.Confidence)
		} else {
			// Clamp adjusted confidence to [0.0, 1.0]
			if adjustedConf < 0.0 {
				adjustedConf = 0.0
			}
			if adjustedConf > 1.0 {
				adjustedConf = 1.0
			}
			cls.Confidence = adjustedConf
		}
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
	keepFullCandidateSet := d.workTypeRouteStore != nil &&
		(d.workTypeRouteStore.HasRoutes(task) || len(requestedWorkType) > 0)
	if d.overrideStore != nil && len(d.overrideStore.GetPins(task, prof)) > 0 {
		keepFullCandidateSet = true
	}
	// R48: role 路由开启且请求带可路由角色时，偏好模型可能在 top-N 之外，
	// 需要全候选窗口（与 work-type/pin 同一处理）。
	roleRoutingOn := d.roleRoutingActive() && roleRoutedRoles[normalizeAgentRole(sigs.AgentRole)]
	if roleRoutingOn {
		keepFullCandidateSet = true
	}
	if keepFullCandidateSet {
		if poolSize := len(idx.Snapshot()); poolSize > candidateTopN {
			candidateTopN = poolSize
		}
	}

	// requestID 来自请求 context（由 maybeResolveAuto 注入），用于 affinity
	// 的 explore 分桶。缺失时 explore 退化为「不探索」，affinity 仍可应用。
	// FilterNotes 收集 RT-1 IQ 门禁等硬过滤的排除原因，进 Decision metadata。
	var filterReasons []string
	recommended := idx.RecommendV2WithHints(ctx, cls.Primary, sigs, profile, sessionID, candidateTopN, DecisionHints{
		RequestID:        requestIDFromContext(ctx),
		ApiKeyID:         apiKeyID,
		FullCandidateSet: keepFullCandidateSet,
		FilterNotes:      &filterReasons,
	})

	// P2.2: plugin re-ranking. Runs before explicit-default/override so
	// admin pins and tenant defaults keep precedence over the optimizer.
	// P2.5: cls/sigs additionally feed the ONNX ML re-ranker's features.
	recommended = d.recommendWithOptimizer(ctx, recommended, cls, sigs, profile, apiKeyID, sessionID, sigs.ClientType)

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
	tierFailoverModels := []string(nil)
	// R48 修订（2026-09-20，245 e2e 实测抓到）：role 偏好必须在 work-type
	// tier 过滤之前算出并并入 tier 豁免名单——applyTierPolicyWithRoutes 的
	// pinned 参数仅用于过滤豁免（无 pin 提升语义）。不并入则文档化级联
	// pin > role_route > work_type tier 自相矛盾：tier 配置不含偏好模型时
	// （实测 worker+solution 的 gpt-5.6-sol 被 glm-5.2 secondary 档滤除）
	// promoteFirstPresent 找不到偏好，role_route 静默让位。
	roleKind := TaskKind("")
	if d.roleRoutingActive() {
		roleKind = ClassifyTaskKind(sigs)
	}
	rolePrefs := []string(nil)
	if roleRoutingOn {
		rolePrefs = d.roleLLMRouter.SelectLLM(normalizeAgentRole(sigs.AgentRole), roleKind)
	}
	if d.workTypeRouteStore != nil {
		pins := []string(nil)
		if d.overrideStore != nil {
			pins = d.overrideStore.GetPins(task, prof)
		}
		tierFailoverModels = d.workTypeRouteStore.TierFailoverModelsWithWorkType(recommended, task, requestedWorkType)
		recommended = d.workTypeRouteStore.ApplyTierPolicyWithWorkType(recommended, task, requestedWorkType, append(pins, rolePrefs...))
	}
	tierChangedWinner := len(recommended) > 0 && recommended[0].Candidate.CanonicalName != beforeBoostWinner

	// Step 3b（R48, 2026-09-20）: role × kind promotion —— work-type tier
	// policy 之后、pin promote 之前插入，admin pin 仍是最强约束。仅
	// AUTO_ROLE_ROUTING_ENABLED 开启 + 子代理角色时介入；flag-off / main /
	// unknown 路径零改动（决策字节级不变）。
	roleChangedWinner := false
	if len(rolePrefs) > 0 {
		if promoted, hit := promoteFirstPresent(recommended, rolePrefs); hit != "" {
			recommended = promoted
			roleChangedWinner = len(recommended) > 0 && recommended[0].Candidate.CanonicalName != beforeBoostWinner
		}
	}

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
	} else if roleChangedWinner {
		routingSource = "role_route"
	} else if tierChangedWinner || (len(recommended) > 0 && recommended[0].Breakdown.RouteTier != "") {
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
		TierFailoverModels: tierFailoverModels,
		EnabledFeatures:    enabledFeatures,
		FallbackUsed:       fallbackUsed,
		FilterReasons:      filterReasons,
		DecidedAt:          time.Now(),
		RoutingSource:      routingSource,
	}
	// R48: role 路由审计字段（flag 开启时；无论是否命中提升都记录观察值）。
	if d.roleRoutingActive() {
		decision.SessionRole = string(normalizeAgentRole(sigs.AgentRole))
		decision.TaskKind = string(normalizeTaskKind(roleKind))
	}
	d.annotateTreatment(ctx, apiKeyID, decision)
	d.populateShadow(ctx, sigs, decision)

	// P2.2: fire-and-forget feedback for the learning loop (fresh decisions only).
	d.recordFeedbackAsync(ctx, decision, apiKeyID, sessionID, sigs.ClientType)

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
		cachedPut := CachedIntent{
			TaskType:     decision.TaskType,
			WorkType:     requestedWorkType,
			ChosenModel:  decision.ChosenModel,
			CredentialID: decision.ChosenCredentialID,
			Profile:      decision.Profile,
			Confidence:   decision.Confidence,
			Classifier:   decision.Classifier,
		}
		// R48: 角色/任务类型随 intent 缓存（flag 开启时），跨轮复用不退化。
		if d.roleRoutingActive() {
			cachedPut.Role = normalizeAgentRole(sigs.AgentRole)
			cachedPut.Kind = normalizeTaskKind(roleKind)
		}
		d.intentCache.Put(sessionID, cachedPut)
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
