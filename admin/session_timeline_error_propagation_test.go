// Package admin — session_timeline_error_propagation_test.go
//
// 补 handoff「既有测试缺口」：loadSessionTimelineInTx 迁出（原 P0 事故
// 317f28556 抽函数时误留悬空 rows.Err）后，调用方 loadSessionDetailDataInTx
// 对 **timeline 迭代错误** 的传播此前无任何测试覆盖。检查本身在
// session_timeline_query.go:77 `return timeline, rows.Err()`，但没有门钉住它。
//
// 为什么这道门要有判别力（变异验证，勿简化）：
//   - 变异 A：把 line 77 改成 `return timeline, nil`（丢迭代错误）
//     → TestTimelineIterError_PropagatesAndAbortsDetail 转红（err 变 nil、
//     detail 变非 nil）。
//   - 变异 B：让调用方吞掉 timeline 错误继续往下走
//     → 同用例转红（detail 非 nil / err 非 iterErr）。
//   - 变异 C：改 summary 查询使其失败（让错误来自 summary 而非 timeline）
//     → 本用例仍应转红（errors.Is(err, iterErr) 不成立），证明门确实在
//     区分「timeline 迭代错误」而非「任意错误」。
//
// 对照用例 TestTimelineNoError_ReturnsDetail 是防空转的：若 summary 行形状
// 写错使 scanSessionSummary 恒失败，对照用例会先转红。
package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// summaryRow 返回一行形状合法的 session_summaries 行（42 列，顺序与
// sessionSummarySelectCols / scanSessionSummary 严格一致）。任何一列类型
// 不对都会让 scanSessionSummary 报错，从而让对照用例转红。
func summaryRow(sessionID string) *pgxmock.Rows {
	now := time.Now().UTC().Truncate(time.Second)
	cols := []string{
		"session_key", "tenant_id", "task_id", "status",
		"first_request_at", "last_request_at", "duration_seconds",
		"request_count", "success_count", "error_count",
		"total_cost_usd", "input_cost_usd", "output_cost_usd",
		"total_prompt_tokens", "total_completion_tokens", "total_tokens",
		"avg_latency_ms", "min_latency_ms", "max_latency_ms",
		"models_used", "primary_model", "model_switch_count",
		"title", "summary", "key_topics", "user_intent", "quality_score",
		"compliance_status", "compliance_issues_count",
		"prompt_injection_detected", "pii_detected", "toxic_output_detected",
		"work_types", "providers", "client_models",
		"last_summarized_at", "created_at", "updated_at",
		"health_score", "health_grade", "outcome", "last_health_at",
	}
	return pgxmock.NewRows(cols).AddRow(
		sessionID, "tenant-a", nil, nil, // task_id *string, status *string
		now.Add(-time.Minute), now, 60.0,
		2, 1, 1,
		0.02, 0.01, 0.01,
		int64(100), int64(50), int64(150),
		120, nil, nil, // avg_latency_ms int, min/max_latency_ms *int
		[]string{"glm-4"}, nil, 1, // primary_model *string, model_switch_count int
		nil, nil, nil, nil, nil, // title/summary/user_intent *string, key_topics []string, quality_score *int
		"", 0,
		false, false, false,
		[]string{"chat"}, []string{"openai"}, []string{"glm-4"},
		nil, now, now, // last_summarized_at *time.Time
		nil, nil, nil, nil, // health_score/health_grade/outcome/last_health_at
	)
}

// timelineRows 返回一行合法的 timeline 行（15 列，与 sessionTimelineQuery
// 投影顺序一致）。
func timelineRows(requestID string) *pgxmock.Rows {
	ts := time.Now().UTC().Truncate(time.Second)
	cols := []string{
		"request_id", "ts", "success", "client_model", "outbound_model",
		"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
		"work_type", "compression_strategy", "cache_read_tokens",
		"error_kind", "request_preview", "response_preview",
	}
	return pgxmock.NewRows(cols).AddRow(
		requestID, ts, true, "glm-4", "glm-4",
		100, 50, 0.02, 120,
		nil, nil, nil,
		nil, nil, nil,
	)
}

const complianceCols = "request_id, detected_at, issue_type, severity, evidence, action_taken"

// TestTimelineIterError_PropagatesAndAbortsDetail 钉住核心契约：
// timeline 行迭代出错时，loadSessionDetailDataInTx 必须把错误原样上抛、
// 且不返回半成品 detail。
//
// 迭代错误用 pgxmock 的 CloseError 制造：它在 rows.Next() 走完、close() 时
// 把 closeErr 写进 nextErr[最后一行]，于是循环正常结束后 rows.Err() 返回该
// 错误 —— 精确对应真实 PG 在游标中途断流时 rows.Err() 非 nil 的情形
// （区别于 RowError 走的是 rows.Scan 分支）。
func TestTimelineIterError_PropagatesAndAbortsDetail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	tx, err := mock.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}

	iterErr := errors.New("timeline cursor broke mid-stream")

	// summary 查询成功（确保错误来源唯一：timeline 的 rows.Err）。
	mock.ExpectQuery(`FROM session_summaries ss`).
		WithArgs("sess-iter", "tenant-a").
		WillReturnRows(summaryRow("sess-iter"))

	// timeline 查询：1 行合法数据 + CloseError → 循环跑完，rows.Err() = iterErr。
	tr := timelineRows("req-1")
	tr.CloseError(iterErr)
	mock.ExpectQuery(`FROM request_logs_with_current_month`).
		WithArgs("sess-iter", "tenant-a").
		WillReturnRows(tr)

	h := &Handler{}
	detail, gotErr := h.loadSessionDetailDataInTx(context.Background(), tx, "tenant-a", "sess-iter")

	if gotErr == nil {
		t.Fatal("timeline 迭代错误被丢弃：loadSessionDetailDataInTx 返回 nil error（变异：line 77 改成 return timeline, nil）")
	}
	if !errors.Is(gotErr, iterErr) {
		t.Fatalf("error 来自非 timeline 迭代路径：got %v, want errors.Is(iterErr)", gotErr)
	}
	if detail != nil {
		t.Fatalf("迭代出错时不得返回半成品 detail：%+v", detail)
	}
	// compliance 查询绝不能被触达（错误应在 analysis 之前中止）。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected extra query or unmet expectation: %v", err)
	}
}

// TestTimelineNoError_ReturnsDetail 是对照用例：timeline 正常时必须走完
// summary → timeline → analysis(compliance) 全链路并返回完整 detail。
// 它保证上一用例不是因 summary 形状写错而「恒红/恒绿」地空转。
func TestTimelineNoError_ReturnsDetail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	tx, err := mock.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery(`FROM session_summaries ss`).
		WithArgs("sess-ok", "tenant-a").
		WillReturnRows(summaryRow("sess-ok"))
	mock.ExpectQuery(`FROM request_logs_with_current_month`).
		WithArgs("sess-ok", "tenant-a").
		WillReturnRows(timelineRows("req-ok"))
	mock.ExpectQuery(`output_compliance_audit`).
		WithArgs("sess-ok", "tenant-a").
		WillReturnRows(pgxmock.NewRows([]string{
			"request_id", "detected_at", "issue_type", "severity", "evidence", "action_taken",
		}))

	h := &Handler{}
	detail, gotErr := h.loadSessionDetailDataInTx(context.Background(), tx, "tenant-a", "sess-ok")
	if gotErr != nil {
		t.Fatalf("happy path 不应报错：%v", gotErr)
	}
	if detail == nil {
		t.Fatal("happy path 必须返回 detail")
	}
	if detail.Summary.GwSessionID != "sess-ok" {
		t.Fatalf("summary.gw_session_id = %q, want sess-ok", detail.Summary.GwSessionID)
	}
	if len(detail.Timeline) != 1 || detail.Timeline[0].RequestID != "req-ok" {
		t.Fatalf("timeline 期望 1 行 req-ok，实际 %+v", detail.Timeline)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestLoadSessionTimelineInTx_CloseErrorSurfacesAtRowsErr 直接钉住
// loadSessionTimelineInTx 自身：迭代错误必须经 rows.Err() 返回。
//
// 这里只钉「错误被上抛」这一条契约，不钉「出错时切片必须为空」——
// 生产实现是 `return timeline, rows.Err()`，出错时**同时**返回已扫到的部分
// 行与错误，这是 Go 里标准且正确的模式：调用方先判 err 再用返回值。把
// 「出错即空切片」当契约反而会诱导后来者写出 `if err == nil { use(timeline) }`
// 之外的多余防御或错误地丢弃合法部分结果。真正的防线在调用方必须判 err，
// 由 TestTimelineIterError_PropagatesAndAbortsDetail 钉住。
func TestLoadSessionTimelineInTx_CloseErrorSurfacesAtRowsErr(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	tx, err := mock.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}

	iterErr := errors.New("cursor died")
	tr := timelineRows("req-1")
	tr.CloseError(iterErr)
	mock.ExpectQuery(`FROM request_logs_with_current_month`).
		WithArgs("sess-direct", "tenant-a").
		WillReturnRows(tr)

	_, gotErr := loadSessionTimelineInTx(context.Background(), tx, "sess-direct", "tenant-a")
	if !errors.Is(gotErr, iterErr) {
		t.Fatalf("loadSessionTimelineInTx 迭代错误未上抛：got err=%v, want %v", gotErr, iterErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
