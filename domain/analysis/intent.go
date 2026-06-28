// Package analysis 定义 V4 治理平台异步分析层与资产沉淀层共享的纯类型契约。
//
// 本包只放类型、常量、构造器与最朴素的 helpers；不持有任何外部依赖（无 DB、无
// HTTP、无 LLM 客户端），便于被同步治理层、异步分析 worker、资产沉淀 store 共同
// 引用而不产生循环依赖。
//
// 关联子目录（不在本包内）:
//   - domains/analysis/*   异步 worker 与事件总线实现
//   - domains/assets/*     资产沉淀 store 实现
package analysis

import "time"

// IntentKind 任务意图分类。
//
// 与现有 classification TaskType（chat/code/reasoning/...）正交：本枚举
// 强调"用户为什么要做这件事"，由 IntentAnalyzer 异步产出，写入资产库。
type IntentKind string

const (
	IntentChat         IntentKind = "chat"
	IntentCode         IntentKind = "code"
	IntentReasoning    IntentKind = "reasoning"
	IntentSummary      IntentKind = "summary"
	IntentTranslation  IntentKind = "translation"
	IntentExtraction   IntentKind = "extraction"
	IntentToolUse      IntentKind = "tool_use"
	IntentUnclassified IntentKind = "unclassified"
)

// IntentAnalysis 单请求/单提示词的意图分析结果。
//
// 由 IntentAnalyzer Worker 产出；同步治理层在读-through 资产库时也可参考。
type IntentAnalysis struct {
	RequestID  string
	SessionID  string
	TenantID   string
	Primary    IntentKind
	Secondary  []IntentKind
	Confidence float64
	Classifier string
	Signals    map[string]float64
	AnalyzedAt time.Time
}
