// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES
package requestobserver

// requestobserver_test.go — P1-1 的门：**观察者永不影响请求结果**。
//
// # 这道门守住哪块缺口
//
// magpie 的 `usage.Observer` 契约（internal/usage/observe.go + 社区 JS 层的
// `onUsage`）：队列 1024，**满了丢弃并计数，请求永不等待**；
// 观察者是只读的，**永不失败请求**。
//
// 计划书 §6-P1-1 的验收：
//	① 观察者**永不影响请求结果**的断言；
//	② 队列满时**丢弃计数增加且请求不阻塞**的断言；
//	③ 观察者关闭 ⇒ 无观测、无开销。
//
// # 判据清单
//
//	① 发布路径**永不阻塞**（队列满时立刻返回）
//	② 队列满 ⇒ **丢弃计数增加**
//	③ 观察者 panic ⇒ 被兜住，**不影响其它观察者**，也不外泄
//	④ 观察面关闭 ⇒ 无投递、无计数、发布路径只有一次原子读
//	⑤ 注销后不再收到事件
//	⑥ 字段级 allow-list：观察者拿不到请求体/响应体/凭据明文

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// magpieP1Collector 是一个记录型观察者。
type magpieP1Collector struct {
	mu      sync.Mutex
	begins  int
	watches int
	ends    int
	lastReq RequestInfo
}

func (c *magpieP1Collector) Begin(_ context.Context, req RequestInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.begins++
	c.lastReq = req
}
func (c *magpieP1Collector) Watch(context.Context, AttemptInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watches++
}
func (c *magpieP1Collector) End(context.Context, ResultInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ends++
}
func (c *magpieP1Collector) counts() (int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.begins, c.watches, c.ends
}

// TestHubDeliversAllThreeEvents 断言三类事件都投到了观察者。
func TestHubDeliversAllThreeEvents(t *testing.T) {
	h := NewHub(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	c := &magpieP1Collector{}
	h.Register(c)

	h.Begin(ctx, RequestInfo{RequestID: "r1", ClientModel: "m"})
	h.Watch(ctx, AttemptInfo{RequestID: "r1", Attempt: 1})
	h.End(ctx, ResultInfo{RequestID: "r1", Success: true})

	// 等分发（最多 2s）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, w, e := c.counts()
		if b == 1 && w == 1 && e == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b, w, e := c.counts()
	t.Fatalf("事件未送达：begin=%d watch=%d end=%d", b, w, e)
}

// TestHubQueueFullDropsAndCounts 判据①②：**队列满 ⇒ 丢弃计数增加、请求不阻塞**。
func TestHubQueueFullDropsAndCounts(t *testing.T) {
	// ★ 不启动分发 goroutine ⇒ 队列不会被消费 ⇒ 必然满。
	h := NewHub(2)
	c := &magpieP1Collector{}
	h.Register(c)

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		h.Begin(ctx, RequestInfo{RequestID: "r"})
	}

	if got := h.Dropped(); got != 8 {
		t.Fatalf("丢弃计数 = %d，want 8（队列 2 + 发布 10）", got)
	}
	if got := h.ObserverCount(); got != 1 {
		t.Fatalf("观察者数 = %d，want 1", got)
	}
}

// TestHubPublishNeverBlocks 判据①：**发布路径永不阻塞**。
//
// ★ 用"一个观察者卡住 + 队列满"的组合 ⇒ 此时发布必须**立刻**返回。
func TestHubPublishNeverBlocks(t *testing.T) {
	h := NewHub(1)
	blocked := make(chan struct{})
	defer close(blocked)
	h.Register(ObserverFuncs{OnBegin: func(context.Context, RequestInfo) { <-blocked }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	// 灌满队列并让观察者卡住。
	for i := 0; i < 50; i++ {
		h.Begin(ctx, RequestInfo{RequestID: "r"})
	}

	start := time.Now()
	for i := 0; i < 1000; i++ {
		h.Begin(ctx, RequestInfo{RequestID: "r"})
	}
	elapsed := time.Since(start)

	// ★ 1000 次发布必须**远快于** 1s（否则就是在等队列）。
	if elapsed > time.Second {
		t.Fatalf("1000 次发布耗时 %s —— 发布路径**不得**阻塞", elapsed)
	}
	if h.Dropped() == 0 {
		t.Fatal("队列满了却没有丢弃计数 —— 丢弃必须可观测")
	}
}

// TestHubObserverPanicIsContained 判据③：**观察者 panic 不影响其它观察者**。
func TestHubObserverPanicIsContained(t *testing.T) {
	h := NewHub(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	h.Register(ObserverFuncs{OnBegin: func(context.Context, RequestInfo) {
		panic("observer exploded")
	}})
	healthy := &magpieP1Collector{}
	h.Register(healthy)

	// 发布不应 panic（若外泄，这行就炸了）。
	h.Begin(ctx, RequestInfo{RequestID: "r1"})
	h.Watch(ctx, AttemptInfo{RequestID: "r1"})
	h.End(ctx, ResultInfo{RequestID: "r1"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, w, e := healthy.counts()
		if b == 1 && w == 1 && e == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b, w, e := healthy.counts()
	t.Fatalf("健康观察者未收到事件（panic 的观察者影响了别人）：begin=%d watch=%d end=%d", b, w, e)
}

// TestHubDisabledMeansNoObservation 判据④：**关闭 ⇒ 无观测、无开销**。
func TestHubDisabledMeansNoObservation(t *testing.T) {
	h := NewHub(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	c := &magpieP1Collector{}
	h.Register(c)
	h.SetEnabled(false)

	if h.Enabled() {
		t.Fatal("SetEnabled(false) 后仍报 Enabled")
	}
	for i := 0; i < 10; i++ {
		h.Begin(ctx, RequestInfo{RequestID: "r"})
	}
	// 给分发一点时间（证明"没有事件在途"）。
	time.Sleep(50 * time.Millisecond)

	if b, w, e := c.counts(); b != 0 || w != 0 || e != 0 {
		t.Fatalf("关闭后仍收到事件：begin=%d watch=%d end=%d", b, w, e)
	}
	if h.Dropped() != 0 {
		t.Fatalf("关闭后丢弃计数 = %d，want 0（关闭 ⇒ 连队列都不碰）", h.Dropped())
	}
	if h.Delivered() != 0 {
		t.Fatalf("关闭后投递计数 = %d，want 0", h.Delivered())
	}
}

// TestHubUnregisterStopsDelivery 判据⑤：**注销后不再收到事件**。
func TestHubUnregisterStopsDelivery(t *testing.T) {
	h := NewHub(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	c := &magpieP1Collector{}
	h.Register(c)
	h.Unregister(c)
	if h.ObserverCount() != 0 {
		t.Fatalf("注销后观察者数 = %d，want 0", h.ObserverCount())
	}

	h.Begin(ctx, RequestInfo{RequestID: "r"})
	time.Sleep(100 * time.Millisecond)
	if b, _, _ := c.counts(); b != 0 {
		t.Fatalf("注销后仍收到事件：begin=%d", b)
	}
}

// TestObserverPayloadsExposeNoSensitiveFields 判据⑥：**字段级 allow-list**。
func TestObserverPayloadsExposeNoSensitiveFields(t *testing.T) {
	forbidden := []string{
		"apikey", "api_key", "secret", "password", "authorization",
		"privatekey", "private_key", "accesskey", "access_key",
		"body", "payload", "prompt", "content", "raw",
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(RequestInfo{}),
		reflect.TypeOf(AttemptInfo{}),
		reflect.TypeOf(ResultInfo{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			lower := strings.ToLower(name)
			for _, bad := range forbidden {
				if strings.Contains(lower, bad) {
					t.Fatalf("%s 出现了敏感字段 %q —— 观察者拿不到请求体/响应体/凭据明文",
						typ.Name(), name)
				}
			}
		}
	}
}

// TestHubIsSafeWhenNil 断言 nil 中心不 panic（调用方不该为此加分支）。
func TestHubIsSafeWhenNil(t *testing.T) {
	var h *Hub
	ctx := context.Background()
	h.Begin(ctx, RequestInfo{})
	h.Watch(ctx, AttemptInfo{})
	h.End(ctx, ResultInfo{})
	h.SetEnabled(true)
	h.Register(nil)
	h.Unregister(nil)
	if h.Enabled() {
		t.Fatal("nil 中心应报未开启")
	}
	if h.Dropped() != 0 || h.Delivered() != 0 {
		t.Fatal("nil 中心的计数应为 0")
	}
}

// TestDroppedCounterIsMonotonic 断言丢弃计数**单调递增**（不会回退）。
func TestDroppedCounterIsMonotonic(t *testing.T) {
	h := NewHub(1)
	ctx := context.Background()
	var last uint64
	for i := 0; i < 20; i++ {
		h.Begin(ctx, RequestInfo{RequestID: "r"})
		got := h.Dropped()
		if got < last {
			t.Fatalf("丢弃计数回退：%d < %d", got, last)
		}
		last = got
	}
	if last == 0 {
		t.Fatal("丢弃计数始终为 0")
	}
	// 并发发布下也不该回退或溢出成负数。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Begin(ctx, RequestInfo{RequestID: "r"})
			}
		}()
	}
	wg.Wait()
	if h.Dropped() < last {
		t.Fatalf("并发后丢弃计数回退：%d < %d", h.Dropped(), last)
	}
}

// TestHubDeliverCountsEvents 断言投递计数随事件增长（可观测）。
func TestHubDeliverCountsEvents(t *testing.T) {
	h := NewHub(64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)
	c := &magpieP1Collector{}
	h.Register(c)

	for i := 0; i < 5; i++ {
		h.Begin(ctx, RequestInfo{RequestID: "r"})
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.Delivered() >= 5 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("投递计数 = %d，want >= 5", h.Delivered())
}

// TestHubStopIsIdempotent 断言 Stop 可重复调用（不 panic）。
func TestHubStopIsIdempotent(t *testing.T) {
	h := NewHub(4)
	h.Stop()
	h.Stop()
}

// atomicUnused keeps the atomic import used if future assertions need it.
var atomicUnused atomic.Uint64
