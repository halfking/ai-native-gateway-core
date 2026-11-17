package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/internal/anomaly"
)

// newObserveWrapHarness 构造带异常检测包装的 handler 与捕获器。
func newObserveWrapHarness(t *testing.T) (*httptest.ResponseRecorder, *[]*http.Request, http.Handler) {
	t.Helper()
	mgr, err := anomaly.NewDefaultManager()
	if err != nil {
		t.Fatalf("NewDefaultManager: %v", err)
	}
	var downstream []*http.Request
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		downstream = append(downstream, r)
	})
	// 第三个参数是安全事件出网 sink（M2-2），第四个是跨实例模式快照
	// （M2-3）。这里都传 nil：本组用例只关心"观察模式零执法"的行为，
	// 不关心事件出网与模式同步（它们各有自己的用例）。
	return httptest.NewRecorder(), &downstream, wrapAnomalyDetector(next, mgr, nil, nil)
}

func observeKeyInfo(id int) *authentication.KeyInfo {
	return &authentication.KeyInfo{ID: id, TenantID: "tenant-observe"}
}

func observeRequest(ctx context.Context, path string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	if ctx != nil {
		r = r.WithContext(ctx)
	}
	return r
}

// TestAnomalyWrap_NoKeyIdentitySkipsDetection 钉 2026-11-15 ②：无 key 身份
// （静态 key 旁路/内部回环探针/无验证模式）不检测、不计数、直接放行——
// 空桶共享缺陷的消除面（211 事故：无会话头流量全挤一个窗口 ~1 小时）。
func TestAnomalyWrap_NoKeyIdentitySkipsDetection(t *testing.T) {
	rec, downstream, h := newObserveWrapHarness(t)

	// 无身份上下文（prepareSanitizeRequest 未装配 / 静态 key 旁路）。
	for i := 0; i < 15; i++ {
		h.ServeHTTP(rec, observeRequest(nil, "/v1/chat/completions"))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("无身份请求被拦: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(*downstream) != 15 {
		t.Fatalf("无身份请求必须全部放行（got %d/15）", len(*downstream))
	}
	// 内部 key 同样跳过。
	internal := observeKeyInfo(9)
	internal.IsInternal = true
	rec2, downstream2, h2 := newObserveWrapHarness(t)
	for i := 0; i < 15; i++ {
		h2.ServeHTTP(rec2, observeRequest(streaming.WithVerifiedKeyInfoForTest(context.Background(), internal), "/v1/chat/completions"))
	}
	if len(*downstream2) != 15 || rec2.Code != http.StatusOK {
		t.Fatalf("内部 key 必须跳过检测（downstream=%d status=%d）", len(*downstream2), rec2.Code)
	}
}

// TestAnomalyWrap_ObserveOnlyNeverBlocks 钉 2026-11-15 ③：频率风暴只观察
// 不拦截——同一 key 连打 15 笔（远超旧阈值 10/5min）必须全部 200 到达下游，
// 客户端限流统一由按 key RPM 链执法（变异臂：恢复拦截臂必须让本测试红）。
func TestAnomalyWrap_ObserveOnlyNeverBlocks(t *testing.T) {
	rec, downstream, h := newObserveWrapHarness(t)
	ctx := streaming.WithVerifiedKeyInfoForTest(context.Background(), observeKeyInfo(42))

	for i := 0; i < 15; i++ {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, observeRequest(ctx, "/v1/chat/completions"))
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 笔被观察面拦截: status=%d body=%s（观察模式必须零拦截）", i+1, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Anomaly-Code") != "" {
			t.Fatalf("观察模式不得产出异常标记头: %q", rec.Header().Get("X-Anomaly-Code"))
		}
	}
	if len(*downstream) != 15 {
		t.Fatalf("观察模式下全部请求应到达下游（got %d/15）", len(*downstream))
	}
}

// TestAnomalyWrap_PerKeyWindows 钉按 key 分桶：key A 打满风暴阈值后，key B
// 的首笔必须干净（旧实现空 session 共享桶的反面）。
func TestAnomalyWrap_PerKeyWindows(t *testing.T) {
	_, _, h := newObserveWrapHarness(t)
	ctxA := streaming.WithVerifiedKeyInfoForTest(context.Background(), observeKeyInfo(7))
	ctxB := streaming.WithVerifiedKeyInfoForTest(context.Background(), observeKeyInfo(8))

	for i := 0; i < 15; i++ {
		h.ServeHTTP(httptest.NewRecorder(), observeRequest(ctxA, "/v1/chat/completions"))
	}
	recB := httptest.NewRecorder()
	h.ServeHTTP(recB, observeRequest(ctxB, "/v1/chat/completions"))
	if recB.Code != http.StatusOK {
		t.Fatalf("key B 首笔被 key A 的窗口污染: status=%d", recB.Code)
	}
}

// TestAnomalyWrap_NonChatEndpointBypasses 钉快速路径：非聊天端点零开销直通。
func TestAnomalyWrap_NonChatEndpointBypasses(t *testing.T) {
	rec, downstream, h := newObserveWrapHarness(t)
	h.ServeHTTP(rec, observeRequest(nil, "/v1/models"))
	if rec.Code != http.StatusOK || len(*downstream) != 1 {
		t.Fatalf("非聊天端点必须直通（status=%d downstream=%d）", rec.Code, len(*downstream))
	}
}
