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

type fakeRegistry struct {
	executed  []string
	snapshots []json.RawMessage
	reverted  []json.RawMessage
	failOn    string
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
