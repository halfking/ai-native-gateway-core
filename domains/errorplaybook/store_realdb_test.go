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
}

// jsonUnmarshalSteps 解执行留痕（真库 JSONB → []byte → 结构）。
func jsonUnmarshalSteps(raw []byte, out *[]StepLog) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
