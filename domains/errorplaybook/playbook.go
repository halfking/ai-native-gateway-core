// Package errorplaybook 实现「错误处理手册」（Error Handling Playbook）。
//
// ★ owner 需求（2026-11-20）：
//
//	「当系统发生错误时，如果是已经在错误处理手册中的错误类型，请使用预定的
//	 执行方案执行；当是未知错误时，PI 智能体需要进行 log 及上下文分析，找到
//	 问题的原因，给出执行方案，进行执行修正，然后将这个新的错误类型记录到
//	 错误处理手册中，并给出执行方案，供后面以标准模式执行。执行的方案可以是
//	 一段动态脚本或一段 API，或 AI 执行的提示词。」
//
// ★ 结构（与 PI 分析管线同族、但覆盖面是全系统错误）：
//
//	错误事件（candidate_failure_logs_hot 的 error_kind 窗口聚合 / 管理面手动注入）
//	  → 匹配手册（error_playbook_entries）
//	    · 命中 ⇒ 标准模式：按条目 auto_execute 决定自动执行或记 awaiting_approval
//	    · 未命中 ⇒ PI 智能体 log+上下文分析（AnalyzeUnknown）
//	        → 根因 + 方案（白名单校验 + 置信度门 0.80）
//	        → 执行一次修正（变更类方案需要过置信度门；只读方案直接执行）
//	        → 落一条新手册条目（source='pi_analysis'）供后续标准模式执行
//	  → 自动完善：命中/执行计数自增；标准执行失败的条目带失败上下文重新提炼
//	    （source='refinement'，refinement_count++）。
//
// ★ 安全设计（与 PI 一键应用修复同款三件套，全部在后端强制）：
//
//	① 白名单动作：方案里的每一步只能调用动作注册表里的闭集原语
//	   （record_note / set_settings_kv / disable_pi_rule / call_admin_api）；
//	   set_settings_kv 只允许写代码级 allowlist 里的键；call_admin_api 只允许
//	   调登记在册的内部端点。任意 shell / 任意 SQL 被结构性排除。
//	② 置信度门：PI 分析产出的**变更类**方案（写设置 / 禁规则 / 调管理面
//	   非只读端点）必须 confidence ≥ 0.80 才允许自动执行一次；否则只落手册
//	   条目（view-only），执行动作转为 awaiting_approval。
//	③ 旧值快照 + 可回滚：每个有副作用的步骤执行前把旧值快照进执行留痕
//	   （error_playbook_executions.steps）⇒ 管理面可按快照逐条回滚。
package errorplaybook

import (
	"encoding/json"
	"time"
)

// PlanKind 是方案形态的闭集（迁移 995 的 CHECK 同口径）。
const (
	PlanKindScript = "script" // 一段动作步骤脚本（白名单原语的有序步骤）
	PlanKindAPI    = "api"    // 一次管理面 API 调用
	PlanKindPrompt = "prompt" // 一段 AI 执行提示词（交给 PI 智能体/运维）
)

// Entry 来源闭集（迁移 995 的 CHECK 同口径）。
const (
	SourceSeed       = "seed"        // 迁移种子（历史事故沉淀的标准处置）
	SourceManual     = "manual"      // 管理面手工录入
	SourcePIAnalysis = "pi_analysis" // PI 智能体分析未知错误后落库
	SourceRefinement = "refinement"  // 执行失败后的自动提炼
)

// 执行状态闭集（迁移 995 的 CHECK 同口径）。
const (
	StatusSucceeded        = "succeeded"
	StatusFailed           = "failed"
	StatusSkipped          = "skipped"
	StatusAwaitingApproval = "awaiting_approval"
	StatusNoAction         = "no_action"
)

// 执行触发来源闭集（迁移 995 的 CHECK 同口径）。
const (
	TriggerStandard    = "standard"     // 标准模式：手册命中后按预定方案执行
	TriggerAutoAnalyze = "auto_analyze" // 未知错误首次分析后的执行修正
	TriggerManual      = "manual"       // 管理面手动执行
	TriggerRefinement  = "refinement"   // 自动提炼后的执行
)

// PlanStep 是 script 方案里的单个动作步骤。
type PlanStep struct {
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params,omitempty"`
	Note   string                 `json:"note,omitempty"`
}

// Plan 是归一化后的预定执行方案（plan_payload 列的形状）。
//
//	★ 三种形态共用一个结构：script 用 Steps，api 归一化成单个
//	call_admin_api 步骤（执行面统一走动作注册表），prompt 用 Prompt。
type Plan struct {
	Kind      string          `json:"kind"`
	Steps     []PlanStep      `json:"steps,omitempty"`
	Method    string          `json:"method,omitempty"`     // api 形态的展示字段（归一化后同时进 steps）
	Path      string          `json:"path,omitempty"`       // api
	Body      json.RawMessage `json:"body,omitempty"`       // api
	TimeoutMS int             `json:"timeout_ms,omitempty"` // api
	Prompt    string          `json:"prompt,omitempty"`     // prompt
	Expect    string          `json:"expect,omitempty"`     // prompt: analysis | proposal
}

// Mutating 判断方案是否包含**变更类**动作（置信度门的判据）。
//
//	非变更方案（record_note / 只读 api / prompt 分析）不会改变生产状态
//	⇒ 不需要过 0.80 置信度门。
func (p Plan) Mutating() bool {
	switch p.Kind {
	case PlanKindPrompt:
		return false
	case PlanKindAPI:
		return !isReadOnlyMethod(p.Method)
	default:
		for _, s := range p.Steps {
			if isMutatingAction(s.Action, s.Params) {
				return true
			}
		}
		return false
	}
}

func isReadOnlyMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// isMutatingAction 判定单个动作是否变更类。
//
//	★ 与动作注册表（actions.go）的闭集对表：新增变更类原语必须同步到这里，
//	否则置信度门会漏放。用 allowlist 而不是 denylist：未知的动作名在
//	NormalizePlan 就被拒绝，这里是第二道闸。
func isMutatingAction(action string, params map[string]interface{}) bool {
	switch action {
	case ActionSetSettingsKV, ActionDisablePIRule:
		return true
	case ActionCallAdminAPI:
		method, _ := params["method"].(string)
		return !isReadOnlyMethod(method)
	}
	return false
}

// Entry 是手册条目（error_playbook_entries 一行）。
type Entry struct {
	ID              int64      `json:"id"`
	ErrorType       string     `json:"error_type"`
	MatchPattern    string     `json:"match_pattern"` // "" = 类型级条目
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	RootCause       string     `json:"root_cause"`
	PlanKind        string     `json:"plan_kind"`
	Plan            Plan       `json:"plan"`
	Confidence      float64    `json:"confidence"`
	Source          string     `json:"source"`
	SourceQueueID   int64      `json:"source_queue_id"` // 0 = 无
	Enabled         bool       `json:"enabled"`
	AutoExecute     bool       `json:"auto_execute"`
	HitCount        int64      `json:"hit_count"`
	ExecutionCount  int64      `json:"execution_count"`
	SuccessCount    int64      `json:"success_count"`
	FailureCount    int64      `json:"failure_count"`
	LastHitAt       *time.Time `json:"last_hit_at"`
	LastExecutedAt  *time.Time `json:"last_executed_at"`
	LastStatus      string     `json:"last_status"`
	RefinementCount int        `json:"refinement_count"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Event 是一次错误事件（窗口聚合后的形态）。
type Event struct {
	ErrorType   string    `json:"error_type"` // errorsx kind / PI 队列类型 / 自定义
	Message     string    `json:"message"`    // 最近一条错误消息样本（匹配 match_pattern 用）
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	TenantID    string    `json:"tenant_id,omitempty"`
	Occurrences int       `json:"occurrences"`
	FirstSeen   time.Time `json:"first_seen,omitempty"`
	LastSeen    time.Time `json:"last_seen,omitempty"`
	Samples     []string  `json:"samples,omitempty"`
}

// Execution 是一次执行留痕（error_playbook_executions 一行）。
type Execution struct {
	ID            int64           `json:"id"`
	EntryID       int64           `json:"entry_id"` // 0 = 无（未知错误分析等）
	ErrorType     string          `json:"error_type"`
	MatchPattern  string          `json:"match_pattern"`
	TriggerSource string          `json:"trigger_source"`
	Context       json.RawMessage `json:"context,omitempty"`
	Status        string          `json:"status"`
	Steps         json.RawMessage `json:"steps,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	DurationMS    int             `json:"duration_ms"`
	ExecutedBy    string          `json:"executed_by"`
	CreatedAt     time.Time       `json:"created_at"`
}

// StepLog 是执行留痕里的单步记录（steps JSONB 数组的元素）。
type StepLog struct {
	Step     int             `json:"step"`
	Action   string          `json:"action"`
	Status   string          `json:"status"` // ok | failed | skipped
	Result   string          `json:"result,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"` // 旧值快照（revert 的依据）
	Error    string          `json:"error,omitempty"`
}

// jsonb 是可空 JSONB 列的 Scan/Marshal 适配器。
//
//	★ 与 admin.NullableJSON 同款纪律：Go 1.27 的 json.RawMessage 不能 Scan
//	NULL，而真实数据里 JSONB 列就是 NULL ⇒ 直接 Scan 会 500。
type jsonb []byte

func (n *jsonb) Scan(src interface{}) error {
	if src == nil {
		*n = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*n = append((*n)[:0], v...)
		return nil
	case string:
		*n = append((*n)[:0], v...)
		return nil
	}
	return ErrStoreScan
}

func (n jsonb) Value() (interface{}, error) {
	if len(n) == 0 {
		return nil, nil
	}
	return []byte(n), nil
}
