package settings

// 流式节点失败转移的词典与敏感词评分阈值设置键（Wave 3 任务 B5，2026-09-22）。
//
// B5① 重试/继续语义词典：domains/streaming/executors/node_failover.go 的
// LoadRetryKeywords 走 hotconfig（settings_kv 表 llmgw_% 前缀 30s 轮询）。
// 键名沿用 llmgw_continue_keywords / llmgw_retry_keywords（存储兼容，不迁移），
// 本文件只补 spec 让管理端可视化编辑。值形态是 JSON 数组的字符串化形式
// （hotconfig.GetString 只认 string，admin 保存 TypeString 原样落库）。
// 默认值在此与 executors 两处镜像 —— settings 不能反向 import domains，
// 改任何一侧必须同步另一侧（两侧测试互为锚点）。
//
// B5② 敏感词评分阈值：security/sensitive EvaluateSafety 的 block/warn
// 两档硬编码 0.6/0.3，提为可热更键（CachedPlatformFloat，≤5s 生效）。
const (
	KeyNodeFailoverContinueKeywords = "llmgw_continue_keywords"
	KeyNodeFailoverRetryKeywords    = "llmgw_retry_keywords"

	KeySensitiveBlockScore = "security.sensitive_block_score"
	KeySensitiveWarnScore  = "security.sensitive_warn_score"
)

// 词典默认值 = node_failover.go 内置默认 + Wave 3 补的 ja 骨架（zh/en 原词
// 逐字保留，行为只增不减）。
const (
	DefaultContinueKeywordsJSON = `["继续", "continue", "go", "come on", "请继续", "接着", "keep going", "继续回答", "接着说", "go on", "続けて", "続けてください", "このまま続けて", "続きを"]`
	DefaultRetryKeywordsJSON    = `["重试", "retry", "请重试", "再试一次", "try again", "重新回答", "再来", "再試行", "もう一度", "やり直してください", "retry please"]`
)

const (
	// DefaultSensitiveBlockScore 高危阻断阈值（原 EvaluateSafety 硬编码 0.6）。
	DefaultSensitiveBlockScore = 0.6
	// DefaultSensitiveWarnScore 中危告警阈值（原 EvaluateSafety 硬编码 0.3）。
	DefaultSensitiveWarnScore = 0.3
)

// NodeFailoverSpecs returns the continue/retry keyword dictionary specs.
func NodeFailoverSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeyNodeFailoverContinueKeywords,
			EnvName:         EnvNameAuto(KeyNodeFailoverContinueKeywords),
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryRouting,
			Default:         DefaultContinueKeywordsJSON,
			Description:     "流式“继续”判定词典",
			DescriptionLong: "流式节点失败转移时，尾部用户消息命中任一关键词且长度 ≤32 字符即视为“继续”类 nudge（删去上一轮问答让模型接着说）。JSON 数组的字符串形式，多语种（zh/en/ja）。热更经 hotconfig 30s 轮询生效。",
			Unit:            "个词",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             KeyNodeFailoverRetryKeywords,
			EnvName:         EnvNameAuto(KeyNodeFailoverRetryKeywords),
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryRouting,
			Default:         DefaultRetryKeywordsJSON,
			Description:     "流式“重试”判定词典",
			DescriptionLong: "流式节点失败转移时，尾部用户消息命中任一关键词即视为“重试”nudge（原样重发触发 failover）。JSON 数组的字符串形式，多语种（zh/en/ja）。热更经 hotconfig 30s 轮询生效。",
			Unit:            "个词",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}

// SensitiveSpecs returns the sensitive-word score threshold specs.
func SensitiveSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeySensitiveBlockScore,
			EnvName:         EnvNameAuto(KeySensitiveBlockScore),
			Type:            TypeFloat,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             floatPtr(0.1),
			Max:             floatPtr(1),
			Default:         DefaultSensitiveBlockScore,
			Description:     "敏感词高危阻断阈值",
			DescriptionLong: "EvaluateSafety 评分达到该值即 ActionBlock（P0 词每命中 +0.5、P1 +0.3、P2 +0.1，封顶 1.0）。原为 security/sensitive 硬编码 0.6。须大于告警阈值，读取处会自动钳制。",
			Unit:            "分",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             KeySensitiveWarnScore,
			EnvName:         EnvNameAuto(KeySensitiveWarnScore),
			Type:            TypeFloat,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             floatPtr(0.05),
			Max:             floatPtr(1),
			Default:         DefaultSensitiveWarnScore,
			Description:     "敏感词中危告警阈值",
			DescriptionLong: "EvaluateSafety 评分达到该值但未达阻断阈值时 ActionWarn。原为 security/sensitive 硬编码 0.3。达到阻断阈值时以阻断阈值为准。",
			Unit:            "分",
			DangerLevel:     Warning,
			HotReload:       true,
		},
	}
}

// CachedSensitiveScores resolves the block/warn thresholds through the
// ≤5s cache family with a defensive clamp keeping warn < block (the spec
// language cannot express cross-key constraints).
func CachedSensitiveScores() (blockScore, warnScore float64) {
	blockScore = CachedPlatformFloat(KeySensitiveBlockScore, DefaultSensitiveBlockScore)
	warnScore = CachedPlatformFloat(KeySensitiveWarnScore, DefaultSensitiveWarnScore)
	if warnScore >= blockScore {
		warnScore = blockScore - 0.01
	}
	if warnScore < 0 {
		warnScore = 0
	}
	return blockScore, warnScore
}
