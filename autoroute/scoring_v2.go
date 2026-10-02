package autoroute

import (
	"math"
	"strings"
	"time"
)

// scoring_v2.go — V2 通道质量评分与池分层。
//
// ScoreWithChannelQuality 实现 4 维评分（CHANNEL_QUALITY_ROUTING 新逻辑）：
//   FinalScore = IntentMatch * 0.4 + Price * 0.2 + ChannelQuality * 0.3
//              + Reliability * 0.1 + Correction
//
// ScoreWithAffinity 在 4 维之上叠加第 5 维「学习亲和度」（0.15）：
//   FinalScore = 0.85 × (4-dim 加权) + 0.15 × Affinity + Correction
//
// 业务诉求（CHANNEL_QUALITY_ROUTING_DESIGN.md）：
//   "可靠的资源可用时（如 Minimax 原厂），优先使用；免费的、不可靠的
//    （如 NVIDIA NIM 免费凭据）在主渠道未用满之前原则上跳过，除非
//    该凭据历史上没有错误发生。同成本情况下，质量更好者优先。"
//
// 4 维评分通过新增 ChannelQuality（通道质量）和 Reliability（运行时
// 可靠度）两个维度实现"质量优先于价格"的诉求。ChannelQuality 综合
// providers.category 静态分 + 实时健康度调整；Reliability 直接由
// success_rate + p95_latency 推导。
//
// 本文件是 recommend_v2.go 唯一的评分实现来源；V1 兼容入口 ScoreSimplified
// 仍保留在 scoring_simplified.go 中。

// ScoreWithChannelQuality 实现 4 维评分（CHANNEL_QUALITY_ROUTING）：
//
//	FinalScore = IntentMatch * 0.4
//	           + Price * 0.2
//	           + ChannelQuality * 0.3
//	           + Reliability * 0.1
//	           + Correction
//
// 权重设计的理由：
//   - ChannelQuality 0.3：质量分主导，体现"质量优先于价格"
//   - IntentMatch 0.4：仍是最大权重，路由必须先匹配任务类型
//   - Price 0.2：从 0.4 降到 0.2，价格不再是主要决策因子
//   - Reliability 0.1：作为安全网，反映凭据的实时健康
//
// PriceScore 与 ScoreSimplified 共用 scorePriceByCostContext（P75 归一化，
// 详见该函数注释）。池分层（preferred/fallback）由 RecommendV2 在调
// 用本函数后单独处理：本函数只产出 composite，不施加 demotion 系数。
//
// 当 ProviderCategory 为空（冷启动 / SQL 尚未加载该字段）时，
// ChannelQuality 走默认 base=40；不会拉黑候选，但会让该候选落入
// fallback 池。
func ScoreWithChannelQuality(c Candidate, task TaskType, costCtx CostContext, correctionScore float64) ScoringBreakdown {
	intentMatch := c.TaskMatchScore * 100

	priceScore := scorePriceByCostContext(c, costCtx)
	channelQuality := scoreChannelQuality(c)
	reliability := scoreReliability(c)
	correction := clampCorrection(correctionScore)

	composite := intentMatch*0.4 +
		priceScore*0.2 +
		channelQuality*0.3 +
		reliability*0.1 +
		correction

	return ScoringBreakdown{
		MatchScore:     intentMatch,
		PriceScore:     priceScore,
		ChannelQuality: channelQuality,
		Reliability:    reliability,
		Composite:      composite,
		// 其余维度保持为 0，保持结构兼容
		SpeedScore:     0,
		StabilityScore: 0,
		PressureScore:  0,
		ContextFit:     0,
		VersionRecency: 0,
		StrengthMatch:  0,
	}
}

// AffinityWeight 是学习亲和度在 composite 中的权重。
// 现有 4 维权重整体乘 (1 - AffinityWeight) 后腾出这一份，因此 4 维之间的
// 相对比例完全不变——本次改动只是让出 15% 给实测信号，不重新平衡既有维度。
const AffinityWeight = 0.15

// ScoreWithAffinity 在 4 维渠道质量评分之上叠加第 5 维「学习亲和度」：
//
//	FinalScore = IntentMatch * 0.34
//	           + Price * 0.17
//	           + ChannelQuality * 0.255
//	           + Reliability * 0.085
//	           + Affinity * 0.15
//	           + Correction
//
// （0.4/0.2/0.3/0.1 各乘 0.85 得到前四项。）
//
// affinity 由调用方从 AffinityStore.Lookup 取得，已完成最小样本门槛、
// 陈旧衰减与 [10,90] 钳制；本函数只做 NaN/Inf 兜底。
//
// applied=false（shadow 模式或探索流量）时只记录 Affinity 字段，
// Composite 与 ScoreWithChannelQuality 逐位相同——这正是影子期的意义：
// 同一条代码路径既能观测又能生效，不存在「上线才第一次跑」的分支。
//
// 边界：亲和度只在**已通过硬约束**（活性过滤、ban）的候选之间排序。
// 它不能让不可用或被封禁的模型复活——那些过滤发生在 RecommendV2 里、
// 在本函数之前。
func ScoreWithAffinity(
	c Candidate,
	task TaskType,
	costCtx CostContext,
	correctionScore float64,
	affinity float64,
	applied bool,
) ScoringBreakdown {
	b := ScoreWithChannelQuality(c, task, costCtx, correctionScore)

	// NaN/Inf 兜底：坏数据退化为「无意见」，绝不污染 composite。
	if math.IsNaN(affinity) || math.IsInf(affinity, 0) {
		affinity = AffinityNeutral
	}
	b.Affinity = affinity
	b.AffinityApplied = applied

	if !applied {
		return b
	}

	// correction 是绝对偏移（±10），不应被 0.85 缩放稀释：先摘出、
	// 缩放 4 维基础分、叠加亲和度、再原样加回。
	correction := clampCorrection(correctionScore)
	base := b.Composite - correction
	b.Composite = base*(1-AffinityWeight) + affinity*AffinityWeight + correction

	return b
}

// ── 池分层（preferred/fallback） ──────────────────────────────────

// StratifyByChannelQuality 把候选按 ChannelQuality 拆分为 preferred
// 与 fallback 两个池。preferred 池的 ChannelQuality 严格 >= 阈值。
//
// 阈值通过 ChannelQualityPreferredThreshold（=50）常量定义。
//
// 调用方在排序后处理：
//   - 若 preferred 池足够（>= topN），只返回 preferred
//   - 否则用 fallback 池补足，并对 fallback 的 composite 施加 demotion
//
// 注意：此函数只做拆分，不修改 composite。要施加 demotion 请调用
// ApplyFallbackDemotion。
func StratifyByChannelQuality(scored []ScoredCandidate) (preferred, fallback []ScoredCandidate) {
	for _, sc := range scored {
		if sc.Breakdown.ChannelQuality >= ChannelQualityPreferredThreshold {
			preferred = append(preferred, sc)
			continue
		}
		fallback = append(fallback, sc)
	}
	return preferred, fallback
}

// FallbackDemotionFactor 是 fallback 池的 demotion 系数（严格 < 1.0）。
// 设计：preferred 池未饱和时给 0.5（fallback 难以胜出），
// saturated 时给 0.85（仍低于 preferred 但保留竞争力）。
//
// 详见 RecommendV2 中根据主渠道饱和度切换系数的逻辑。
const FallbackDemotionFactor = 0.5
const FallbackDemotionFactorSaturated = 0.85

// ApplyFallbackDemotion 给 fallback 候选的 composite 乘 demotion 系数。
// 注意：MatchScore / PriceScore / ChannelQuality / Reliability 维度
// 分数保持不变，只调整 composite。便于可观测：仍能看到 fallback 候选
// 自身的通道质量分，只是总分会下降。
func ApplyFallbackDemotion(scored []ScoredCandidate, factor float64) {
	for i := range scored {
		scored[i].Breakdown.Composite *= factor
	}
}

// IsPreferredChannelSaturated 判断主渠道（Preferred 池）是否饱和。
// 规则：Preferred 池中所有候选的 PressureRatio >= 0.95。
//
// 当 Preferred 池为空时（仅 fallback 候选），返回 true（视为饱和，
// 让 fallback 有机会胜出）。
func IsPreferredChannelSaturated(preferred []ScoredCandidate) bool {
	if len(preferred) == 0 {
		return true
	}
	for _, sc := range preferred {
		if sc.Candidate.PressureRatio < 0.95 {
			return false
		}
	}
	return true
}

// ── 会话校正（与 V2 推荐路径绑定） ──────────────────────────────

// ComputeCorrectionScore 计算会话校正分
//
// 基于上次任务结果：
//   - 上次成功且快速 → +5
//   - 上次失败 → -10
//   - 任务类型变化 → 0
//   - 无历史记录 → 0
//
// 参数：
//   - lastTask: 上次任务类型
//   - lastModel: 上次选择的模型 (canonical_name)
//   - lastSuccess: 上次是否成功
//   - lastLatencyMs: 上次延迟（毫秒）
//   - currentTask: 当前任务类型
//   - currentModel: 当前候选模型 (canonical_name)
//
// 返回：-10 ~ +10 的校正分
func ComputeCorrectionScore(
	lastTask TaskType,
	lastModel string,
	lastSuccess bool,
	lastLatencyMs int,
	currentTask TaskType,
	currentModel string,
) float64 {
	// 只对同一模型应用校正
	if lastModel != currentModel {
		return 0
	}

	// 任务类型变化 → 校正归零
	if lastTask != currentTask {
		return 0
	}

	// 上次失败 → 降权
	if !lastSuccess {
		return -10
	}

	// 上次成功且快速（< 2 秒）→ 小幅加分
	if lastLatencyMs > 0 && lastLatencyMs < 2000 {
		return 5
	}

	// 上次成功但较慢 → 不加分
	return 0
}

// ── 8 维补充分（scoreVersionRecency / scoreStrengthMatch）──
//
// 自 scoring_new.go（092ab1b61 删除）恢复：scoring.go 191/192 双引用
// 缺其定义即编译失败；手写泰勒 exp 已是 math.Exp（R67 F4 随迁）。
//
// scoreVersionRecency 和 scoreStrengthMatch

// scoreVersionRecency 根据模型发布时间和版本级次计算新旧度评分（0-100）。
//
// 策略：
//   - 高难度任务（reasoning/agent/code/long_context）→ 最新版得分最高（released_at 最近 180 天 = 100）
//   - 普通任务（chat/creative/function_call/vision）→ 次新版得分最高（version_rank=2 = 100，最新版略低 80）
//   - 按 released_at 衰减：每 365 天衰减到 50%（指数衰减）
//   - 未知 released_at 或 version_rank → 50（中性，不惩罚也不奖励）
//
// 高难度任务定义：需要最新推理能力、上下文理解、代码生成的任务。
func scoreVersionRecency(c Candidate, task TaskType) float64 {
	// 未知发布时间 → 中性分
	if c.ReleasedAt == nil {
		return 50
	}

	now := time.Now()
	daysSinceRelease := int(now.Sub(*c.ReleasedAt).Hours() / 24)
	if daysSinceRelease < 0 {
		daysSinceRelease = 0 // 未来日期（数据错误），当作今天发布
	}

	// 判断是否为高难度任务
	isHighDifficulty := task == TaskReasoning || task == TaskAgent ||
		task == TaskCode || task == TaskLongContext

	if isHighDifficulty {
		// 高难度任务：最新版（180 天内）得分最高 100，逐年衰减
		// 公式：score = 100 * exp(-days / 365)
		// 0 天 = 100, 180 天 ≈ 90, 365 天 = 50, 730 天 = 25
		decayFactor := float64(daysSinceRelease) / 365.0
		score := 100.0 * math.Exp(-decayFactor)
		if score < 10 {
			score = 10 // 最低保底 10 分（避免老版本完全被排除）
		}
		return score
	}

	// 普通任务：次新版得分最高（避免普通任务浪费最新版资源）
	// version_rank = 1（最新版）→ 80 分
	// version_rank = 2（次新版）→ 100 分
	// version_rank >= 3（稳定版）→ 按 released_at 衰减
	if c.VersionRank == 1 {
		// 最新版在普通任务上略低（鼓励用次新版）
		return 80
	}
	if c.VersionRank == 2 {
		// 次新版（sweet spot for 普通任务）
		return 100
	}

	// version_rank >= 3 或 未设置：按 released_at 衰减
	// 365 天内 = 70-90，1-2 年 = 50-70，2 年+ = 30-50
	decayFactor := float64(daysSinceRelease) / 730.0 // 2 年衰减周期
	score := 90.0 * math.Exp(-decayFactor)
	if score < 20 {
		score = 20
	}
	return score
}

// scoreStrengthMatch 计算候选模型的优势方向与任务类型的匹配度（0-100）。
//
// 与 scoreMatch（基于 tags）的区别：
//   - tags 是宽泛的能力标签（reasoning/code/...），自动化生成或推断
//   - strengths 是运营人工标注的优势方向，更精准（如 "math"/"long_context"/"multimodal"）
//
// 公式：
//
//	required = 任务类型所需的优势方向集合（定义见下）
//	hits = |required ∩ candidate.strengths|
//	score = (hits / |required|) × 100，至少 50（避免未标注模型被过度惩罚）
//
// 任务 → 优势方向映射：
//
//	reasoning    : ["reasoning", "math", "logic"]
//	code         : ["code", "programming"]
//	agent        : ["agent", "tool_use"]
//	creative     : ["creative", "writing"]
//	long_context : ["long_context"]
//	vision       : ["vision", "multimodal"]
//	function_call: ["tool_use", "function_call"]
//	chat         : []  (无特定要求，返回 75 基准分)
func scoreStrengthMatch(c Candidate, task TaskType) float64 {
	required := requiredStrengthsForTask(task)
	if len(required) == 0 {
		// chat 或未知任务 → 基准分 75（不惩罚也不特别奖励）
		return 75
	}

	// 候选模型未标注 strengths → 返回 50（中性，但低于有标注的）
	if len(c.Strengths) == 0 {
		return 50
	}

	// 计算交集
	hits := 0
	for _, r := range required {
		for _, s := range c.Strengths {
			if strings.EqualFold(s, r) || containsFold(s, r) {
				hits++
				break
			}
		}
	}

	// 匹配度：hits / required，映射到 50-100 区间
	// 全匹配 = 100，0 匹配 = 50
	matchRatio := float64(hits) / float64(len(required))
	score := 50 + matchRatio*50
	return score
}

// requiredStrengthsForTask 返回任务类型所需的优势方向列表。
//
// 与 requiredTagsForTask 的区别：strengths 更精准，要求更严格。
func requiredStrengthsForTask(task TaskType) []string {
	switch task {
	case TaskReasoning:
		return []string{"reasoning", "math", "logic"}
	case TaskCode:
		return []string{"code", "programming"}
	case TaskAgent:
		return []string{"agent", "tool_use"}
	case TaskCreative:
		return []string{"creative", "writing"}
	case TaskLongContext:
		return []string{"long_context"}
	case TaskVision:
		return []string{"vision", "multimodal"}
	case TaskFunctionCall:
		return []string{"tool_use", "function_call"}
	case TaskChat:
		return []string{} // 无特定要求
	default:
		return []string{}
	}
}
