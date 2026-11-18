package promptinjection

// ══════════════════════════════════════════════════════════════════════
// 2026-11-18 审计轮 P1 钉测：回收器必须**排除本进程在途的行**。
//
// 缺口本体（源码核验，非推测）：
//   ① ClaimPendingErrors 对**整批**一次 `SET started_at = NOW()`（批龄）；
//   ② processBatchLimit 对批内**串行**逐条分析（单行龄远小于批龄）；
//   ③ ReclaimStaleClaims 的谓词只看 `started_at < NOW() - interval`。
//   ⇒ 批内第 k 行的 started_at 已老，而后台 worker 与管理 API 的手动触发
//     **可并发** ⇒ 另一路领取在阈值之后进来回收，会把**正在分析中**的行
//     放回 pending ⇒ 同一错误被重复分析（重复 LLM 花费），且 retry_count
//     累加到 max_retries 后被直接标成 failed ⇒ 本来能分析成功的错误最终失败。
//
// 该竞态在超时旋钮从 60s 放宽到 150s（f135752f0）之后由「罕见形态」变成
// 「常态形态」：批内串行上限 = batchSize × timeout = 10 × 150s = 1500s，
// 而回收阈值只有 600s。
//
// ★ 本门是**恒跑**门（sqlmock，不需要真库）⇒ 真库门缺环境时仍有判据。
// ══════════════════════════════════════════════════════════════════════

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// newMockCollector 造一个只用于验证 SQL 形状的 ErrorCollector。
func newMockCollector(t *testing.T) (*ErrorCollector, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &ErrorCollector{db: db}, mock
}

// TestReclaimStaleClaims_ExcludesInflightIDs 是主断言：
// 传入在途 ID ⇒ UPDATE 语句里必须出现 id NOT IN (...) 且参数带上它们。
//
// ★ 负控：把实现里的排除子句去掉（等价于回归到修复前），本条的
//
//	ExpectExec 正则将**匹配不上** ⇒ 门红 ⇒ 这道尺子有牙。
func TestReclaimStaleClaims_ExcludesInflightIDs(t *testing.T) {
	ec, mock := newMockCollector(t)

	mock.ExpectExec(regexp.QuoteMeta("id NOT IN ($2,$3)")).
		WithArgs("600 seconds", int64(11), int64(12)).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta("id NOT IN ($2,$3)")).
		WithArgs("600 seconds", int64(11), int64(12)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	n, err := ec.ReclaimStaleClaims(context.Background(), 10*time.Minute, 11, 12)
	if err != nil {
		t.Fatalf("ReclaimStaleClaims: %v", err)
	}
	if n != 4 {
		t.Errorf("reclaimed = %d, want 4 (3 回 pending + 1 标 failed)", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("未满足的期望: %v", err)
	}
}

// TestReclaimStaleClaims_WithoutExcludeHasNoNotIn 是反向断言：
// **不带**排除项时，SQL 里不得出现 NOT IN（否则说明排除子句是常驻的、
// 或者拼错了占位符编号）。
func TestReclaimStaleClaims_WithoutExcludeHasNoNotIn(t *testing.T) {
	ec, mock := newMockCollector(t)

	// 这里故意要求匹配 "id NOT IN" —— 实现正确时**匹配不上**，
	// sqlmock 会把这次调用当作「意外执行」而报错 ⇒ 正是我们要的断言。
	mock.ExpectExec(regexp.QuoteMeta("id NOT IN")).
		WithArgs("600 seconds").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err := ec.ReclaimStaleClaims(context.Background(), 10*time.Minute)
	if err == nil {
		t.Fatal("★ 实现居然在没有排除项时也写了 NOT IN ⇒ 排除子句的拼装是常驻的")
	}
}

// TestReclaimStaleClaims_ZeroWindowIsStillNoop 保证既有语义没被改坏。
func TestReclaimStaleClaims_ZeroWindowIsStillNoop(t *testing.T) {
	ec, mock := newMockCollector(t)
	// 零窗口 ⇒ 直接返回，**不得**发出任何 SQL。
	n, err := ec.ReclaimStaleClaims(context.Background(), 0, 1, 2)
	if err != nil || n != 0 {
		t.Fatalf("ReclaimStaleClaims(0) = (%d, %v), want (0, nil)", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("零窗口不该发出任何 SQL: %v", err)
	}
}

// TestErrorAnalyzer_InflightTracking 钉住「在途登记」这个语义：
// 登记后可见，释放后不可见。
func TestErrorAnalyzer_InflightTracking(t *testing.T) {
	ea := &ErrorAnalyzer{inflight: make(map[int64]struct{})}

	release := ea.markInflight([]int64{7, 9})
	ids := ea.inflightIDs()
	if len(ids) != 2 {
		t.Fatalf("inflightIDs = %v, want 2 项", ids)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("inflightIDs 含重复项: %v", ids)
		}
		seen[id] = true
	}
	if !seen[7] || !seen[9] {
		t.Fatalf("inflightIDs = %v, want 含 7 与 9", ids)
	}

	release()
	if got := ea.inflightIDs(); len(got) != 0 {
		t.Fatalf("释放后 inflightIDs = %v, want 空", got)
	}
}

// TestErrorAnalyzer_ProcessBatchN_DoesNotShareBatchSize 钉住既有语义：
// 请求级批量不得泄漏进共享字段（本轮未改，但批量/超时语义相邻）。
func TestErrorAnalyzer_ProcessBatchN_DoesNotShareBatchSize(t *testing.T) {
	ea := &ErrorAnalyzer{batchSize: 10, enabled: false, inflight: make(map[int64]struct{})}
	if n, err := ea.ProcessBatchN(context.Background(), 3); err != nil || n != 0 {
		t.Fatalf("ProcessBatchN = (%d, %v), want (0, nil)", n, err)
	}
	if ea.batchSize != 10 {
		t.Fatalf("共享 batchSize 被改成了 %d ⇒ 请求级 limit 泄漏到共享状态", ea.batchSize)
	}
}
