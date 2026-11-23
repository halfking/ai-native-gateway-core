package errorplaybook

// 引擎全流程钉测（假 store / 假动作注册表 / 假 LLM）：
//
//	标准模式（命中→执行 / 待确认 / 冷却）、未知错误（阈值→分析→门控执行→
//	落手册）、自动提炼、按快照回滚。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ── 假件 ────────────────────────────────────────────────────────────

type fakeStore struct {
	entries     []Entry
	executions  []Execution
	hits        map[int64]int
	upserts     []AnalysisEntryInput
	refinements map[int64]int
	prunes      []time.Duration
}

func newFakeStore(entries ...Entry) *fakeStore {
	return &fakeStore{entries: entries, hits: map[int64]int{}, refinements: map[int64]int{}}
}

func (f *fakeStore) ListEnabled(ctx context.Context) ([]Entry, error) {
	out := []Entry{}
	for _, e := range f.entries {
		if e.Enabled {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) RecordHit(ctx context.Context, entryID int64) error {
	f.hits[entryID]++
	return nil
}

func (f *fakeStore) RecordExecution(ctx context.Context, rec Execution) error {
	f.executions = append(f.executions, rec)
	return nil
}

func (f *fakeStore) BumpExecutionCounters(ctx context.Context, entryID int64, status string) error {
	return nil
}

func (f *fakeStore) UpsertFromAnalysis(ctx context.Context, in AnalysisEntryInput) (Entry, error) {
	f.upserts = append(f.upserts, in)
	e := Entry{
		ID:           int64(len(f.upserts) + 100),
		ErrorType:    in.ErrorType,
		MatchPattern: in.MatchPattern,
		Title:        in.Title,
		Description:  in.Description,
		RootCause:    in.RootCause,
		Plan:         in.Plan,
		PlanKind:     in.Plan.Kind,
		Confidence:   in.Confidence,
		Source:       SourcePIAnalysis,
		Enabled:      true,
		AutoExecute:  in.AutoExecute,
	}
	f.entries = append(f.entries, e)
	return e, nil
}

func (f *fakeStore) UpdatePlanFromRefinement(ctx context.Context, entryID int64, plan Plan, title, rootCause string, confidence float64) error {
	f.refinements[entryID]++
	return nil
}

func (f *fakeStore) ListRefineCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Entry, error) {
	out := []Entry{}
	now := time.Now()
	for _, e := range f.entries {
		if e.LastStatus == StatusFailed && e.LastExecutedAt != nil && now.Sub(*e.LastExecutedAt) >= olderThan {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) LastFailedExecution(ctx context.Context, entryID int64) (Execution, bool, error) {
	for i := len(f.executions) - 1; i >= 0; i-- {
		if f.executions[i].EntryID == entryID && f.executions[i].Status == StatusFailed {
			return f.executions[i], true, nil
		}
	}
	return Execution{}, false, nil
}

func (f *fakeStore) PruneExecutions(ctx context.Context, olderThan time.Duration) (int64, error) {
	f.prunes = append(f.prunes, olderThan)
	return 0, nil
}

type fakeRegistry struct {
	executed   []string
	snapshots  []json.RawMessage
	reverted   []json.RawMessage
	failOn     string
	failRevert bool
}

func (f *fakeRegistry) Has(action string) bool {
	switch action {
	case ActionRecordNote, ActionSetSettingsKV, ActionDisablePIRule, ActionCallAdminAPI:
		return true
	}
	return false
}

func (f *fakeRegistry) Names() []string {
	return []string{ActionRecordNote, ActionSetSettingsKV, ActionDisablePIRule, ActionCallAdminAPI}
}

func (f *fakeRegistry) Execute(ctx context.Context, action string, params map[string]interface{}, ev *Event) (json.RawMessage, string, error) {
	if f.failOn != "" && action == f.failOn {
		return nil, "", errors.New("injected failure: " + action)
	}
	f.executed = append(f.executed, action)
	snap := json.RawMessage(`{"marker":"snap-` + action + `"}`)
	f.snapshots = append(f.snapshots, snap)
	return snap, "ok:" + action, nil
}

func (f *fakeRegistry) Revert(ctx context.Context, action string, params map[string]interface{}, snapshot json.RawMessage) (string, error) {
	f.reverted = append(f.reverted, snapshot)
	if f.failRevert {
		return "", errors.New("injected revert failure")
	}
	return "reverted:" + action, nil
}

type fakeLLM struct {
	resp    string
	err     error
	prompts []string
}

func (f *fakeLLM) Analyze(ctx context.Context, prompt string) (string, int, error) {
	f.prompts = append(f.prompts, prompt)
	if f.err != nil {
		return "", 0, f.err
	}
	return f.resp, 10, nil
}

func enabledEntry(id int64, errorType string, plan Plan, autoExecute bool, lastExecutedAt *time.Time) Entry {
	return Entry{
		ID: id, ErrorType: errorType, Plan: plan, PlanKind: plan.Kind,
		Confidence: 1.0, Source: SourceManual, Enabled: true, AutoExecute: autoExecute,
		LastExecutedAt: lastExecutedAt,
	}
}

func cfgAuto() Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.KindThreshold = 2
	cfg.AnalyzeDedup = 24 * time.Hour
	return cfg
}

// ── 标准模式 ────────────────────────────────────────────────────────

func TestHandleEventStandardExecution(t *testing.T) {
	store := newFakeStore(enabledEntry(1, "quota_permanent",
		Plan{Kind: PlanKindScript, Steps: []PlanStep{{Action: ActionRecordNote, Params: map[string]interface{}{"message": "下线凭据"}}}},
		true, nil))
	reg := &fakeRegistry{}
	engine := NewEngine(store, reg, nil, nil)

	res := engine.HandleEvent(context.Background(), cfgAuto(), Event{ErrorType: "quota_permanent", Message: "billing exhausted", Occurrences: 5})
	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}
	if !res.Matched || res.ExecutionStatus != StatusSucceeded {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(reg.executed) != 1 || reg.executed[0] != ActionRecordNote {
		t.Fatalf("plan not executed: %v", reg.executed)
	}
	if store.hits[1] != 1 {
		t.Fatalf("hit not recorded: %v", store.hits)
	}
	if len(store.executions) != 1 || store.executions[0].TriggerSource != TriggerStandard {
		t.Fatalf("execution not recorded properly: %+v", store.executions)
	}
	// 快照进留痕。
	if len(reg.snapshots) == 0 || !strings.Contains(string(store.executions[0].Steps), "snap-record_note") {
		t.Fatalf("snapshot missing from steps: %s", string(store.executions[0].Steps))
	}
}

func TestHandleEventAwaitingApprovalWhenAutoExecuteOff(t *testing.T) {
	store := newFakeStore(enabledEntry(1, "quota_permanent", Plan{Kind: PlanKindPrompt, Prompt: "核对"}, false, nil))
	reg := &fakeRegistry{}
	engine := NewEngine(store, reg, nil, nil)

	res := engine.HandleEvent(context.Background(), cfgAuto(), Event{ErrorType: "quota_permanent", Occurrences: 1})
	if res.ExecutionStatus != StatusAwaitingApproval {
		t.Fatalf("expected awaiting_approval, got %q", res.ExecutionStatus)
	}
	if len(reg.executed) != 0 {
		t.Fatalf("plan must not run: %v", reg.executed)
	}
}

func TestHandleEventCooldownSkipsExecution(t *testing.T) {
	recent := time.Now().Add(-5 * time.Minute)
	store := newFakeStore(enabledEntry(1, "quota_permanent", Plan{Kind: PlanKindPrompt, Prompt: "核对"}, true, &recent))
	engine := NewEngine(store, &fakeRegistry{}, nil, nil)

	res := engine.HandleEvent(context.Background(), cfgAuto(), Event{ErrorType: "quota_permanent", Occurrences: 1})
	if res.ExecutionStatus != StatusSkipped {
		t.Fatalf("expected skipped (cooldown), got %q", res.ExecutionStatus)
	}
}

func TestPickEntryPatternBeatsTypeLevel(t *testing.T) {
	entries := []Entry{
		{ID: 1, ErrorType: "auth", Enabled: true, Confidence: 0.99}, // 类型级
		{ID: 2, ErrorType: "auth", MatchPattern: "invalid api key", Enabled: true, Confidence: 0.5},
		{ID: 3, ErrorType: "auth", MatchPattern: "other", Enabled: true},
	}
	ev := Event{ErrorType: "auth", Message: "upstream said: invalid api key"}
	got, ok := PickEntry(entries, ev)
	if !ok || got.ID != 2 {
		t.Fatalf("pattern entry must win, got %+v", got)
	}
	// 模式不命中 ⇒ 回落类型级。
	ev2 := Event{ErrorType: "auth", Message: "token expired"}
	got2, ok2 := PickEntry(entries, ev2)
	if !ok2 || got2.ID != 1 {
		t.Fatalf("type-level entry must match fallback, got %+v", got2)
	}
	// 非法正则条目不参与匹配。
	bad := Entry{ID: 9, ErrorType: "auth", MatchPattern: "([", Enabled: true}
	if _, ok := PickEntry([]Entry{bad}, ev); ok {
		t.Fatal("invalid-regex entry must not match")
	}
}

// ── 未知错误 → PI 分析 → 落手册 ─────────────────────────────────────

func proposalLLM(t *testing.T, planKind string, plan map[string]interface{}, confidence float64) *fakeLLM {
	t.Helper()
	planJSON, _ := json.Marshal(plan)
	resp, _ := json.Marshal(map[string]interface{}{
		"root_cause": "上游余额耗尽", "error_category": "credential",
		"title": "余额耗尽标准处置", "summary": "确认后下线凭据",
		"is_false_positive": false, "confidence": confidence,
		"plan_kind": planKind, "plan": json.RawMessage(planJSON),
	})
	return &fakeLLM{resp: string(resp)}
}

func TestHandleEventUnknownTriggersAnalysisAndRecordsEntry(t *testing.T) {
	store := newFakeStore()
	llm := proposalLLM(t, "script", map[string]interface{}{
		"steps": []interface{}{map[string]interface{}{"action": "record_note", "params": map[string]interface{}{"message": "核对配额类别"}}},
	}, 0.9)
	reg := &fakeRegistry{}
	engine := NewEngine(store, reg, llm, nil)
	cfg := cfgAuto()

	// 第 1 次：未达阈值 ⇒ 只累计。
	res := engine.HandleEvent(context.Background(), cfg, Event{ErrorType: "new_kind", Occurrences: 1})
	if res.Matched || res.Analyzed {
		t.Fatalf("below threshold must not analyze: %+v", res)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("no entry before threshold: %v", store.upserts)
	}

	// 第 2 次：达阈值 ⇒ 分析 + 执行 + 落条目。
	res = engine.HandleEvent(context.Background(), cfg, Event{ErrorType: "new_kind", Occurrences: 2})
	if !res.Analyzed || res.Err != nil {
		t.Fatalf("expected analysis: %+v", res)
	}
	if len(store.upserts) != 1 || store.upserts[0].ErrorType != "new_kind" {
		t.Fatalf("entry not recorded: %+v", store.upserts)
	}
	if store.upserts[0].Plan.Kind != PlanKindScript {
		t.Fatalf("plan not carried: %+v", store.upserts[0].Plan)
	}
	// record_note 非变更 ⇒ 条目 auto_execute=true（后续标准模式自动执行）。
	if !store.upserts[0].AutoExecute {
		t.Fatal("non-mutating plan must default auto_execute=true")
	}
	// 首次修正已执行。
	if len(reg.executed) != 1 {
		t.Fatalf("first fix not executed: %v", reg.executed)
	}
	// 同类型再次出现 ⇒ 标准模式命中（不再分析）。
	res = engine.HandleEvent(context.Background(), cfg, Event{ErrorType: "new_kind", Occurrences: 1})
	if !res.Matched || res.Analyzed {
		t.Fatalf("subsequent occurrence must take standard mode: %+v", res)
	}
}

func TestAnalyzeUnknownMutatingLowConfidenceNotExecuted(t *testing.T) {
	store := newFakeStore()
	llm := proposalLLM(t, "script", map[string]interface{}{
		"steps": []interface{}{map[string]interface{}{"action": "set_settings_kv", "params": map[string]interface{}{"key": "security.enabled", "value": false}}},
	}, 0.50)
	reg := &fakeRegistry{}
	engine := NewEngine(store, reg, llm, nil)

	if err := engine.AnalyzeUnknown(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 9}); err != nil {
		t.Fatalf("analyze failed: %v", err)
	}
	if len(reg.executed) != 0 {
		t.Fatalf("mutating plan below floor must NOT execute: %v", reg.executed)
	}
	if len(store.upserts) != 1 || store.upserts[0].AutoExecute {
		t.Fatalf("mutating plan must record auto_execute=false: %+v", store.upserts)
	}
	// 留痕 = awaiting_approval。
	if len(store.executions) != 1 || store.executions[0].Status != StatusAwaitingApproval {
		t.Fatalf("gate execution not recorded: %+v", store.executions)
	}
}

func TestAnalyzeUnknownMutatingHighConfidenceExecutesOnce(t *testing.T) {
	store := newFakeStore()
	llm := proposalLLM(t, "script", map[string]interface{}{
		"steps": []interface{}{map[string]interface{}{"action": "set_settings_kv", "params": map[string]interface{}{"key": "security.mode", "value": "observe"}}},
	}, 0.92)
	reg := &fakeRegistry{}
	engine := NewEngine(store, reg, llm, nil)

	if err := engine.AnalyzeUnknown(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 9}); err != nil {
		t.Fatalf("analyze failed: %v", err)
	}
	if len(reg.executed) != 1 || reg.executed[0] != ActionSetSettingsKV {
		t.Fatalf("first fix must execute once: %v", reg.executed)
	}
	if store.upserts[0].AutoExecute {
		t.Fatal("mutating plan must default auto_execute=false (standard mode needs admin approval)")
	}
}

func TestAnalyzeUnknownInvalidPlanFallsBackToPrompt(t *testing.T) {
	store := newFakeStore()
	resp, _ := json.Marshal(map[string]interface{}{
		"root_cause": "不明", "title": "t", "summary": "s",
		"confidence": 0.9, "plan_kind": "script",
		"plan": map[string]interface{}{"steps": []interface{}{map[string]interface{}{"action": "nuke_everything"}}},
	})
	engine := NewEngine(store, &fakeRegistry{}, &fakeLLM{resp: string(resp)}, nil)

	if err := engine.AnalyzeUnknown(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 1}); err != nil {
		t.Fatalf("analyze failed: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatal("entry must still be recorded (fallback)")
	}
	if store.upserts[0].Plan.Kind != PlanKindPrompt {
		t.Fatalf("fallback must be prompt plan, got %s", store.upserts[0].Plan.Kind)
	}
}

func TestAnalyzeUnknownLLMFailureRecordsNothing(t *testing.T) {
	store := newFakeStore()
	engine := NewEngine(store, &fakeRegistry{}, &fakeLLM{err: errors.New("endpoint down")}, nil)
	if err := engine.AnalyzeUnknown(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 1}); err == nil {
		t.Fatal("LLM failure must surface as error")
	}
	if len(store.upserts) != 0 {
		t.Fatalf("no entry without analysis material: %v", store.upserts)
	}
}

// ── 全局执行闸（2026-11-23 审计轮） ──────────────────────────────────

// mutatingProposals 是全部三种变更类方案形态（isMutatingAction 的三个分支）。
var mutatingProposals = []struct {
	name    string
	kind    string
	payload map[string]interface{}
}{
	{"script:set_settings_kv", "script", map[string]interface{}{
		"steps": []interface{}{map[string]interface{}{"action": ActionSetSettingsKV,
			"params": map[string]interface{}{"key": "security.enabled", "value": false}}}}},
	{"script:disable_pi_rule", "script", map[string]interface{}{
		"steps": []interface{}{map[string]interface{}{"action": ActionDisablePIRule,
			"params": map[string]interface{}{"rule_name": "r1"}}}}},
	{"api:POST", "api", map[string]interface{}{
		"method": "POST", "path": "/api/admin/credentials/force-recover"}},
}

// 全局 auto_execute 关闭时，未知错误首次修正**不得**执行变更类方案。
//
//	★ 回归钉：此前这条路径只看置信度 —— 标准模式有全局闸、首次修正没有
//	  ⇒「auto_execute 先关」的灰度态下，LLM 给出置信度 ≥0.80 的变更类方案
//	  会被当场执行（设置项文档自称「全局执行闸」，行为却只覆盖标准模式）。
func TestAnalyzeUnknownMutatingBlockedWhenGlobalAutoExecuteOff(t *testing.T) {
	for _, tc := range mutatingProposals {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			reg := &fakeRegistry{}
			engine := NewEngine(store, reg, proposalLLM(t, tc.kind, tc.payload, 0.95), nil)
			cfg := cfgAuto()
			cfg.AutoExecute = false

			if err := engine.AnalyzeUnknown(context.Background(), cfg, Event{ErrorType: "k", Occurrences: 9}); err != nil {
				t.Fatalf("analyze failed: %v", err)
			}
			if len(reg.executed) != 0 || len(reg.snapshots) != 0 {
				t.Fatalf("mutating plan must NOT execute while global auto_execute is off: %v", reg.executed)
			}
			if len(store.executions) != 1 || store.executions[0].Status != StatusAwaitingApproval {
				t.Fatalf("mutating first-fix must be held at awaiting_approval: %+v", store.executions)
			}
			if msg := store.executions[0].ErrorMessage; !strings.Contains(msg, "auto_execute disabled globally") {
				t.Fatalf("gate reason must name the global gate, got %q", msg)
			}
			// 分析面不受执行闸影响：条目照常落库，变更类默认 auto_execute=false。
			if len(store.upserts) != 1 || store.upserts[0].AutoExecute {
				t.Fatalf("entry must still be recorded with auto_execute=false: %+v", store.upserts)
			}
		})
	}
}

// 全局闸开 ⇒ 行为不变：高置信变更类方案仍执行一次（三种形态逐一钉）。
// 与 TestAnalyzeUnknownMutatingHighConfidenceExecutesOnce 互为对照臂。
func TestAnalyzeUnknownMutatingExecutesWhenGlobalAutoExecuteOn(t *testing.T) {
	for _, tc := range mutatingProposals {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			reg := &fakeRegistry{}
			engine := NewEngine(store, reg, proposalLLM(t, tc.kind, tc.payload, 0.95), nil)
			cfg := cfgAuto() // DefaultConfig.AutoExecute == true
			if !cfg.AutoExecute {
				t.Fatal("test premise broken: DefaultConfig().AutoExecute must be true")
			}

			if err := engine.AnalyzeUnknown(context.Background(), cfg, Event{ErrorType: "k", Occurrences: 9}); err != nil {
				t.Fatalf("analyze failed: %v", err)
			}
			if len(reg.executed) != 1 || store.executions[0].Status != StatusSucceeded {
				t.Fatalf("first fix must still execute once with the gate on: executed=%v execs=%+v",
					reg.executed, store.executions)
			}
		})
	}
}

// 只读方案（prompt / record_note）不受全局执行闸影响：
// 它们不改变生产状态，属于 auto_analyze 的分析面。
func TestAnalyzeUnknownReadOnlyPlanUnaffectedByGlobalAutoExecuteOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		payload map[string]interface{}
	}{
		{"prompt", "prompt", map[string]interface{}{"prompt": "分析该错误", "expect": "analysis"}},
		{"script:record_note", "script", map[string]interface{}{
			"steps": []interface{}{map[string]interface{}{"action": ActionRecordNote,
				"params": map[string]interface{}{"message": "核对配额类别"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			reg := &fakeRegistry{}
			engine := NewEngine(store, reg, proposalLLM(t, tc.kind, tc.payload, 0.9), nil)
			cfg := cfgAuto()
			cfg.AutoExecute = false

			if err := engine.AnalyzeUnknown(context.Background(), cfg, Event{ErrorType: "k", Occurrences: 9}); err != nil {
				t.Fatalf("analyze failed: %v", err)
			}
			if len(store.executions) != 1 || store.executions[0].Status != StatusSucceeded {
				t.Fatalf("read-only plan must still run with the gate off: %+v", store.executions)
			}
			if tc.name == "script:record_note" && (len(reg.executed) != 1 || reg.executed[0] != ActionRecordNote) {
				t.Fatalf("record_note must run: %v", reg.executed)
			}
		})
	}
}

// 两道闸同时不过 ⇒ 原因报置信度门（把全局闸打开它也执行不了，那才是主因）。
func TestAnalyzeUnknownGateReasonReportsConfidenceFloorFirst(t *testing.T) {
	store := newFakeStore()
	tc := mutatingProposals[0]
	engine := NewEngine(store, &fakeRegistry{}, proposalLLM(t, tc.kind, tc.payload, 0.50), nil)
	cfg := cfgAuto()
	cfg.AutoExecute = false

	if err := engine.AnalyzeUnknown(context.Background(), cfg, Event{ErrorType: "k", Occurrences: 9}); err != nil {
		t.Fatalf("analyze failed: %v", err)
	}
	msg := store.executions[0].ErrorMessage
	if !strings.Contains(msg, "< floor") || strings.Contains(msg, "auto_execute disabled globally") {
		t.Fatalf("confidence floor must be reported as the blocking gate, got %q", msg)
	}
}

// ── 留痕归属（2026-11-23 审计轮） ─────────────────────────────────────

// 手动执行的留痕记操作者身份；自动化入口仍记引擎身份；空身份回落引擎身份。
//
//	★ 回归钉：此前手动执行也走 ExecuteEntry ⇒ 留痕 executed_by 恒为
//	  "error_playbook"，能执行变更动作并留快照的功能答不出「是谁按的」。
func TestManualExecutionRecordsOperatorIdentity(t *testing.T) {
	store := newFakeStore(enabledEntry(1, "k", Plan{Kind: PlanKindPrompt, Prompt: "核对"}, true, nil))
	engine := NewEngine(store, &fakeRegistry{}, &fakeLLM{resp: "analysis"}, nil)
	ev := Event{ErrorType: "k", Occurrences: 1}

	if _, err := engine.ExecuteEntryAs(context.Background(), &store.entries[0], ev, TriggerManual, "manual:alice"); err != nil {
		t.Fatalf("manual execute failed: %v", err)
	}
	if got := store.executions[0].ExecutedBy; got != "manual:alice" {
		t.Fatalf("manual execution must record the operator, got %q", got)
	}
	if _, err := engine.ExecuteEntry(context.Background(), &store.entries[0], ev, TriggerStandard); err != nil {
		t.Fatalf("automation execute failed: %v", err)
	}
	if got := store.executions[1].ExecutedBy; got != "error_playbook" {
		t.Fatalf("automation entry point must keep the engine identity, got %q", got)
	}
	if _, err := engine.ExecuteEntryAs(context.Background(), &store.entries[0], ev, TriggerManual, "  "); err != nil {
		t.Fatalf("manual execute (blank operator) failed: %v", err)
	}
	if got := store.executions[2].ExecutedBy; got != "error_playbook" {
		t.Fatalf("blank operator must fall back to the engine identity, got %q", got)
	}
}

// 手动分析的两个留痕分支（执行 / 被闸拦下）都记操作者身份；自动化入口仍记引擎身份。
func TestManualAnalysisRecordsOperatorIdentity(t *testing.T) {
	readOnly := proposalLLM(t, "script", map[string]interface{}{
		"steps": []interface{}{map[string]interface{}{"action": "record_note", "params": map[string]interface{}{"message": "核对"}}},
	}, 0.9)
	gated := proposalLLM(t, "script", mutatingProposals[0].payload, 0.50)

	// ① 执行分支。
	store := newFakeStore()
	engine := NewEngine(store, &fakeRegistry{}, readOnly, nil)
	if err := engine.AnalyzeUnknownAs(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 9}, "manual:bob"); err != nil {
		t.Fatalf("manual analyze failed: %v", err)
	}
	if len(store.executions) != 1 || store.executions[0].ExecutedBy != "manual:bob" {
		t.Fatalf("executed first-fix must record the operator: %+v", store.executions)
	}
	// ② 闸门分支。
	store = newFakeStore()
	engine = NewEngine(store, &fakeRegistry{}, gated, nil)
	if err := engine.AnalyzeUnknownAs(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 9}, "manual:bob"); err != nil {
		t.Fatalf("manual analyze (gated) failed: %v", err)
	}
	if len(store.executions) != 1 || store.executions[0].ExecutedBy != "manual:bob" ||
		store.executions[0].Status != StatusAwaitingApproval {
		t.Fatalf("gated first-fix must record the operator: %+v", store.executions)
	}
	// ③ 自动化入口不变。
	store = newFakeStore()
	engine = NewEngine(store, &fakeRegistry{}, readOnly, nil)
	if err := engine.AnalyzeUnknown(context.Background(), cfgAuto(), Event{ErrorType: "k", Occurrences: 9}); err != nil {
		t.Fatalf("automation analyze failed: %v", err)
	}
	if got := store.executions[0].ExecutedBy; got != "error_playbook" {
		t.Fatalf("automation entry point must keep the engine identity, got %q", got)
	}
}

// ── 自动完善与回滚 ──────────────────────────────────────────────────

func TestRefineEntryUpdatesPlan(t *testing.T) {
	last := time.Now().Add(-3 * time.Hour)
	entry := enabledEntry(1, "quota_permanent", Plan{Kind: PlanKindPrompt, Prompt: "旧指引"}, false, nil)
	entry.LastStatus = StatusFailed
	entry.LastExecutedAt = &last
	store := newFakeStore(entry)
	store.executions = append(store.executions, Execution{
		EntryID: 1, Status: StatusFailed, ErrorMessage: "rule \"x\" does not exist",
		Context: mustJSON(t, Event{ErrorType: "quota_permanent"}),
	})
	llm := proposalLLM(t, "prompt", map[string]interface{}{"prompt": "新指引：先核对规则名"}, 0.85)
	engine := NewEngine(store, &fakeRegistry{}, llm, nil)

	if err := engine.RefineEntry(context.Background(), cfgAuto(), &store.entries[0]); err != nil {
		t.Fatalf("refine failed: %v", err)
	}
	if store.refinements[1] != 1 {
		t.Fatalf("refinement not recorded: %v", store.refinements)
	}
	// 提炼提示词必须带上失败上下文。
	if len(llm.prompts) != 1 || !strings.Contains(llm.prompts[0], "rule \\\"x\\\" does not exist") &&
		!strings.Contains(llm.prompts[0], "does not exist") {
		t.Fatalf("failure context missing from refine prompt: %.200s", llm.prompts[0])
	}
}

func TestRevertExecutionUsesSnapshots(t *testing.T) {
	store := newFakeStore()
	reg := &fakeRegistry{}
	engine := NewEngine(store, reg, nil, nil)

	steps, _ := json.Marshal([]StepLog{
		{Step: 1, Action: ActionSetSettingsKV, Status: "ok", Snapshot: json.RawMessage(`{"key":"security.enabled"}`)},
		{Step: 2, Action: ActionRecordNote, Status: "ok"},
	})
	rec := Execution{ID: 7, EntryID: 1, Status: StatusSucceeded, Steps: steps}
	out, err := engine.RevertExecution(context.Background(), rec)
	if err != nil {
		t.Fatalf("revert failed: %v", err)
	}
	if len(reg.reverted) != 1 {
		t.Fatalf("only snapshot-bearing steps revert: %v", reg.reverted)
	}
	if len(out) != 2 || !strings.Contains(out[1], "no snapshot") {
		t.Fatalf("non-revertible step must be reported: %v", out)
	}
}

func TestExecuteEntryAutoRollbackOnStepFailure(t *testing.T) {
	// ★ 审计轮 F1 钉测：多步变更方案在第 2 步失败 ⇒ 第 1 步的变更必须
	//   就地回滚（快照逆序），且留痕里能看到回滚记录与人工回滚提示。
	store := newFakeStore(enabledEntry(1, "quota_permanent",
		Plan{Kind: PlanKindScript, Steps: []PlanStep{
			{Action: ActionSetSettingsKV, Params: map[string]interface{}{"key": "security.mode", "value": "observe"}},
			{Action: ActionDisablePIRule, Params: map[string]interface{}{"rule_name": "ghost_rule"}},
		}}, true, nil))
	reg := &fakeRegistry{failOn: ActionDisablePIRule}
	engine := NewEngine(store, reg, nil, nil)

	rec, err := engine.ExecuteEntry(context.Background(), &store.entries[0],
		Event{ErrorType: "quota_permanent", Occurrences: 1}, TriggerManual)
	if err == nil {
		t.Fatal("step failure must surface")
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status=failed expected, got %q", rec.Status)
	}
	if len(reg.reverted) != 1 {
		t.Fatalf("completed mutating step must be auto-reverted: reverted=%d", len(reg.reverted))
	}
	if !strings.Contains(string(rec.Steps), "#revert") {
		t.Fatalf("rollback must be recorded in steps: %s", string(rec.Steps))
	}
}

func TestExecuteEntryRevertFailureSurfacesManualHint(t *testing.T) {
	// 回滚本身失败 ⇒ 原失败不被掩盖，且 errorMessage 必须给出人工回滚指引。
	store := newFakeStore(enabledEntry(1, "quota_permanent",
		Plan{Kind: PlanKindScript, Steps: []PlanStep{
			{Action: ActionSetSettingsKV, Params: map[string]interface{}{"key": "security.mode", "value": "observe"}},
			{Action: ActionDisablePIRule, Params: map[string]interface{}{"rule_name": "ghost_rule"}},
		}}, true, nil))
	reg := &fakeRegistry{failOn: ActionDisablePIRule, failRevert: true}
	engine := NewEngine(store, reg, nil, nil)

	rec, err := engine.ExecuteEntry(context.Background(), &store.entries[0],
		Event{ErrorType: "quota_permanent", Occurrences: 1}, TriggerManual)
	if err == nil || rec.Status != StatusFailed {
		t.Fatalf("expected failed execution, err=%v status=%q", err, rec.Status)
	}
	if !strings.Contains(rec.ErrorMessage, "injected failure: disable_pi_rule") {
		t.Fatalf("original failure must not be masked: %q", rec.ErrorMessage)
	}
	if !strings.Contains(rec.ErrorMessage, "/executions/{id}/revert") {
		t.Fatalf("manual revert hint missing: %q", rec.ErrorMessage)
	}
}

func TestExecuteEntryFailureWithoutSnapshotNoRollbackNoise(t *testing.T) {
	// 只读步骤失败（record_note 不产生快照）⇒ 没有可回滚对象 ⇒ 留痕里
	// 不该出现 #revert 噪音。
	store := newFakeStore(enabledEntry(1, "k",
		Plan{Kind: PlanKindScript, Steps: []PlanStep{
			{Action: ActionRecordNote, Params: map[string]interface{}{"message": "x"}},
		}}, true, nil))
	reg := &fakeRegistry{failOn: ActionRecordNote}
	engine := NewEngine(store, reg, nil, nil)
	rec, err := engine.ExecuteEntry(context.Background(), &store.entries[0],
		Event{ErrorType: "k", Occurrences: 1}, TriggerManual)
	if err == nil || rec.Status != StatusFailed {
		t.Fatalf("injected failure must fail: err=%v status=%q", err, rec.Status)
	}
	if len(reg.reverted) != 0 || strings.Contains(string(rec.Steps), "#revert") {
		t.Fatalf("snapshot-less steps must not produce rollback noise: %s", string(rec.Steps))
	}
}

// ── worker 摄取 ─────────────────────────────────────────────────────

type fakeSource struct{ events []Event }

func (f fakeSource) FetchWindow(ctx context.Context, since, until time.Time) ([]Event, time.Time, error) {
	return f.events, until, nil
}

func TestWorkerRunOnceProcessesWindow(t *testing.T) {
	store := newFakeStore(enabledEntry(1, "quota_permanent", Plan{Kind: PlanKindPrompt, Prompt: "核对"}, true, nil))
	engine := NewEngine(store, &fakeRegistry{}, nil, nil)
	w := NewWorker(engine, fakeSource{events: []Event{
		{ErrorType: "quota_permanent", Occurrences: 3, Message: "billing"},
	}}, func() Config { return cfgAuto() }, time.Minute)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("runOnce failed: %v", err)
	}
	if store.hits[1] != 1 {
		t.Fatalf("window event not handled: %v", store.hits)
	}
	// ★ 审计轮 F6 钉测：首轮会触发一次执行留痕清理（保留期 30 天）。
	if len(store.prunes) != 1 || store.prunes[0] != playbookRetention {
		t.Fatalf("retention prune not invoked: %v", store.prunes)
	}
}

func TestWorkerRefinePassSkippedWithoutLLM(t *testing.T) {
	// ★ 审计轮 F3 钉测：LLM 未配置 ⇒ 提炼段整段跳过（不再每轮空转刷 WARN）。
	last := time.Now().Add(-3 * time.Hour)
	entry := enabledEntry(1, "quota_permanent", Plan{Kind: PlanKindPrompt, Prompt: "旧指引"}, false, nil)
	entry.LastStatus = StatusFailed
	entry.LastExecutedAt = &last
	store := newFakeStore(entry)
	engine := NewEngine(store, &fakeRegistry{}, nil, nil) // 无 LLM
	w := NewWorker(engine, fakeSource{events: []Event{}}, func() Config { return cfgAuto() }, time.Minute)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("runOnce failed: %v", err)
	}
	if store.refinements[1] != 0 {
		t.Fatal("refine pass must be skipped without LLM")
	}
}

func TestWorkerRunOnceDisabledIsNoop(t *testing.T) {
	store := newFakeStore()
	engine := NewEngine(store, &fakeRegistry{}, nil, nil)
	w := NewWorker(engine, fakeSource{events: []Event{{ErrorType: "x", Occurrences: 9}}},
		func() Config { c := cfgAuto(); c.Enabled = false; return c }, time.Minute)
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("disabled worker must be noop, got %v", err)
	}
	if len(store.executions) != 0 || len(store.hits) != 0 {
		t.Fatal("disabled worker must not touch store")
	}
}
