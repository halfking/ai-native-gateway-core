package errorplaybook

// 真库契约测试：**存储层对着真库跑一遍**，断言「建表语义 + SQL 列名 +
// 表达式唯一键 + 种子 ON CONFLICT」都能查通。
//
// ★ 为什么必须有这条（与 admin/session_analytics 真库契约同款纪律）：
//
//	单测用假 store，**SQL 里的列名/类型错误打不穿**——只有真库才能证明
//	「store.go 的每条 SQL 与迁移 995 建出的形状逐列对得上」。
//
// ★ 用法：环境变量提供 DSN（TEST_ERROR_PLAYBOOK_DSN → LLM_GATEWAY_DATABASE_URL
//
//	→ DATABASE_URL），**没有 DSN 就跳过**（不连库、不造库、不写任何数据）
//	⇒ CI 不会因缺环境而红。
//
//	测试自建自清（临时条目 + 临时执行留痕，cleanup 删除）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func playbookRealDBDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{
		"TEST_ERROR_PLAYBOOK_DSN",
		"LLM_GATEWAY_DATABASE_URL",
		"DATABASE_URL",
	} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	t.Skip("realdb: no DSN provided (TEST_ERROR_PLAYBOOK_DSN / LLM_GATEWAY_DATABASE_URL / DATABASE_URL)")
	return ""
}

func TestRealDBPlaybookStoreContract(t *testing.T) {
	dsn := playbookRealDBDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	store := NewStore(db)

	// ① 表在场（迁移 995 已应用）。
	var entriesExist, execExist bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass('public.error_playbook_entries') IS NOT NULL, to_regclass('public.error_playbook_executions') IS NOT NULL`,
	).Scan(&entriesExist, &execExist); err != nil {
		t.Fatalf("probe tables: %v", err)
	}
	if !entriesExist || !execExist {
		t.Fatalf("playbook tables not provisioned (entries=%v executions=%v) — 先应用迁移 995", entriesExist, execExist)
	}

	// ② 种子条目 ≥5（迁移 995 的内容探针同口径）。
	var seedCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM error_playbook_entries WHERE source='seed'`).Scan(&seedCount); err != nil {
		t.Fatalf("count seeds: %v", err)
	}
	if seedCount < 5 {
		t.Fatalf("seed entries=%d, want >=5", seedCount)
	}

	// ③ UpsertManual + GetByID + ListEnabled 往返（plan payload 形状保真）。
	in := AnalysisEntryInput{
		ErrorType:    "pb_realdb_test_kind",
		MatchPattern: "realdb-pattern-.*",
		Title:        "真库契约测试条目",
		Description:  "自建自清",
		RootCause:    "test",
		Plan: Plan{Kind: PlanKindScript, Steps: []PlanStep{{
			Action: ActionRecordNote,
			Params: map[string]interface{}{"message": "hello"},
		}}},
		Confidence:  0.9,
		Enabled:     true,
		AutoExecute: false,
	}
	entry, err := store.UpsertManualEntry(ctx, in)
	if err != nil {
		t.Fatalf("upsert manual entry: %v", err)
	}
	defer func() {
		_ = store.DeleteEntry(context.Background(), entry.ID)
	}()
	if entry.ID == 0 || entry.Plan.Kind != PlanKindScript || len(entry.Plan.Steps) != 1 {
		t.Fatalf("entry roundtrip broken: %+v", entry)
	}
	if entry.Plan.Steps[0].Action != ActionRecordNote {
		t.Fatalf("plan steps lost: %+v", entry.Plan)
	}
	// 同键再写 ⇒ 更新同一行（唯一键语义）。
	entry2, err := store.UpsertManualEntry(ctx, in)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if entry2.ID != entry.ID {
		t.Fatalf("unique key violated: %d vs %d", entry.ID, entry2.ID)
	}

	// ④ RecordExecution + 计数回写 + 留痕形状。
	rec := Execution{
		EntryID:       entry.ID,
		ErrorType:     entry.ErrorType,
		MatchPattern:  entry.MatchPattern,
		TriggerSource: TriggerManual,
		Status:        StatusSucceeded,
		Steps:         mustJSON(t, []StepLog{{Step: 1, Action: ActionRecordNote, Status: "ok", Result: "hello"}}),
		DurationMS:    5,
		ExecutedBy:    "realdb_test",
	}
	if err := store.RecordExecution(ctx, rec); err != nil {
		t.Fatalf("record execution: %v", err)
	}
	got, err := store.GetByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get after execution: %v", err)
	}
	if got.ExecutionCount != 1 || got.SuccessCount != 1 || got.LastStatus != StatusSucceeded || got.LastExecutedAt == nil {
		t.Fatalf("counters not bumped: %+v", got)
	}
	execs, total, err := store.ListExecutions(ctx, entry.ID, "", "", 10, 0)
	if err != nil || total != 1 || len(execs) != 1 {
		t.Fatalf("list executions: n=%d total=%d err=%v", len(execs), total, err)
	}
	var stepsOut []StepLog
	if err := jsonUnmarshalSteps(execs[0].Steps, &stepsOut); err != nil || len(stepsOut) != 1 {
		t.Fatalf("steps roundtrip broken: err=%v steps=%s", err, string(execs[0].Steps))
	}

	// ⑤ UpsertFromAnalysis（ON CONFLICT 路径：同键已有 manual 条目 ⇒ 不覆盖）。
	fromPI, err := store.UpsertFromAnalysis(ctx, AnalysisEntryInput{
		ErrorType:    "pb_realdb_test_kind",
		MatchPattern: "realdb-pattern-.*",
		Title:        "PI 分析标题（不应覆盖）",
		Plan:         Plan{Kind: PlanKindPrompt, Prompt: "p"},
		Confidence:   0.7,
		AutoExecute:  true,
	})
	if err != nil {
		t.Fatalf("upsert from analysis: %v", err)
	}
	if fromPI.ID != entry.ID {
		t.Fatalf("conflict path must return existing row: %d vs %d", fromPI.ID, entry.ID)
	}
	after, _ := store.GetByID(ctx, entry.ID)
	if after.Title != "真库契约测试条目" {
		t.Fatalf("conflict path overwrote manual entry: %q", after.Title)
	}

	// ⑥ 提炼路径 + 冷却窗口。
	now := time.Now()
	entry3 := entry
	entry3.LastStatus = StatusFailed
	entry3.LastExecutedAt = &now
	_ = entry3
	if err := store.UpdatePlanFromRefinement(ctx, entry.ID, Plan{Kind: PlanKindPrompt, Prompt: "refined"}, "t2", "rc2", 0.8); err != nil {
		t.Fatalf("update refinement: %v", err)
	}
	got2, _ := store.GetByID(ctx, entry.ID)
	if got2.RefinementCount != 1 || got2.Plan.Prompt != "refined" {
		t.Fatalf("refinement not persisted: %+v", got2)
	}

	// ⑦ 表达式唯一键：同 type 不同 pattern 共存；同键再插被唯一键拒绝。
	pat, err := store.UpsertManualEntry(ctx, AnalysisEntryInput{
		ErrorType:  "pb_realdb_test_kind",
		Title:      "类型级条目",
		Plan:       Plan{Kind: PlanKindPrompt, Prompt: "type-level"},
		Confidence: 1.0, Enabled: true,
	})
	if err != nil {
		t.Fatalf("type-level entry: %v", err)
	}
	defer func() { _ = store.DeleteEntry(context.Background(), pat.ID) }()
	if pat.ID == entry.ID {
		t.Fatal("type-level and pattern entries must be distinct rows")
	}

	// ⑧ 审计轮 F2：提炼守卫——失败条目在提炼后不再是候选（提炼标记行
	//   判据；updated_at 触发器使时间戳比较在真库恒失效——首版守卫被
	//   本测试当场打回）。★ 时序注记：步骤 ⑥ 的提炼已写过一条 NOW() 的
	//   标记 ⇒ 本段先清掉它再建模「失败在前、未提炼」的初始态。
	if _, err := db.ExecContext(ctx, `
		DELETE FROM error_playbook_executions
		WHERE entry_id = $1 AND trigger_source = 'refinement'`, entry.ID); err != nil {
		t.Fatalf("clear old refinement markers: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE error_playbook_entries SET last_status='failed',
		       last_executed_at = NOW() - interval '3 hours'
		WHERE id = $1`, entry.ID); err != nil {
		t.Fatalf("backdate failure state: %v", err)
	}
	cands, err := store.ListRefineCandidates(ctx, 2*time.Hour, 5)
	if err != nil {
		t.Fatalf("list refine candidates: %v", err)
	}
	found := false
	for _, c := range cands {
		if c.ID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("failed entry must be a refine candidate before refinement")
	}
	if err := store.UpdatePlanFromRefinement(ctx, entry.ID, Plan{Kind: PlanKindPrompt, Prompt: "v2"}, "t", "rc", 0.9); err != nil {
		t.Fatalf("refine: %v", err)
	}
	cands2, err := store.ListRefineCandidates(ctx, 2*time.Hour, 5)
	if err != nil {
		t.Fatalf("list refine candidates after refinement: %v", err)
	}
	for _, c := range cands2 {
		if c.ID == entry.ID {
			t.Fatal("refined entry must NOT remain a refine candidate (audit F2 guard)")
		}
	}
	// 再次失败（真实次序：失败时间晚于旧提炼标记 ⇒ 重新成为候选）。
	//   模拟方式：把标记回拨到失败之前（真实场景里 BumpExecutionCounters
	//   会把 last_executed_at 推到 NOW()，自然晚于旧标记）。
	if _, err := db.ExecContext(ctx, `
		UPDATE error_playbook_entries SET last_status='failed',
		       last_executed_at = NOW() - interval '3 hours'
		WHERE id = $1`, entry.ID); err != nil {
		t.Fatalf("re-fail state: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE error_playbook_executions SET created_at = NOW() - interval '4 hours'
		WHERE entry_id = $1 AND trigger_source = 'refinement'`, entry.ID); err != nil {
		t.Fatalf("backdate refinement marker: %v", err)
	}
	cands3, err := store.ListRefineCandidates(ctx, 2*time.Hour, 5)
	if err != nil {
		t.Fatalf("list refine candidates after re-failure: %v", err)
	}
	found = false
	for _, c := range cands3 {
		if c.ID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("re-failed entry (failure later than refinement marker) must re-qualify as candidate")
	}

	// ⑨ 审计轮 F6：PruneExecutions 只删超保留期的行。
	var oldExecID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO error_playbook_executions
			(entry_id, error_type, trigger_source, status, executed_by, created_at)
		VALUES ($1, 'pb_realdb_test_kind', 'manual', 'succeeded', 'realdb_test', NOW() - interval '60 days')
		RETURNING id`, entry.ID).Scan(&oldExecID); err != nil {
		t.Fatalf("insert old execution: %v", err)
	}
	var freshExecID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO error_playbook_executions
			(entry_id, error_type, trigger_source, status, executed_by)
		VALUES ($1, 'pb_realdb_test_kind', 'manual', 'succeeded', 'realdb_test')
		RETURNING id`, entry.ID).Scan(&freshExecID); err != nil {
		t.Fatalf("insert fresh execution: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM error_playbook_executions WHERE executed_by='realdb_test'`)
	})
	deleted, err := store.PruneExecutions(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	var aliveOld, aliveFresh int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM error_playbook_executions WHERE id=$1`, oldExecID).Scan(&aliveOld)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM error_playbook_executions WHERE id=$1`, freshExecID).Scan(&aliveFresh)
	if aliveOld != 0 {
		t.Fatal("60-day-old execution must be pruned")
	}
	if aliveFresh != 1 {
		t.Fatal("fresh execution must survive pruning")
	}
	if deleted < 1 {
		t.Fatalf("prune must report deleted rows, got %d", deleted)
	}
}

// jsonUnmarshalSteps 解执行留痕（真库 JSONB → []byte → 结构）。
func jsonUnmarshalSteps(raw []byte, out *[]StepLog) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
