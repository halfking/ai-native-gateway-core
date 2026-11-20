package errorplaybook

// 引擎：匹配 → 标准执行 / 未知错误 PI 分析 → 落手册 → 自动完善。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Engine 是错误处理手册的运行引擎。
type Engine struct {
	store      EntryStore
	actions    ActionRegistry
	llm        LLMClient
	logContext LogContextProvider
	nowFn      func() time.Time

	// executedBy 是执行留痕的 executed_by 标识（自动执行时 'error_playbook'）。
	executedBy string

	// pending 是未知错误类型的累计窗口（in-memory；重启即清零，
	// 重新累计 —— 阈值判定的语义是「持续出现」而非「历史总量」）。
	mu           sync.Mutex
	pending      map[string]*pendingKind
	lastAnalyzed map[string]time.Time
	lastRefined  map[int64]time.Time
	lastRun      time.Time
}

// Config 是引擎/worker 的运行配置（由装配侧从 settings/env 读出注入）。
type Config struct {
	// Enabled 是自动化总闸（worker 循环是否处理错误窗口）。
	Enabled bool
	// AutoAnalyze 未知错误是否触发 PI 智能体分析。
	AutoAnalyze bool
	// AutoExecute 标准模式下命中条目是否允许自动执行（仍需条目自身的
	// auto_execute=TRUE 与冷却期）。
	AutoExecute bool
	// KindThreshold 未知错误累计出现多少次才触发分析（防抖 + 省钱）。
	KindThreshold int
	// ExecutionCooldown 同一条目两次自动执行的最小间隔。
	ExecutionCooldown time.Duration
	// RefineCooldown 执行失败的条目距上次失败多久才允许重新提炼。
	RefineCooldown time.Duration
	// AnalyzeDedup 同一错误类型两次分析的最小间隔。
	AnalyzeDedup time.Duration
	// MaxEventsPerRound 单轮 worker 最多处理的错误类型数。
	MaxEventsPerRound int
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		Enabled:           false,
		AutoAnalyze:       true,
		AutoExecute:       true,
		KindThreshold:     3,
		ExecutionCooldown: 30 * time.Minute,
		RefineCooldown:    2 * time.Hour,
		AnalyzeDedup:      24 * time.Hour,
		MaxEventsPerRound: 10,
	}
}

// pendingKind 是一个未知错误类型的累计状态。
type pendingKind struct {
	count    int
	firstSee time.Time
	last     Event
}

// NewEngine 创建引擎。llm / logContext 可为 nil（此时未知错误分析不可用，
// 标准匹配与执行不受影响）。
func NewEngine(store EntryStore, actions ActionRegistry, llm LLMClient, logContext LogContextProvider) *Engine {
	return &Engine{
		store:        store,
		actions:      actions,
		llm:          llm,
		logContext:   logContext,
		nowFn:        time.Now,
		executedBy:   "error_playbook",
		pending:      map[string]*pendingKind{},
		lastAnalyzed: map[string]time.Time{},
		lastRefined:  map[int64]time.Time{},
	}
}

// HandleEvent 处理一个错误事件：命中手册 ⇒ 标准模式；未命中 ⇒ 未知错误路径。
//
//	★ 幂等与限频都在本层：命中计数永远 +1（可观测），执行受 autoExecute
//	（全局）× auto_execute（条目）× 冷却期三重门。
func (e *Engine) HandleEvent(ctx context.Context, cfg Config, ev Event) HandleResult {
	res := HandleResult{ErrorType: ev.ErrorType}

	entries, err := e.store.ListEnabled(ctx)
	if err != nil {
		res.Err = err
		return res
	}

	entry, _ := PickEntry(entries, ev)
	if entry != nil {
		res.Matched = true
		res.EntryID = entry.ID
		if err := e.store.RecordHit(ctx, entry.ID); err != nil {
			slog.Warn("error_playbook: record hit failed", "entry", entry.ID, "error", err)
		}
		if !cfg.Enabled || !cfg.AutoExecute || !entry.AutoExecute {
			// 条目命中但不允许自动执行 ⇒ 记 awaiting_approval（管理面可见）。
			rec := e.newExecution(entry, ev, TriggerStandard, StatusAwaitingApproval)
			rec.ErrorMessage = approvalReason(cfg, entry)
			res.ExecutionStatus = rec.Status
			if err := e.store.RecordExecution(ctx, rec); err != nil {
				res.Err = err
			}
			return res
		}
		if !cooldownElapsed(entry.LastExecutedAt, cfg.ExecutionCooldown, e.nowFn()) {
			rec := e.newExecution(entry, ev, TriggerStandard, StatusSkipped)
			rec.ErrorMessage = fmt.Sprintf("execution cooldown: last executed %.0f min ago (cooldown %.0f min)",
				minutesSince(entry.LastExecutedAt, e.nowFn()), cfg.ExecutionCooldown.Minutes())
			res.ExecutionStatus = rec.Status
			if err := e.store.RecordExecution(ctx, rec); err != nil {
				res.Err = err
			}
			return res
		}
		rec, execErr := e.ExecuteEntry(ctx, entry, ev, TriggerStandard)
		res.ExecutionStatus = rec.Status
		res.Err = execErr
		return res
	}

	// ── 未知错误路径 ──────────────────────────────────────────────
	res.Matched = false
	e.mu.Lock()
	p := e.pending[ev.ErrorType]
	if p == nil {
		p = &pendingKind{firstSee: e.nowFn()}
		e.pending[ev.ErrorType] = p
	}
	p.count += maxInt(ev.Occurrences, 1)
	p.last = ev
	count := p.count
	e.mu.Unlock()

	if !cfg.Enabled || !cfg.AutoAnalyze || e.llm == nil {
		return res
	}
	if count < maxInt(cfg.KindThreshold, 1) {
		return res
	}
	e.mu.Lock()
	lastAt := e.lastAnalyzed[ev.ErrorType]
	e.mu.Unlock()
	if !dedupElapsed(lastAt, cfg.AnalyzeDedup, e.nowFn()) {
		res.AnalyzeSkipped = "analyze dedup window"
		return res
	}

	// 分析（会落手册条目 + 按门执行一次修正）。
	if err := e.AnalyzeUnknown(ctx, cfg, ev); err != nil {
		res.Err = err
		return res
	}
	res.Analyzed = true
	e.mu.Lock()
	e.lastAnalyzed[ev.ErrorType] = e.nowFn()
	delete(e.pending, ev.ErrorType)
	e.mu.Unlock()
	return res
}

// HandleResult 是 HandleEvent 的结果摘要。
type HandleResult struct {
	ErrorType       string
	Matched         bool
	EntryID         int64
	ExecutionStatus string
	Analyzed        bool
	AnalyzeSkipped  string
	Err             error
}

func approvalReason(cfg Config, entry *Entry) string {
	if !cfg.Enabled {
		return "automation disabled (error_playbook.enabled=false): matched entry awaits approval"
	}
	if !cfg.AutoExecute {
		return "auto_execute disabled globally: matched entry awaits approval"
	}
	return "entry auto_execute is off: matched entry awaits approval (flip it on the entry to run in standard mode)"
}

func (e *Engine) newExecution(entry *Entry, ev Event, trigger, status string) Execution {
	rec := Execution{
		ErrorType:     ev.ErrorType,
		TriggerSource: trigger,
		Status:        status,
		ExecutedBy:    e.executedBy,
	}
	if entry != nil {
		rec.EntryID = entry.ID
		rec.MatchPattern = entry.MatchPattern
	}
	if ctxBytes, err := json.Marshal(ev); err == nil {
		rec.Context = ctxBytes
	}
	return rec
}

// PickEntry 从启用条目里选出本次事件的匹配条目。
//
//	★ 优先级：正文模式命中 > 类型级条目 > 未命中；同级里 confidence 高者、
//	再 id 小者（稳定）。
func PickEntry(entries []Entry, ev Event) (*Entry, bool) {
	var best *Entry
	bestPatternMatched := false
	for i := range entries {
		en := &entries[i]
		if en.ErrorType != ev.ErrorType {
			continue
		}
		patternMatched := false
		if p := en.MatchPattern; p != "" {
			re, err := CompilePattern(p)
			if err != nil || re == nil {
				continue // 非法正则的条目不参与匹配（不 panic、不误匹配）
			}
			if !re.MatchString(ev.Message) {
				continue
			}
			patternMatched = true
		}
		if best == nil {
			best, bestPatternMatched = en, patternMatched
			continue
		}
		if patternMatched != bestPatternMatched {
			if patternMatched {
				best, bestPatternMatched = en, true
			}
			continue
		}
		if en.Confidence > best.Confidence || (en.Confidence == best.Confidence && en.ID < best.ID) {
			best = en
		}
	}
	return best, best != nil
}

// ExecuteEntry 执行一个条目的预定方案（三种 kind 统一入口）。
//
//	★ prompt 方案 = 调 LLM 生成分析（记录输出，不改状态）；
//	★ script/api 方案 = 逐步骤过动作注册表（白名单 + 快照 + 可回滚）。
//	返回执行留痕（已写入 store）与业务错误（步骤失败时 rec.Status=failed）。
func (e *Engine) ExecuteEntry(ctx context.Context, entry *Entry, ev Event, trigger string) (Execution, error) {
	started := e.nowFn()
	rec := e.newExecution(entry, ev, trigger, "")
	rec.ExecutedBy = e.executedBy

	switch entry.Plan.Kind {
	case PlanKindPrompt:
		out, _, err := e.executePromptPlan(ctx, entry, ev)
		steps := []StepLog{{
			Step:   1,
			Action: "prompt_analysis",
			Status: "ok",
			Result: out,
		}}
		if err != nil {
			steps[0].Status = "failed"
			steps[0].Error = err.Error()
			rec.Status = StatusFailed
			rec.ErrorMessage = err.Error()
		} else {
			rec.Status = StatusSucceeded
		}
		rec.Steps = marshalSteps(steps)
		rec.DurationMS = int(e.nowFn().Sub(started).Milliseconds())
		recErr := e.store.RecordExecution(ctx, rec)
		if err != nil {
			return rec, err
		}
		return rec, recErr

	case PlanKindScript, PlanKindAPI:
		steps := make([]StepLog, 0, len(entry.Plan.Steps))
		failed := false
		var failErr error
		for i, st := range entry.Plan.Steps {
			slogStep := StepLog{Step: i + 1, Action: st.Action}
			snapshot, result, err := e.actions.Execute(ctx, st.Action, st.Params, &ev)
			if err != nil {
				slogStep.Status = "failed"
				slogStep.Error = err.Error()
				steps = append(steps, slogStep)
				failed = true
				failErr = err
				break // 失败即停（不做半途而废之后的步骤）
			}
			slogStep.Status = "ok"
			slogStep.Result = result
			slogStep.Snapshot = snapshot
			steps = append(steps, slogStep)
		}
		if failed {
			rec.Status = StatusFailed
			rec.ErrorMessage = failErr.Error()
		} else {
			rec.Status = StatusSucceeded
		}
		rec.Steps = marshalSteps(steps)
		rec.DurationMS = int(e.nowFn().Sub(started).Milliseconds())
		recErr := e.store.RecordExecution(ctx, rec)
		if failErr != nil {
			return rec, failErr
		}
		return rec, recErr

	default:
		rec.Status = StatusNoAction
		rec.ErrorMessage = fmt.Sprintf("entry plan kind %q is not executable", entry.Plan.Kind)
		rec.DurationMS = int(e.nowFn().Sub(started).Milliseconds())
		recErr := e.store.RecordExecution(ctx, rec)
		return rec, recErr
	}
}

// executePromptPlan 跑一段 AI 执行提示词（把错误上下文与日志摘要交给 LLM）。
func (e *Engine) executePromptPlan(ctx context.Context, entry *Entry, ev Event) (string, int, error) {
	if e.llm == nil {
		return "", 0, fmt.Errorf("LLM client not configured; prompt plan cannot run")
	}
	var logCtx string
	if e.logContext != nil {
		if lc, err := e.logContext.Gather(ctx, ev.ErrorType); err == nil && lc != "" {
			logCtx = lc
		}
	}
	prompt := buildPromptExecutionPrompt(entry.Plan.Prompt, ev, logCtx)
	out, _, err := e.llm.Analyze(ctx, prompt)
	if err != nil {
		return "", 0, fmt.Errorf("prompt plan execution failed: %w", err)
	}
	out = truncateRunes(out, 4000)
	return out, 0, nil
}

// RevertExecution 按执行留痕的快照逐条回滚。
//
//	返回回滚结果摘要（每步一行）。不可回滚的步骤（无快照）如实列出。
func (e *Engine) RevertExecution(ctx context.Context, rec Execution) ([]string, error) {
	var steps []StepLog
	if len(rec.Steps) > 0 {
		if err := json.Unmarshal(rec.Steps, &steps); err != nil {
			return nil, fmt.Errorf("execution steps are not valid JSON: %w", err)
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("execution has no steps to revert")
	}
	out := make([]string, 0, len(steps))
	for _, st := range steps {
		if st.Status != "ok" || len(st.Snapshot) == 0 || string(st.Snapshot) == "null" {
			out = append(out, fmt.Sprintf("step %d (%s): no snapshot, nothing to revert", st.Step, st.Action))
			continue
		}
		// ★ 回滚只依赖快照（快照里带 key/rule_name 与旧值全量材料），
		//   不回读条目 ⇒ 条目被删也不影响回滚。
		result, err := e.actions.Revert(ctx, st.Action, nil, st.Snapshot)
		if err != nil {
			return out, fmt.Errorf("revert step %d (%s) failed: %w", st.Step, st.Action, err)
		}
		out = append(out, fmt.Sprintf("step %d (%s): %s", st.Step, st.Action, result))
	}
	return out, nil
}

// RefineEntry 对「标准执行失败」的条目做自动提炼（带失败上下文重新分析）。
func (e *Engine) RefineEntry(ctx context.Context, cfg Config, entry *Entry) error {
	if e.llm == nil {
		return fmt.Errorf("LLM client not configured; refinement unavailable")
	}
	e.mu.Lock()
	lastAt := e.lastRefined[entry.ID]
	e.mu.Unlock()
	if !dedupElapsed(lastAt, cfg.RefineCooldown, e.nowFn()) {
		return nil
	}

	failExec, ok, err := e.store.LastFailedExecution(ctx, entry.ID)
	if err != nil || !ok {
		return err
	}
	ev := Event{ErrorType: entry.ErrorType, Occurrences: 1}
	if failExec.Context != nil {
		_ = json.Unmarshal(failExec.Context, &ev) // 快照解析失败 ⇒ 用空上下文继续
	}

	var logCtx string
	if e.logContext != nil {
		if lc, err := e.logContext.Gather(ctx, entry.ErrorType); err == nil {
			logCtx = lc
		}
	}

	proposal, raw, err := e.proposePlan(ctx, ev, logCtx, entry, failExec)
	if err != nil {
		return err
	}
	_ = raw

	plan, perr := NormalizePlan(proposal.PlanKind, proposal.Plan, e.actions)
	if perr != nil {
		// 提炼不出可执行方案 ⇒ 降级成 prompt 方案（手册仍被完善）。
		plan = Plan{Kind: PlanKindPrompt, Prompt: fallbackPrompt(proposal.Summary, raw), Expect: "analysis"}
	}
	confidence := clampConfidence(proposal.Confidence)

	// 提炼本身不改生产状态：只更新手册条目。是否再执行交由标准模式
	//（条目 auto_execute + 置信度门）。
	if err := e.store.UpdatePlanFromRefinement(ctx, entry.ID, plan, proposal.Title, proposal.RootCause, confidence); err != nil {
		return err
	}
	e.mu.Lock()
	e.lastRefined[entry.ID] = e.nowFn()
	e.mu.Unlock()
	slog.Info("error_playbook: entry refined",
		"entry", entry.ID, "error_type", entry.ErrorType, "plan_kind", plan.Kind,
		"confidence", confidence, "refinement", entry.RefinementCount+1)
	return nil
}

// ── 小工具 ─────────────────────────────────────────────────────────

func cooldownElapsed(last *time.Time, cooldown time.Duration, now time.Time) bool {
	if last == nil {
		return true
	}
	return now.Sub(*last) >= cooldown
}

func dedupElapsed(last time.Time, window time.Duration, now time.Time) bool {
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= window
}

func minutesSince(t *time.Time, now time.Time) float64 {
	if t == nil {
		return 0
	}
	return now.Sub(*t).Minutes()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func marshalSteps(steps []StepLog) json.RawMessage {
	b, err := json.Marshal(steps)
	if err != nil {
		return nil
	}
	return b
}

// LastRunTime 暴露 worker 最近一轮完成时间（/stats 用）。
func (e *Engine) MarkRun(now time.Time) {
	e.mu.Lock()
	e.lastRun = now
	e.mu.Unlock()
}

// HasLLM 报告分析 LLM 是否接线（/stats 的链路状态）。
func (e *Engine) HasLLM() bool {
	return e != nil && e.llm != nil
}

func (e *Engine) LastRun() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastRun
}

// PendingCount 暴露未知错误的累计状态（/stats 观测用）。
func (e *Engine) PendingCount() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.pending))
	for k, p := range e.pending {
		out[k] = p.count
	}
	return out
}
