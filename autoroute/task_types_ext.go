// Package autoroute extensions for goal mode task types.
package autoroute

import "strings"

// Extended task types for goal mode operations.
const (
	// TaskCodeAudit covers code review, security analysis, and quality checks.
	TaskCodeAudit TaskType = "code_audit"
	// TaskIntentClassification covers intent detection and classification tasks.
	TaskIntentClassification TaskType = "intent_classification"
	// TaskPlanning covers writing plans, proposals, technical designs, and
	// task breakdown — high-intelligence work that does NOT itself contain code
	// to implement. Pure "先制定计划然后实现" requests stay TaskCode (detected
	// earlier by the coding strong-signal channel + patterns.go).
	TaskPlanning TaskType = "planning"
)

// codeAuditKeywords are deliberately *narrow* multi-word phrases. The earlier
// draft used bare "review"/"检查" which collided with TaskCode ("review my
// code", "检查这段代码"). These phrases only fire on an explicit audit ask.
var codeAuditKeywords = []string{
	"code audit", "代码审查", "代码审计",
	"security audit", "安全审计", "security review", "安全审查",
	"vulnerability scan", "漏洞扫描", "查找漏洞", "检查漏洞",
	"代码质量检查", "code quality review",
}

// IsCodeAuditRequest checks if a request is asking for an explicit code audit /
// security review / vulnerability scan. Returns false for ordinary "review my
// code" requests (those classify as TaskCode).
//
// 2026-09-14 匹配复审（gap_sysprompt_haiku）：审计短语优先只看 user 提示词；
// system 提示词里的短语仅在 user 同时提到代码对象时才采信——否则"你是代码
// 审查助手"这类角色设定会劫持 user 的任意请求（如写诗）。
func IsCodeAuditRequest(signals ClassificationSignals) bool {
	userLower := strings.ToLower(signals.LastUserPrompt)
	if containsAnyPhrase(userLower, codeAuditKeywords) {
		return true
	}
	sysLower := strings.ToLower(signals.SystemPrompt)
	return containsAnyPhrase(sysLower, codeAuditKeywords) && userMentionsCodeObject(userLower)
}

func containsAnyPhrase(haystack string, phrases []string) bool {
	for _, kw := range phrases {
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}

// userMentionsCodeObject reports whether the user prompt itself references a
// code artifact — required before a system-prompt audit phrase may count.
func userMentionsCodeObject(userLower string) bool {
	for _, obj := range []string{
		"代码", "code", "函数", "function", "接口", "api", "脚本", "script",
		"模块", "module", "commit", "pull request", " pr", "bug", "中间件", "middleware",
	} {
		if strings.Contains(userLower, obj) {
			return true
		}
	}
	return false
}

// intentClassificationKeywords target the *act* of classifying intents, not
// the word "intent" appearing incidentally. 紧邻短语（动词+目标连写）直接命中；
// 中文里"识别/判断 ... 的意图"常被"以下文本的"等词隔开，由
// hasIntentClassifySignal 做动词+目标组合判断（允许间隔）。
var intentClassificationKeywords = []string{
	// 中文紧邻短语
	"意图分类", "意图识别", "意图检测", "意图归类",
	"文本分类", "识别意图", "判断意图",
	// 中文情感/倾向二分类（2026-09-14 复审 gap_zh_sentiment 补充）
	"情感分类", "情感分析", "情感倾向", "正面还是负面", "是正面", "是负面",
	// 英文紧邻短语
	"intent classification", "intent detection",
	"classify intent", "classify the intent", "detect intent",
	"classify this", "text classification",
	"sentiment analysis", "sentiment classification", "positive or negative",
}

// intentClassifyVerbs / intentClassifyTargets 用于组合判断：当动词与目标
// 同时出现（允许间隔，如"识别以下文本的意图"）即判定为意图分类任务。
// 单独的"意图"或"识别"不触发（避免普通对话误判）。
var intentClassifyVerbs = []string{"识别", "判断", "分类", "归类", "辨别", "判定"}
var intentClassifyTargets = []string{"意图", "类别", "意图类别"}

// hasIntentClassifySignal 报告文本是否同时含一个分类动词和一个意图/类别目标。
func hasIntentClassifySignal(contentLower string) bool {
	hasVerb := false
	for _, v := range intentClassifyVerbs {
		if strings.Contains(contentLower, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	for _, tgt := range intentClassifyTargets {
		if strings.Contains(contentLower, tgt) {
			return true
		}
	}
	return false
}

// IsIntentClassificationRequest checks if a request is performing intent
// detection / text classification as the task itself (not just containing the
// word "intent" in passing).
func IsIntentClassificationRequest(signals ClassificationSignals) bool {
	contentLower := strings.ToLower(signals.SystemPrompt + " " + signals.LastUserPrompt)
	for _, kw := range intentClassificationKeywords {
		if strings.Contains(contentLower, kw) {
			return true
		}
	}
	// 组合判断：动词 + 目标同时出现（允许间隔），覆盖"识别以下文本的意图"。
	return hasIntentClassifySignal(contentLower)
}

// planningKeywords target plan/proposal/design/breakdown asks. "计划" alone is
// too broad (it appears in "完成这个计划" etc.), so phrases require a planning
// verb or a design-doc noun.
var planningKeywords = []string{
	// 中文：方案 / 规划 / 任务拆解
	"写一份方案", "写个方案", "设计方案", "制定方案",
	"技术方案", "架构方案", "实施方案",
	"写一份计划", "制定计划", "项目计划", "实施计划",
	"任务拆解", "拆解任务", "拆分任务", "任务划分", "工作分解",
	"写一份规划", "产品规划", "技术规划",
	"路线图", "技术路线",
	// English
	"technical design", "design doc", "design document",
	"write a plan", "create a plan", "project plan",
	"break down", "work breakdown", "task breakdown",
	"roadmap", "technical proposal", "write a proposal",
}

// planningVerbs / planningTargets 用于组合判断（2026-09-14 复审
// plan_zh_migration 补充）：紧邻短语覆盖不了"制定一个数据迁移方案"这类
// 动词与目标被修饰语隔开的写法，动词+目标同时出现即判定（与意图分类的
// 组合判断同型）。动词表刻意不含"做/写"这类泛动词，避免普通创作误判。
var planningVerbs = []string{"制定", "拟定", "拟一个", "起草", "设计", "规划"}
var planningTargets = []string{"方案", "计划", "规划", "路线图"}

// hasPlanDesignSignal 报告文本是否同时含一个规划动词和一个方案/计划目标。
func hasPlanDesignSignal(contentLower string) bool {
	hasVerb := false
	for _, v := range planningVerbs {
		if strings.Contains(contentLower, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	for _, tgt := range planningTargets {
		if strings.Contains(contentLower, tgt) {
			return true
		}
	}
	return false
}

// IsPlanningRequest checks if a request is asking for a plan / proposal /
// technical design / task breakdown — i.e. high-intelligence structured
// thinking rather than code implementation. Caller must still guard against
// the coding strong-signal channel so "先制定计划然后实现" stays TaskCode.
func IsPlanningRequest(signals ClassificationSignals) bool {
	contentLower := strings.ToLower(signals.SystemPrompt + " " + signals.LastUserPrompt)
	// "先制定…计划,然后(逐步)实现"是编程请求（plan-mode coding pattern），
	// 动宾组合不得把它抢成 planning——2026-09-14 复审回归教训。
	if looksLikePlanModeCoding(contentLower) {
		return false
	}
	for _, kw := range planningKeywords {
		if strings.Contains(contentLower, kw) {
			return true
		}
	}
	return hasPlanDesignSignal(contentLower)
}
