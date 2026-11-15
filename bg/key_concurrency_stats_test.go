package bg

// key_concurrency_stats_test.go — 2026-11-15 三点审计轮③：per-key 并发分钟
// 观测写入器（迁移 965）的形状与行为钉测。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// TestKeyConcurrencyStats_SQLShape 钉 UPSERT/Trim SQL 的关键子句：
//   - UPSERT 冲突键 = (minute_ts, api_key_id, instance_id)（主键）
//   - peak 取 GREATEST（同分钟双窗口不丢峰值）
//   - rejections 累加（不丢拒绝计数）
//   - 写入表 = public.api_key_concurrency_minute（迁移 965）
//   - trim 按 minute_ts 保留期删除
func TestKeyConcurrencyStats_SQLShape(t *testing.T) {
	upsert := KeyConcurrencyUpsertSQL()
	for _, want := range []string{
		"public.api_key_concurrency_minute",
		"ON CONFLICT (minute_ts, api_key_id, instance_id)",
		"peak_in_flight = GREATEST(",
		"rejections = public.api_key_concurrency_minute.rejections + EXCLUDED.rejections",
		"concurrent_limit",
	} {
		if !strings.Contains(upsert, want) {
			t.Errorf("UPSERT SQL 缺关键子句 %q:\n%s", want, upsert)
		}
	}
	trim := KeyConcurrencyTrimSQL()
	if !strings.Contains(trim, "DELETE FROM public.api_key_concurrency_minute WHERE minute_ts < now() - $1::interval") {
		t.Errorf("Trim SQL 形状错: %s", trim)
	}
}

// TestKeyConcurrencyStatsWriter_WriteOnce pgxmock 验证写入行为：每样本一条
// UPSERT、KeyID<=0 的脏样本被挡、回调带行数。
func TestKeyConcurrencyStatsWriter_WriteOnce(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	var written int64
	w := &KeyConcurrencyStatsWriter{
		pool:       mock,
		instanceID: "inst-test",
		Interval:   time.Minute,
		Retention:  7 * 24 * time.Hour,
		OnWriteComplete: func(rows int64, took time.Duration) {
			written = rows
		},
	}
	samples := []KeyConcurrencySample{
		{KeyID: 10, InFlight: 3, PeakInFlight: 5, Rejected: 1, Limit: 10},
		{KeyID: 199, InFlight: 1, PeakInFlight: 1, Rejected: 0, Limit: 20},
		{KeyID: 0, InFlight: 9, PeakInFlight: 9, Rejected: 9, Limit: 9},  // 脏样本：不得落库
		{KeyID: -3, InFlight: 9, PeakInFlight: 9, Rejected: 9, Limit: 9}, // 脏样本：不得落库
	}

	eb := mock.ExpectBatch()
	for i := 0; i < 2; i++ { // 只有 2 条干净样本
		eb.ExpectExec("INSERT INTO public.api_key_concurrency_minute").
			WithArgs(
				pgxmock.AnyArg(), // minute_ts（UTC 分钟截断）
				pgxmock.AnyArg(), // api_key_id
				"inst-test",
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	}

	w.snapshot = func() []KeyConcurrencySample { return samples }
	w.writeOnce(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("batch expectations: %v", err)
	}
	if written != 2 {
		t.Fatalf("回调行数必须只计干净样本（want 2, got %d）", written)
	}
}

// TestKeyConcurrencyStatsWriter_EmptySnapshotNoWrite 空窗口零写：无样本时
// 不得碰 DB（pgxmock 严格模式——任何未预期的调用都会红）。
func TestKeyConcurrencyStatsWriter_EmptySnapshotNoWrite(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	w := &KeyConcurrencyStatsWriter{
		pool:     mock,
		snapshot: func() []KeyConcurrencySample { return nil },
		Interval: time.Minute,
	}
	w.writeOnce(context.Background())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("空窗口不得有任何 DB 交互: %v", err)
	}
}

// TestKeyConcurrencyStatsWriter_StartNilDepsNoop nil pool/snapshot 时 Start
// 必须是 no-op（lite/无 DB 形态），不 panic 不起循环。
func TestKeyConcurrencyStatsWriter_StartNilDepsNoop(t *testing.T) {
	w := NewKeyConcurrencyStatsWriter(nil, nil, "")
	w.Start(context.Background()) // 不得 panic
	w.Stop()                      // 幂等停止（无循环也应直接返回）
}

// TestKeyConcurrencyStatsWriter_FailureDoesNotPanic 写失败（表缺席=down 迁移
// 形态）必须 WARN 自愈：不 panic、回调不触发、worker 语义可继续。
func TestKeyConcurrencyStatsWriter_FailureDoesNotPanic(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	called := false
	w := &KeyConcurrencyStatsWriter{
		pool:       mock,
		instanceID: "inst-fail",
		Interval:   time.Minute,
		OnWriteComplete: func(rows int64, took time.Duration) {
			called = true
		},
	}
	eb := mock.ExpectBatch()
	eb.ExpectExec("INSERT INTO public.api_key_concurrency_minute").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "inst-fail",
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	w.snapshot = func() []KeyConcurrencySample {
		return []KeyConcurrencySample{{KeyID: 10, InFlight: 1, PeakInFlight: 1, Limit: 10}}
	}
	w.writeOnce(context.Background()) // 不得 panic

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if called {
		t.Fatal("写失败不得触发完成回调")
	}
}
