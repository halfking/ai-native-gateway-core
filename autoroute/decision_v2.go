package autoroute

import (
	"context"
	"log/slog"
	"time"
)

// DecideV2 是新的决策逻辑，集成了：
//   1. 会话缓存可用性重校验
//   2. 调用 RecommendV2 进行候选推荐
//   3. 改进的审计与日志
//
// 通过 Feature Flag 控制是否启用。
func (d *Decider) DecideV2(ctx context.Context, sigs ClassificationSignals, apiKeyID int, headerProfile string, taskHint TaskType, sessionID string) (*Decision, error) {
	// 类型断言：获取具体的 *Index 类型以访问 V2 方法
	idx, ok := d.index.(*Index)
	if !ok {
		// 如果不是 *Index 类型（比如测试时的 stub），回退到旧逻辑
		slog.Warn("autoroute.v2: index is not *Index type, falling back to v1")
		return d.Decide(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	}

	// Step 0: 会话缓存检查（带可用性重校验）
	if sessionID != "" && d.intentCache != nil {
		if cached, ok := d.intentCache.Get(sessionID); ok {
			if !shouldReclassify(cached.TaskType, sigs) {
				// 新增：验证缓存的模型是否仍可用
				if idx.pool != nil && ValidateCachedChoice(ctx, idx.pool, cached.CredentialID, cached.ChosenModel) {
					slog.Info("autoroute.v2: reusing cached decision (revalidated)",
						"session_id", sessionID,
						"cached_model", cached.ChosenModel,
						"task_type", cached.TaskType,
					)
					return &Decision{
						ChosenModel:        cached.ChosenModel,
						ChosenCredentialID: cached.CredentialID,
						ChosenRawModel:     cached.ChosenModel,
						TaskType:           cached.TaskType,
						Confidence:         cached.Confidence,
						Profile:            cached.Profile,
						Classifier:         "session_cache_v2",
						Reason:             "reused session intent (revalidated)",
						DecidedAt:          time.Now(),
					}, nil
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
	recommended := idx.RecommendV2(ctx, cls.Primary, sigs, sessionID, d.TopN)

	// 应用 override store（如果配置）
	if d.overrideStore != nil {
		task := string(cls.Primary)
		prof := string(profile)
		filtered := d.overrideStore.FilterBanned(recommended, task, prof)
		recommended = d.overrideStore.PromotePins(filtered, task, prof)
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
		DecidedAt:          time.Now(),
	}

	slog.Info("autoroute.v2: decision made",
		"chosen_model", decision.ChosenModel,
		"task_type", decision.TaskType,
		"confidence", decision.Confidence,
		"intent_match_score", winner.Breakdown.MatchScore,
		"price_score", winner.Breakdown.PriceScore,
		"composite_score", winner.Breakdown.Composite,
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
