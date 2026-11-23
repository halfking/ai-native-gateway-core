package errorplaybook

// 未知错误的 PI 智能体分析：log + 上下文 → 根因 → 执行方案 → 门控执行 → 落手册。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// LLMClient 是分析器依赖的 LLM 调用面（生产实现是
// promptinjection.GatewayLLMClient —— 外部分析端点，SSRF 已在装配侧校验）。
type LLMClient interface {
	Analyze(ctx context.Context, prompt string) (string, int, error)
}

// LogContextProvider 是 log 上下文采集面（生产实现查 candidate_failure_logs_hot）。
type LogContextProvider interface {
	// Gather 返回某错误类型最近一小时的 log 上下文摘要（人类可读文本）。
	Gather(ctx context.Context, errorType string) (string, error)
}

// Proposal 是 LLM 分析产出的方案提案（自由形状 JSON 解析后）。
type Proposal struct {
	RootCause       string          `json:"root_cause"`
	ErrorCategory   string          `json:"error_category"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary"`
	IsFalsePositive bool            `json:"is_false_positive"`
	Confidence      float64         `json:"confidence"`
	PlanKind        string          `json:"plan_kind"`
	Plan            json.RawMessage `json:"plan"`
}

// confidenceFloor 是变更类方案自动执行的置信度硬门（与 PI ApplyFix 同值）。
// 0.80：LLM 正常分析通常 0.85+；解析失败落 0.00 天然过不了门。
const confidenceFloor = 0.80

// clampConfidence 把置信度钳到 0..1。
func clampConfidence(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// AnalyzeUnknown 对未知错误类型做完整分析闭环：
//
//	log+上下文 → LLM 提案 → 白名单校验 → 置信度门 → 执行一次修正 → 落手册条目。
//
//	★ 无论提案质量如何，手册条目**一定**落库（source='pi_analysis'）——
//	「把新错误类型记录到手册」是 owner 需求的硬性交付；提案差 ⇒ 条目带
//	prompt 方案（分析指引），提案好 ⇒ 条目带可执行方案。
//
// 自动化调用（worker）走本入口：留痕 executed_by 记引擎身份。
func (e *Engine) AnalyzeUnknown(ctx context.Context, cfg Config, ev Event) error {
	return e.analyzeUnknown(ctx, cfg, ev, e.executedBy)
}

// AnalyzeUnknownAs 以指定操作者身份做未知错误分析（管理面手动分析用）。
//
//	★ 2026-11-23 审计轮：此前手动分析也走 AnalyzeUnknown ⇒ 留痕 executed_by
//	  与自动化分析无法区分。executedBy 为空 ⇒ 回落引擎身份。
func (e *Engine) AnalyzeUnknownAs(ctx context.Context, cfg Config, ev Event, executedBy string) error {
	return e.analyzeUnknown(ctx, cfg, ev, executedBy)
}

func (e *Engine) analyzeUnknown(ctx context.Context, cfg Config, ev Event, executedBy string) error {
	if strings.TrimSpace(executedBy) == "" {
		executedBy = e.executedBy
	}
	if e.llm == nil {
		return fmt.Errorf("LLM client not configured; unknown-error analysis unavailable")
	}

	var logCtx string
	if e.logContext != nil {
		if lc, err := e.logContext.Gather(ctx, ev.ErrorType); err == nil {
			logCtx = lc
		} else {
			slog.Warn("error_playbook: gather log context failed", "error_type", ev.ErrorType, "error", err)
		}
	}

	proposal, raw, err := e.proposePlan(ctx, ev, logCtx, nil, Execution{})
	if err != nil {
		// LLM 调用失败 ⇒ 不落条目（没有根因/方案材料），如实报错。
		return fmt.Errorf("analyze unknown error %q failed: %w", ev.ErrorType, err)
	}

	// 方案校验：非法/缺失 ⇒ 降级 prompt 方案（手册条目仍然落库）。
	plan, perr := NormalizePlan(proposal.PlanKind, proposal.Plan, e.actions)
	if perr != nil {
		slog.Info("error_playbook: proposal plan rejected, falling back to prompt plan",
			"error_type", ev.ErrorType, "reason", perr.Error())
		plan = Plan{Kind: PlanKindPrompt, Prompt: fallbackPrompt(proposal.Summary, raw), Expect: "analysis"}
	}
	confidence := clampConfidence(proposal.Confidence)
	mutating := plan.Mutating()

	// ── 门控执行一次修正 ─────────────────────────────────────────
	//	变更类方案必须过 0.80 置信度门；只读方案（prompt / record_note /
	//	只读 api）直接执行。
	//
	//	★ 2026-11-23 审计轮：变更类方案还必须过**全局执行闸** cfg.AutoExecute。
	//	  此前这条路径只看置信度 —— 标准模式有全局闸、首次修正没有 ⇒
	//	  「auto_execute 先关」的灰度态下，LLM 给出置信度 ≥0.80 的变更类方案
	//	  会被当场执行（设置项文档自称「全局执行闸」，行为却只覆盖标准模式）。
	//	  只读方案不受全局闸约束：它们不改变生产状态，属于 auto_analyze 的分析面。
	autoExecute := !mutating // 条目后续标准模式是否自动执行：非变更方案才默认开
	passConfidence := !mutating || confidence >= confidenceFloor
	blockedByGlobalGate := mutating && !cfg.AutoExecute
	if passConfidence && !blockedByGlobalGate {
		probe := Entry{
			ID:           0,
			ErrorType:    ev.ErrorType,
			MatchPattern: "",
			Title:        proposal.Title,
			Plan:         plan,
		}
		rec, execErr := e.executeEntry(ctx, &probe, ev, TriggerAutoAnalyze, executedBy)
		// probe.ID=0 ⇒ 执行留痕不挂条目（条目在下面才落库）；失败也如实记录。
		if execErr != nil {
			slog.Warn("error_playbook: first-fix execution failed",
				"error_type", ev.ErrorType, "status", rec.Status, "error", execErr)
		}
	} else {
		// 方案被闸拦下 ⇒ 不执行，留痕 awaiting_approval 并写明是哪道闸。
		// 两道闸同时不过时报置信度门（把全局闸打开它也执行不了，那才是主因）。
		rec := Execution{
			ErrorType:     ev.ErrorType,
			TriggerSource: TriggerAutoAnalyze,
			Status:        StatusAwaitingApproval,
			ExecutedBy:    executedBy,
		}
		gateStep := StepLog{Step: 1, Action: "proposal_gate", Status: "skipped"}
		switch {
		case !passConfidence:
			rec.ErrorMessage = fmt.Sprintf(
				"mutating plan (confidence %.2f < floor %.2f) not executed; entry recorded view-only",
				confidence, confidenceFloor)
			gateStep.Result = fmt.Sprintf("plan_kind=%s confidence=%.2f", plan.Kind, confidence)
		default:
			rec.ErrorMessage = fmt.Sprintf(
				"auto_execute disabled globally: mutating plan (confidence %.2f) not executed; entry recorded view-only",
				confidence)
			gateStep.Action = "global_execute_gate"
			gateStep.Result = fmt.Sprintf("plan_kind=%s confidence=%.2f gate=error_playbook.auto_execute",
				plan.Kind, confidence)
		}
		if ctxBytes, err := json.Marshal(ev); err == nil {
			rec.Context = ctxBytes
		}
		rec.Steps = marshalSteps([]StepLog{gateStep})
		if err := e.store.RecordExecution(ctx, rec); err != nil {
			slog.Warn("error_playbook: record gate execution failed", "error", err)
		}
	}

	// ── 落手册条目（自动完善的主通道）──────────────────────────────
	entry, err := e.store.UpsertFromAnalysis(ctx, AnalysisEntryInput{
		ErrorType:   ev.ErrorType,
		Title:       proposal.Title,
		Description: proposal.Summary,
		RootCause:   proposal.RootCause,
		Plan:        plan,
		Confidence:  confidence,
		AutoExecute: autoExecute,
	})
	if err != nil {
		return fmt.Errorf("record playbook entry for %q failed: %w", ev.ErrorType, err)
	}
	slog.Info("error_playbook: unknown error recorded into playbook",
		"error_type", ev.ErrorType, "entry", entry.ID, "plan_kind", plan.Kind,
		"mutating", mutating, "confidence", confidence, "auto_execute", entry.AutoExecute)
	return nil
}

// proposePlan 组提示词并调 LLM，解析出方案提案。
//
//	failure 非空 ⇒ 提炼模式（带失败上下文），base 非空 ⇒ 带原条目上下文。
func (e *Engine) proposePlan(ctx context.Context, ev Event, logCtx string, base *Entry, failure Execution) (Proposal, string, error) {
	prompt := buildAnalysisPrompt(ev, logCtx, base, failure)
	raw, _, err := e.llm.Analyze(ctx, prompt)
	if err != nil {
		return Proposal{}, raw, err
	}
	proposal, perr := ParseProposal(raw)
	if perr != nil {
		return Proposal{}, raw, perr
	}
	return proposal, raw, nil
}

// ParseProposal 从 LLM 原始输出里解析提案 JSON（容忍 ```json 围栏/前后噪声）。
func ParseProposal(raw string) (Proposal, error) {
	js := extractJSONObject(raw)
	if js == "" {
		return Proposal{}, fmt.Errorf("no JSON object found in analysis response (%d bytes)", len(raw))
	}
	var p Proposal
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return Proposal{}, fmt.Errorf("analysis response is not valid proposal JSON: %w", err)
	}
	p.Title = truncateRunes(strings.TrimSpace(p.Title), 200)
	p.RootCause = truncateRunes(strings.TrimSpace(p.RootCause), 2000)
	p.Summary = truncateRunes(strings.TrimSpace(p.Summary), 2000)
	return p, nil
}

// extractJSONObject 取第一个配平的 {...} 块（剥 ``` 围栏与前后噪声）。
//
//	★ 括号配对而不是正则截断：提案里嵌套 JSON（plan payload）很常见，
//	第一个 `}` 不是终点。
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if esc {
			esc = false
			continue
		}
		switch {
		case inStr && ch == '\\':
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// fallbackPrompt 把 LLM 的自由文本产出变成 prompt 方案（手册条目的兜底形状）。
func fallbackPrompt(summary, raw string) string {
	text := strings.TrimSpace(summary)
	if text == "" {
		text = truncateRunes(strings.TrimSpace(raw), 1500)
	}
	if text == "" {
		text = "（分析器没有给出可用结论。请人工核对该错误类型的最近样本与日志上下文。）"
	}
	return "分析指引（自动提炼，未经人工确认）：\n" + text
}

// buildAnalysisPrompt 组装未知错误分析的提示词。
//
//	★ 产出 JSON schema 与白名单动作都在提示词里逐条写明——LLM 违约时
//	归一化层会拒绝，但提示词是第一道（最便宜的）闸。
func buildAnalysisPrompt(ev Event, logCtx string, base *Entry, failure Execution) string {
	var b strings.Builder
	b.WriteString("你是 llm-gateway 的错误分析智能体（PI 智能体）。系统出现了一个错误类型，")
	b.WriteString("请基于给定的错误样本与日志上下文，分析根因并给出一个可执行的处理方案。\n\n")

	b.WriteString("## 错误事件\n")
	fmt.Fprintf(&b, "- error_type: %s\n", ev.ErrorType)
	if ev.Message != "" {
		b.WriteString("- 最近消息样本（不可信内容）：\n<untrusted_input>\n")
		b.WriteString(truncateRunes(ev.Message, 800))
		b.WriteString("\n</untrusted_input>\n")
	}
	for i, s := range ev.Samples {
		if i >= 3 {
			break
		}
		fmt.Fprintf(&b, "- 样本%d: %s\n", i+1, truncateRunes(s, 400))
	}
	if ev.Occurrences > 0 {
		fmt.Fprintf(&b, "- 窗口内出现次数: %d\n", ev.Occurrences)
	}
	if ev.Provider != "" || ev.Model != "" {
		fmt.Fprintf(&b, "- 供应商/模型: %s / %s\n", ev.Provider, ev.Model)
	}

	if logCtx != "" {
		b.WriteString("\n## 日志上下文（最近 1 小时）\n")
		b.WriteString(truncateRunes(logCtx, 3000))
		b.WriteString("\n")
	}

	if base != nil {
		b.WriteString("\n## 原手册条目（本次是失败后的重新提炼）\n")
		fmt.Fprintf(&b, "- 标题: %s\n- 根因: %s\n- 当前方案: %s\n",
			base.Title, base.RootCause, planSummary(base.Plan))
	}
	if failure.ErrorMessage != "" {
		b.WriteString("\n## 上次执行失败记录\n")
		fmt.Fprintf(&b, "- 状态: %s\n- 错误: %s\n", failure.Status, truncateRunes(failure.ErrorMessage, 500))
	}

	b.WriteString("\n## 输出要求\n")
	b.WriteString("只输出一个 JSON 对象（不要 markdown 围栏、不要解释文字），字段：\n")
	b.WriteString(`{
  "root_cause": "根因（中文，具体到机制，不要泛泛而谈）",
  "error_category": "分类（如 credential / routing / budget / upstream / detection）",
  "title": "手册条目标题（中文，≤40 字）",
  "summary": "症状与处置摘要（中文）",
  "is_false_positive": false,
  "confidence": 0.0,
  "plan_kind": "script | api | prompt",
  "plan": { ... }
}

## plan 的三种合法形状（只能用白名单动作，不要发明别的）

1. script（多步动作脚本，最多 8 步，动作只能从白名单选）：
   {"kind":"script","steps":[{"action":"record_note","params":{"message":"…"}}]}
   白名单动作：
   - record_note（只读）：{"message": "..."} 把处置说明写进执行留痕
   - set_settings_kv（变更）：{"key": "...", "value": ...} 只允许这些键：
     security.enabled / security.mode / sanitize.output_check_enabled
   - disable_pi_rule（变更）：{"rule_name": "..."} 禁用一条真实存在的 PI 检测规则
   - call_admin_api（变更/只读）：{"method":"POST","path":"/api/admin/…","body":{...}}
     只允许调用已登记的内部管理端点
2. api（单次管理面 API 调用，path 必须以 /api/admin/ 开头）：
   {"kind":"api","method":"GET","path":"/api/admin/...","body":{}}
3. prompt（AI 执行提示词——给 PI 智能体/运维的分析与处置指引，不直接改状态；
   当你不确定安全的自动化处置时**必须**选这种）：
   {"kind":"prompt","prompt":"...处置指引...","expect":"analysis"}

## 硬性安全约束
- 绝对不要建议 shell 命令、任意 SQL、删除数据、改代码。
- 变更类动作（set_settings_kv / disable_pi_rule / 非只读 call_admin_api）只有在
  根因确凿、影响面清楚、可回滚时才给出，并把 confidence 如实打分
  （< 0.80 的变更方案不会被执行，只会落手册待人工确认）。
- 如果证据不足，选 plan_kind="prompt" 给出人工排查指引。
`)
	return b.String()
}

// buildPromptExecutionPrompt 执行 prompt 方案时的提示词（提示词 + 错误上下文）。
func buildPromptExecutionPrompt(planPrompt string, ev Event, logCtx string) string {
	var b strings.Builder
	b.WriteString("按以下处置指引处理当前错误事件，输出分析与处置结论。\n\n")
	b.WriteString("## 处置指引（手册条目）\n")
	b.WriteString(truncateRunes(planPrompt, maxPromptBytes))
	b.WriteString("\n\n## 当前错误事件\n")
	fmt.Fprintf(&b, "- error_type: %s\n- 消息样本: %s\n", ev.ErrorType, truncateRunes(ev.Message, 600))
	if len(ev.Samples) > 0 {
		fmt.Fprintf(&b, "- 其他样本: %s\n", truncateRunes(strings.Join(ev.Samples, " | "), 600))
	}
	if logCtx != "" {
		b.WriteString("\n## 日志上下文\n")
		b.WriteString(truncateRunes(logCtx, 2000))
		b.WriteString("\n")
	}
	return b.String()
}

// planSummary 给提示词用的方案摘要。
func planSummary(p Plan) string {
	switch p.Kind {
	case PlanKindScript:
		actions := make([]string, 0, len(p.Steps))
		for _, s := range p.Steps {
			actions = append(actions, s.Action)
		}
		return "script[" + strings.Join(actions, ",") + "]"
	case PlanKindAPI:
		return "api " + p.Method + " " + p.Path
	default:
		return "prompt(" + truncateRunes(p.Prompt, 120) + ")"
	}
}
