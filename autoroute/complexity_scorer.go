package autoroute

// complexity_scorer.go — M2: 复杂度/难度路由维度。
//
// 评估单次请求的"难度"，并据此过滤候选模型：
//   - 简单请求（chat/翻译/摘要）不应路由到 frontier 模型（成本浪费）。
//   - 复杂请求（多步推理/长上下文/agent）不应路由到 easy 模型（质量不达标）。
//
// 难度来源（启发式，纯函数 of ClassificationSignals）：
//   - token 量（长上下文 → hard）
//   - 工具数（多工具 agent → hard）
//   - 任务类型（reasoning/code_audit/agent → hard；chat/creative → easy）
//   - code block / 多轮（中难度加成）
//
// 与 models_canonical.complexity_ceiling / min_complexity 配合：
//   过滤规则 = 任务难度档 > ceiling  → 剔除（模型能力不足）
//              任务难度档 < min    → 剔除（大材小用）
//
// 详见 docs/拆分/22-Auto智能路由与任务识别.md §22.9 M2。

// ComplexityLevel 是难度档，与 models_canonical.complexity_ceiling 同词汇表。
type ComplexityLevel string

const (
	ComplexityEasy     ComplexityLevel = "easy"
	ComplexityMedium   ComplexityLevel = "medium"
	ComplexityHard     ComplexityLevel = "hard"
	ComplexityFrontier ComplexityLevel = "frontier"
)

// complexityRank 用于比较（越大越难）。未识别返回 medium 的 rank。
var complexityRank = map[ComplexityLevel]int{
	ComplexityEasy:     1,
	ComplexityMedium:   2,
	ComplexityHard:     3,
	ComplexityFrontier: 4,
}

// ComplexityThresholds 是可调阈值。零值走 DefaultComplexityThresholds。
type ComplexityThresholds struct {
	// LongContextHardTokens：超过此 token 量 → 至少 hard。默认 50_000（与
	// HeuristicThresholds.LongContextTokens 一致）。
	LongContextHardTokens int
	// MediumTokens：超过此量 → 至少 medium。默认 8_000。
	MediumTokens int
	// AgentHardToolCount：工具数 >= 此值 → hard。默认 3（与 AgentToolThreshold 一致）。
	AgentHardToolCount int
}

// DefaultComplexityThresholds 返回生产默认值。
func DefaultComplexityThresholds() ComplexityThresholds {
	return ComplexityThresholds{
		LongContextHardTokens: 50_000,
		MediumTokens:          8_000,
		AgentHardToolCount:    3,
	}
}

// EstimateComplexity 返回请求的难度档。纯函数，无 I/O。
//
// 判定顺序（取最高命中，难者优先）：
//  1. 任务类型基线：reasoning/code_audit/agent → hard；long_context → hard；
//     vision → medium；function_call → medium；creative → easy；chat → easy。
//  2. token 量加成：> LongContextHardTokens → hard（不超 frontier，除非其它信号）；
//     > MediumTokens → 至少 medium。
//  3. 工具数加成：>= AgentHardToolCount → 至少 hard。
//  4. 多轮对话（MessageCount > 10）→ 至少 medium。
//  5. code block 存在 → 至少 medium。
//
// frontier 只在（reasoning/code_audit 且 long_context 且多工具）同时命中时给出，
// 避免轻易占用最贵资源。
func EstimateComplexity(sigs ClassificationSignals, task TaskType, t ComplexityThresholds) ComplexityLevel {
	if t.LongContextHardTokens == 0 {
		t = DefaultComplexityThresholds()
	}
	rank := complexityRank[baseComplexityForTask(task)]
	if rank == 0 {
		rank = complexityRank[ComplexityMedium]
	}
	level := rankToLevel(rank)

	// token 加成
	if sigs.EstimatedTokens > t.LongContextHardTokens {
		level = maxLevel(level, ComplexityHard)
	} else if sigs.EstimatedTokens > t.MediumTokens {
		level = maxLevel(level, ComplexityMedium)
	}

	// 工具数加成
	if sigs.ToolCount >= t.AgentHardToolCount {
		level = maxLevel(level, ComplexityHard)
	}

	// 多轮加成
	if sigs.MessageCount > 10 {
		level = maxLevel(level, ComplexityMedium)
	}

	// code block 加成
	if sigs.HasCodeBlock {
		level = maxLevel(level, ComplexityMedium)
	}

	// frontier 判定：reasoning / code_audit / planning + 长上下文 + 多工具 同时命中
	isHardTask := task == TaskReasoning || task == TaskCodeAudit || task == TaskPlanning
	if isHardTask && sigs.EstimatedTokens > t.LongContextHardTokens && sigs.ToolCount >= t.AgentHardToolCount {
		level = ComplexityFrontier
	}

	return level
}

// baseComplexityForTask 返回任务类型的基线难度。
func baseComplexityForTask(task TaskType) ComplexityLevel {
	switch task {
	case TaskReasoning, TaskCodeAudit, TaskPlanning, TaskAgent, TaskLongContext:
		return ComplexityHard
	case TaskVision, TaskFunctionCall, TaskIntentClassification:
		return ComplexityMedium
	case TaskCode:
		return ComplexityMedium
	case TaskCreative, TaskChat:
		return ComplexityEasy
	default:
		return ComplexityMedium
	}
}

// ComplexityMatch reports 是否允许把任务（reqLevel）路由到具有 ceiling/min 的模型。
//
//	ceiling=="" 或 min=="" → 不参与该方向过滤（向后兼容）。
//	任务难度 > ceiling → false（模型能力不足）。
//	任务难度 < min    → false（大材小用）。
//	否则 true。
func ComplexityMatch(reqLevel ComplexityLevel, ceiling, min string) bool {
	reqRank, ok := complexityRank[reqLevel]
	if !ok {
		reqRank = complexityRank[ComplexityMedium] // 未知难度按 medium 处理（保守）
	}
	if ceiling != "" {
		cRank, ok := complexityRank[ComplexityLevel(ceiling)]
		if ok && reqRank > cRank {
			return false // 任务比模型 ceiling 还难
		}
	}
	if min != "" {
		mRank, ok := complexityRank[ComplexityLevel(min)]
		if ok && reqRank < mRank {
			return false // 任务比模型 min 还简单
		}
	}
	return true
}

func rankToLevel(rank int) ComplexityLevel {
	switch {
	case rank <= 1:
		return ComplexityEasy
	case rank == 2:
		return ComplexityMedium
	case rank == 3:
		return ComplexityHard
	default:
		return ComplexityFrontier
	}
}

func maxLevel(a, b ComplexityLevel) ComplexityLevel {
	if complexityRank[a] >= complexityRank[b] {
		return a
	}
	return b
}
