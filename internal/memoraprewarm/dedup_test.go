// dedup_test.go —— 「预热每会话至多一次」的执行器单测（ADR §5）。
//
// 覆盖三件事：
//
//	① 每会话只预热一次（Wiring.Apply 是每请求入口 ⇒ 这里锁住放大面）；
//	② 去重表内存有界（会话数无界 ⇒ 无界 map ⇒ 内存泄漏）；
//	③ 同一会话的并发调用**恰好一个**赢家。
package memoraprewarm

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// 1) 同一会话连续 N 次 Apply ⇒ 只预热 1 次（ADR §5：每会话至多一次）。
//
// ★ 这是本文件存在的**主因**：Apply 每请求都到，若无去重 ⇒ 请求 2..N
//
//	各起一个 goroutine、各打一次 ASM、各做一次 Redis 旁路写 ⇒ 写放大
//	与 goroutine 放大都随会话长度线性增长。
func TestApplyPrewarmsOncePerSession(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &fakeWriter{}
	g := NewGate()
	g.Set(true, nil)
	wr := NewWiring(c, w, newFakeReader(), g, nil, DefaultSessionTTL)
	body := []byte(`{"messages":[{"role":"user","content":"首问"}]}`)

	for i := 0; i < 5; i++ {
		wr.Apply(context.Background(), "t", "sess-once", body)
	}
	// 等首次预热的 goroutine 落地。
	waitFor(t, func() bool { return c.calls.Load() >= 1 })
	time.Sleep(100 * time.Millisecond)

	if got := c.calls.Load(); got != 1 {
		t.Fatalf("prewarm must fire once per session, calls=%d", got)
	}
	if got := w.calls.Load(); got != 1 {
		t.Fatalf("side-write must fire once per session, writes=%d", got)
	}
}

// 2) 不同会话各自预热一次（去重不能跨会话误伤）。
func TestApplyDistinctSessionsEachPrewarm(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &fakeWriter{}
	g := NewGate()
	g.Set(true, nil)
	wr := NewWiring(c, w, newFakeReader(), g, nil, DefaultSessionTTL)
	body := []byte(`{"messages":[{"role":"user","content":"首问"}]}`)

	for i := 0; i < 3; i++ {
		wr.Apply(context.Background(), "t", fmt.Sprintf("sess-%d", i), body)
	}
	waitFor(t, func() bool { return c.calls.Load() == 3 })
}

// 3) 空 query 的请求**不消耗**本会话那一次预热机会。
//
// ★ 顺序很重要：先去重表抢占、后取 query ⇒ 空 query 的请求会把本会话
//
//	唯一一次预热白白用掉 ⇒ 后续带正常 prompt 的请求反而拿不到预热。
func TestApplyEmptyQueryDoesNotConsumeSessionPrewarm(t *testing.T) {
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &fakeWriter{}
	g := NewGate()
	g.Set(true, nil)
	wr := NewWiring(c, w, newFakeReader(), g, nil, DefaultSessionTTL)

	// 无 user 消息 ⇒ query 为空 ⇒ 不预热、也不抢占。
	emptyBody := []byte(`{"messages":[{"role":"system","content":"sys"}]}`)
	wr.Apply(context.Background(), "t", "sess-empty-first", emptyBody)
	time.Sleep(80 * time.Millisecond)
	if got := c.calls.Load(); got != 0 {
		t.Fatalf("empty query must not prewarm, calls=%d", got)
	}

	// 随后同会话的正常请求仍能拿到那一次预热。
	realBody := []byte(`{"messages":[{"role":"user","content":"真问题"}]}`)
	wr.Apply(context.Background(), "t", "sess-empty-first", realBody)
	waitFor(t, func() bool { return c.calls.Load() == 1 })
}

// 4) 同一会话的并发调用 ⇒ 恰好一个赢家（不是「零个」也不是「多个」）。
func TestSessionDedupConcurrentSingleWinner(t *testing.T) {
	d := newSessionDedup(time.Hour, 100)
	const n = 64
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if d.claim("same-session", time.Now()) {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("exactly one winner expected, got %d", winners)
	}
}

// 5) 去重表内存有界（会话数无界 ⇒ 不能无界增长）。
func TestSessionDedupMemoryBounded(t *testing.T) {
	const max = 32
	d := newSessionDedup(time.Hour, max)
	for i := 0; i < max*20; i++ {
		d.claim(fmt.Sprintf("sess-%d", i), time.Now())
	}
	if got := d.size(); got > max {
		t.Fatalf("dedup table must stay bounded, size=%d max=%d", got, max)
	}
}

// 6) TTL 过期后允许重新抢占（session id 被复用时不永久冻结）。
func TestSessionDedupTTLExpiry(t *testing.T) {
	d := newSessionDedup(50*time.Millisecond, 10)
	if !d.claim("s", time.Now()) {
		t.Fatal("first claim must win")
	}
	if d.claim("s", time.Now()) {
		t.Fatal("claim within TTL must lose")
	}
	if !d.claim("s", time.Now().Add(time.Second)) {
		t.Fatal("claim after TTL must win again")
	}
}

// 7) 指标闭环：旁路写失败 ⇒ 记 unavailable（不是 hit）。
//
// ★ 修前：写失败只写日志不记指标 ⇒ hit 与「写失败」不可区分 ⇒ 漏斗对不上。
func TestWriteFailureRecordedNotAsHit(t *testing.T) {
	beforeTimeout := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeTimeout))
	beforeHit := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit))

	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &fakeWriter{err: context.DeadlineExceeded} // 写超时 ⇒ timeout 口径
	Trigger(c, w, nil, nil, "t", "sess-writefail", "prompt", time.Hour)
	waitFor(t, func() bool {
		return testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeTimeout)) > beforeTimeout
	})

	if got := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit)); got != beforeHit {
		t.Fatalf("write failure must not be counted as hit, hit %v -> %v", beforeHit, got)
	}
}

// 8) 指标闭环：成功旁路写 ⇒ 记 hit（口径「hit：旁路写成功（有注入可用）」）。
func TestSuccessfulWriteRecordedAsHit(t *testing.T) {
	beforeHit := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit))
	c := &fakeClient{ok: true, payload: []byte(`{"items":[{"content":"x"}]}`)}
	w := &fakeWriter{}
	Trigger(c, w, nil, nil, "t", "sess-hit", "prompt", time.Hour)
	waitFor(t, func() bool {
		return testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeHit)) > beforeHit
	})
}

// 9) 指标闭环：空 query ⇒ 不计 requested（没发起预热就不进漏斗）。
func TestEmptyQueryNotCountedAsRequested(t *testing.T) {
	before := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeRequested))
	c := &fakeClient{ok: true}
	Trigger(c, &fakeWriter{}, nil, nil, "t", "sess-empty-metric", "   ", time.Hour)
	time.Sleep(80 * time.Millisecond)
	if got := testutil.ToFloat64(prewarmTotal.WithLabelValues(OutcomeRequested)); got != before {
		t.Fatalf("empty query must not count as requested, %v -> %v", before, got)
	}
}
