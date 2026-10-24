package extpointledger

// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES

// extpointledger_test.go —— P2-2「扩展点台账」的运行期门。
//
// # 判据清单
//
//	① 结局计数：calls / failures / timeouts / dropped 各自正确
//	② p99：★ **最近窗口**的 p99（最近秩法）算得对，且窗口容量写在读端上
//	③ 内存有界：样本环形缓冲不增长（超出的覆盖最旧的）
//	④ 最近错误：文本 + 时间都留得住（且只留**最近**那一次）
//	⑤ 快照：按 ID 排序、**未记录过的机制不出现**
//	⑥ 并发：N 个 goroutine 并发记录 ⇒ 计数**精确**（不丢不重）
//	⑦ 允许集合：只认裁决表里的机制 ID

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestLedgerCountsOutcomes ①：结局计数。
func TestLedgerCountsOutcomes(t *testing.T) {
	l := New()
	l.Record("m1", OutcomeOK, time.Millisecond, nil)
	l.Record("m1", OutcomeFailed, 2*time.Millisecond, errors.New("boom"))
	l.Record("m1", OutcomeTimeout, 3*time.Millisecond, context.DeadlineExceeded)
	l.Record("m1", OutcomeDropped, 0, nil)

	e, ok := l.Entry("m1")
	if !ok {
		t.Fatal("m1 不在台账里")
	}
	if e.Calls != 4 {
		t.Fatalf("calls = %d, want 4", e.Calls)
	}
	if e.Failures != 1 {
		t.Fatalf("failures = %d, want 1", e.Failures)
	}
	if e.Timeouts != 1 {
		t.Fatalf("timeouts = %d, want 1", e.Timeouts)
	}
	if e.Dropped != 1 {
		t.Fatalf("dropped = %d, want 1", e.Dropped)
	}
	// ★ 失败与超时**也算调用** ⇒ calls 是「调用过」的总数，不是「成功数」。
	if e.Calls != e.Failures+e.Timeouts+e.Dropped+(4-e.Failures-e.Timeouts-e.Dropped) {
		t.Fatalf("calls 与结局计数不自洽：%+v", e)
	}
}

// TestLedgerCountsRejectionsSeparately ①′：
// ★ **拒绝**必须与**失败**分开计数（4xx 拒绝 ≠ 5xx 出错）。
//
// ⇒ 理由见 `OutcomeRejected` 的注释：混在一起会让「主链失败数」
// 变成「拒绝率」⇒ 运维会把「有人在狂刷坏 key」读成「网关坏了」。
func TestLedgerCountsRejectionsSeparately(t *testing.T) {
	l := New()
	l.Record("m", OutcomeOK, time.Millisecond, nil)
	l.Record("m", OutcomeRejected, 2*time.Millisecond, errors.New("401 unauthorized"))
	l.Record("m", OutcomeFailed, 3*time.Millisecond, errors.New("500 boom"))

	e, ok := l.Entry("m")
	if !ok {
		t.Fatal("m 不在台账里")
	}
	if e.Calls != 3 {
		t.Fatalf("calls = %d, want 3", e.Calls)
	}
	if e.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1（★ 拒绝必须单列）", e.Rejected)
	}
	if e.Failures != 1 {
		t.Fatalf("failures = %d, want 1（★ 拒绝不计入失败）", e.Failures)
	}
	// ★ 结局计数之和不超过 calls（三类互斥）。
	if e.Failures+e.Rejected+e.Timeouts+e.Dropped > e.Calls {
		t.Fatalf("结局计数之和超过 calls：%+v", e)
	}
}

// TestLedgerP99IsNearestRank ②：p99 算得对。
func TestLedgerP99IsNearestRank(t *testing.T) {
	l := New()
	// 100 个样本：1ms..100ms ⇒ p99 = 第 99 小的那个 = 99ms。
	for i := 1; i <= 100; i++ {
		l.Record("m", OutcomeOK, time.Duration(i)*time.Millisecond, nil)
	}
	e, ok := l.Entry("m")
	if !ok {
		t.Fatal("m 不在台账里")
	}
	if e.P99Millis != 99 {
		t.Fatalf("p99 = %d ms, want 99", e.P99Millis)
	}
	// ★ 窗口容量必须写在读端上（否则「p99」的口径不可知）。
	if e.P99Window != DefaultSampleCapacity {
		t.Fatalf("p99_window = %d, want %d", e.P99Window, DefaultSampleCapacity)
	}
	// 单个样本 ⇒ p99 就是它自己。
	l2 := New()
	l2.Record("m", OutcomeOK, 7*time.Millisecond, nil)
	e2, _ := l2.Entry("m")
	if e2.P99Millis != 7 {
		t.Fatalf("单样本 p99 = %d ms, want 7", e2.P99Millis)
	}
}

// TestLedgerSampleRingIsBounded ③：内存有界。
//
// ★ 关键：**超出容量后覆盖最旧的**（而不是丢弃新样本）⇒
// 「p99 是**最近**窗口」这个口径才成立。
func TestLedgerSampleRingIsBounded(t *testing.T) {
	const capacity = 8
	l := NewWithCapacity(capacity)
	for i := 0; i < 100; i++ {
		l.Record("m", OutcomeOK, time.Duration(i)*time.Millisecond, nil)
	}
	e, ok := l.Entry("m")
	if !ok {
		t.Fatal("m 不在台账里")
	}
	if e.Calls != 100 {
		t.Fatalf("calls = %d, want 100（计数不受样本窗口影响）", e.Calls)
	}
	// 最近的 8 个样本是 92..99ms ⇒ p99 = 99ms。
	if e.P99Millis != 99 {
		t.Fatalf("p99 = %d ms, want 99（应只保留最近窗口）", e.P99Millis)
	}
	if e.P99Window != capacity {
		t.Fatalf("p99_window = %d, want %d", e.P99Window, capacity)
	}
}

// TestLedgerLastError ④：最近错误（文本 + 时间）。
func TestLedgerLastError(t *testing.T) {
	l := New()
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	l.RecordAt("m", OutcomeFailed, time.Millisecond, errors.New("first"), at)
	l.RecordAt("m", OutcomeFailed, time.Millisecond, errors.New("second"), at.Add(time.Minute))
	e, _ := l.Entry("m")
	if e.LastError != "second" {
		t.Fatalf("last_error = %q, want %q", e.LastError, "second")
	}
	if !e.LastErrorAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("last_error_at = %v, want %v", e.LastErrorAt, at.Add(time.Minute))
	}
	if e.Failures != 2 {
		t.Fatalf("failures = %d, want 2", e.Failures)
	}
}

// TestLedgerSnapshotIsSortedAndOnlyRecorded ⑤：快照排序 + 未记录的不出现。
func TestLedgerSnapshotIsSortedAndOnlyRecorded(t *testing.T) {
	l := New()
	l.Record("zeta", OutcomeOK, time.Millisecond, nil)
	l.Record("alpha", OutcomeOK, time.Millisecond, nil)
	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("快照条数 = %d, want 2（未记录过的机制不应出现）", len(snap))
	}
	if snap[0].MechanismID != "alpha" || snap[1].MechanismID != "zeta" {
		t.Fatalf("快照未按 ID 排序：%+v", snap)
	}
	if _, ok := l.Entry("never-recorded"); ok {
		t.Fatal("未记录过的机制不应出现在台账里")
	}
}

// TestLedgerConcurrentRecord ⑥：并发记录不丢不重。
func TestLedgerConcurrentRecord(t *testing.T) {
	l := New()
	const workers = 64
	const each = 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				l.Record("m", OutcomeOK, time.Millisecond, nil)
			}
		}(i)
	}
	wg.Wait()
	e, _ := l.Entry("m")
	if e.Calls != workers*each {
		t.Fatalf("calls = %d, want %d（并发下丢了计数）", e.Calls, workers*each)
	}
}

// TestKnownMechanism ⑦：允许集合。
func TestKnownMechanism(t *testing.T) {
	known := map[string]bool{"http_middleware_chain": true}
	if !KnownMechanism(known, "http_middleware_chain") {
		t.Fatal("已登记的机制应被接受")
	}
	if KnownMechanism(known, "typo_mechanism") {
		t.Fatal("未登记的机制必须被拒（否则会凭空长出一行台账）")
	}
	if KnownMechanism(known, "") {
		t.Fatal("空机制 ID 必须被拒")
	}
	// ★ 未注入允许集合时不拦截（由调用方选择严格度）。
	if !KnownMechanism(nil, "anything") {
		t.Fatal("未注入允许集合时不应拦截")
	}
}

// TestLedgerNilSafety 台账挂在 nil 上时不能 panic（观测面不能拖垮数据面）。
func TestLedgerNilSafety(t *testing.T) {
	var l *Ledger
	l.Record("m", OutcomeOK, time.Millisecond, nil) // 不得 panic
	if snap := l.Snapshot(); snap != nil {
		t.Fatalf("nil 台账的快照应是 nil：%v", snap)
	}
	_ = fmt.Sprint(l)
}
