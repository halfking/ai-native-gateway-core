// service_loopback_timeout_test.go —— 回灌提交器的**超时**行为门
// （★ 2026-11-11 审计 A-5 的钉测）
//
// ★ A-5 的原形态：★ `LoopbackSubmitter.Timeout` 是**死字段**——
//
//	① 默认客户端写死 `&http.Client{Timeout: 120*time.Second}` ⇒ ★ Timeout 被忽略；
//	② 注入 Client 时**连超时都管不到**（★ 注入方 Client.Timeout 可能是 0 ⇒ 不超时）
//	③ cmd/gateway/context_agent.go 设了 `Timeout: 120s` ⇒ ★ 改它毫无效果
//
// ★★ ⇒ ★★ 「回灌挂死」在注入 Client 的部署里可以**无限期挂住** ★★。
//
// ★ 本文件钉住修后的**三个可观测行为**：
//
//	① 注入 Client（其 Transport 无超时）时，★ 请求 ctx 仍带 deadline
//	   （★ ⇒ 有界失败）★ 且那个 deadline 就是 Timeout。
//	② 默认客户端时，★ Timeout 真的传给了 http.Client（★ 实测在超时点失败）。
//	③ Timeout <= 0 时回落到默认值（★ defaultLoopbackTimeout）。
//
// ★ 为什么用**可观测的 RoundTripper**而不是看服务端的 r.Context()：
// ★ HTTP **不传输 deadline**（★ 那只是服务端本地的 ReadTimeout/WriteTimeout）
// ★★ ⇒ ★★ 服务端永远看不到客户端的 ctx deadline ⇒ ★★ 用它观测必然误判 ★★。
// ⇒ ★★ 观测点必须是**客户端这一侧**（★ Transport拿到的 req.Context()）★★。
//
// ★ 变异臂：★ 把 `ctx, cancel := context.WithTimeout(ctx, timeout)` 删掉
// ★ ★⇒ ★★ 用例 ①③ 红（★ 注入无超时 Transport ⇒ 挂死）；★ 把
// ★ `client = &http.Client{Timeout: timeout}` 退回写死 120s ⇒ ★ 用例 ② 红。

package contextagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// deadlineObserver 是一个**可观测**的 Transport 替身：★ 它记录拿到的
// 请求 ctx 上是否带 deadline、以及 deadline 距今多远。
type deadlineObserver struct {
	hasDeadline bool
	remaining   time.Duration
	// blockUntilCancel 为真时，★ 一直阻塞到 ctx 被取消（★ 模拟挂死的上游）。
	blockUntilCancel bool
}

func (o *deadlineObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	if dl, ok := req.Context().Deadline(); ok {
		o.hasDeadline = true
		o.remaining = time.Until(dl)
	}
	if o.blockUntilCancel {
		// ★兜底：★ 最多等 3s。★ 为什么必须有这个兜底：★ 变异臂
		// ★★ 「去掉 ctx 的 deadline」会让请求 ctx **永远不取消** ★★
		// ★★ ⇒ ★★ 若这里无限等 ⇒ ★★ 测试会**挂死**到 go test 的 10 分钟
		// ★★ 超时 ⇒ ★★ 一次变异验证要拖 10 分钟 ⇒ ★★ 而且挂死的报错
		// ★★ 定位很差（★ 只说「test timed out」）★★。
		// ⇒ ★★ 这里给一个**有界**的等待 ⇒ ★★ 变异臂3s 内失败并给出
		// ★★ 精确的断言消息 ★★。
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(3 * time.Second):
			return nil, context.DeadlineExceeded
		}
	}
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

// TestLoopbackSubmitter_TimeoutBoundsInjectedClient 断言：★ 即使注入的
// Client **没有**超时，★ 回灌也必须在 Timeout 内**有界失败**，
// ★ 且请求 ctx 带的 deadline 就是 Timeout。
func TestLoopbackSubmitter_TimeoutBoundsInjectedClient(t *testing.T) {
	obs := &deadlineObserver{blockUntilCancel: true}
	sub := &LoopbackSubmitter{
		Endpoint: "http://127.0.0.1:1/loopback",
		Client:   &http.Client{Transport: obs}, // ★ 注入方**不设**超时。
		Timeout:  80 * time.Millisecond,
	}
	start := time.Now()
	_, err := sub.Submit(context.Background(), []byte(`{"x":1}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("★ 注入无超时 Client 时，回灌必须在 Timeout 内失败（★ 而它成功了 ⇒ Timeout 仍是死字段）")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("回灌耗时 %v ⇒ ★ 没有被 Timeout 约束住（★ 死字段回归）", elapsed)
	}
	if !obs.hasDeadline {
		t.Fatal("★ 请求 ctx 上没有 deadline ⇒ ★ Timeout 仍是被忽略的死字段")
	}
	if obs.remaining <= 0 || obs.remaining > 80*time.Millisecond {
		t.Fatalf("ctx deadline 距今 %v，期望 ≤ 80ms（★ 那就是 Timeout 本身）", obs.remaining)
	}
}

// TestLoopbackSubmitter_DefaultClientHonorsTimeout 断言默认客户端时
// Timeout 真的传给了 http.Client（★ 而不是写死的 120s）。
func TestLoopbackSubmitter_DefaultClientHonorsTimeout(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		slow.Close()
	})

	sub := &LoopbackSubmitter{
		Endpoint: slow.URL,
		Timeout:  80 * time.Millisecond,
	}
	start := time.Now()
	_, err := sub.Submit(context.Background(), []byte(`{"x":1}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("回灌应当因超时失败")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("回灌耗时 %v ⇒ ★ 默认客户端没有采用 Timeout（★ 写死 120s ⇒ 死字段回归）", elapsed)
	}
}

// TestLoopbackSubmitter_ZeroTimeoutFallsBackToDefault 断言 Timeout <= 0 时
// 回落到 defaultLoopbackTimeout（★ 而不是「无超时」）。
func TestLoopbackSubmitter_ZeroTimeoutFallsBackToDefault(t *testing.T) {
	// ★ 绝对值契约（★ 与 B-7 的门同一口径）：★ 断言的是**契约值**，
	// ★ 而非「相对某个常量自洽」⇒ ★★ 否则「把常量改小 ⇒断言跟着改小」
	// ★★ ⇒ ★★ 「默认超时被悄悄改掉」这件事**测不出来** ★★。
	if defaultLoopbackTimeout != 120*time.Second {
		t.Fatalf("defaultLoopbackTimeout=%v，契约值是 120s（★ 改了必须同步审计档与文档）", defaultLoopbackTimeout)
	}
	obs := &deadlineObserver{}
	sub := &LoopbackSubmitter{
		Endpoint: "http://127.0.0.1:1/loopback",
		Client:   &http.Client{Transport: obs},
		// ★ Timeout 零值 ⇒ 回落到默认。
	}
	if _, err := sub.Submit(context.Background(), []byte(`{"x":1}`)); err != nil {
		t.Fatalf("回灌失败：%v", err)
	}
	if !obs.hasDeadline {
		t.Fatal("★ Timeout<=0 时应当回落到默认超时（★ 而请求 ctx 上没有 deadline ⇒ 等于「无超时」）")
	}
	if obs.remaining > defaultLoopbackTimeout {
		t.Fatalf("ctx deadline 距今 %v，超出了默认超时 %v", obs.remaining, defaultLoopbackTimeout)
	}
	if obs.remaining < defaultLoopbackTimeout-time.Minute {
		t.Fatalf("ctx deadline 距今 %v ⇒ ★ 不是默认超时 %v 那个量级", obs.remaining, defaultLoopbackTimeout)
	}
}
