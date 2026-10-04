package admin

// handleRefresh 的「跳过诚实语义」门（2026-10-05 P2，R44 移交 §R44/移交.2）。
//
// 4315a598c 给 AutoIndexRefresher 加了 singleflight 互斥后，「跳过」成了一种
// 正常返回（旧形态返回 nil error）。handleRefresh 旧代码只看 error，对跳过
// 照样回 {"refreshed": true} 假成功 —— 运维点了刷新、看到成功，以为新配置
// 已生效，实际这次触发没有执行。本文件守：skipped 必须如实上报
// {"refreshed": false, "skipped": true}；执行路径的响应保持不变。
//
// 全部离线可跑：注入 stub，不触真库（handleRefresh 本身不读 db）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// statusStub 与生产装配的 *bg.AutoIndexRefresher 形态一致：同时实现
// RefreshOnce（SetIndexRefresher 参数接口要求的）与 RefreshOnceStatus。
type statusStub struct {
	skipped     bool
	err         error
	onceCalls   int
	statusCalls int
}

func (s *statusStub) RefreshOnce(context.Context) error {
	s.onceCalls++
	return s.err
}

func (s *statusStub) RefreshOnceStatus(context.Context) (bool, error) {
	s.statusCalls++
	return s.skipped, s.err
}

// plainStub 是旧形态 stub（只有 RefreshOnce）—— 用于验证回退路径不回归。
type plainStub struct {
	calls int
}

func (p *plainStub) RefreshOnce(context.Context) error {
	p.calls++
	return nil
}

func postRefresh(h *AutoRouteHandlers) (*httptest.ResponseRecorder, map[string]interface{}) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auto-route/refresh", nil)
	rr := httptest.NewRecorder()
	h.handleRefresh(rr, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

// 缺陷 1 的 admin 面：跳过必须返回 {"refreshed": false, "skipped": true}，
// 而不是 {"refreshed": true} 假成功。
func TestHandleRefreshReportsSkippedHonestly(t *testing.T) {
	stub := &statusStub{skipped: true}
	h := &AutoRouteHandlers{}
	h.SetIndexRefresher(stub)

	rr, out := postRefresh(h)
	if rr.Code != http.StatusOK {
		t.Fatalf("跳过不是错误，HTTP 状态 = %d, want 200", rr.Code)
	}
	if out["refreshed"] != false {
		t.Fatalf("跳过时 refreshed = %v, want false —— 假成功会误导运维以为新配置已生效", out["refreshed"])
	}
	if out["skipped"] != true {
		t.Fatalf("跳过时必须返回 skipped=true，得到 %v", out["skipped"])
	}
	if stub.statusCalls != 1 || stub.onceCalls != 0 {
		t.Fatalf("statusRefresher 存在时必须走 RefreshOnceStatus（status=%d once=%d）", stub.statusCalls, stub.onceCalls)
	}
}

// 执行路径的响应保持不变：refreshed=true + refreshed_at，且无 skipped 键。
func TestHandleRefreshExecutedResponseUnchanged(t *testing.T) {
	stub := &statusStub{skipped: false}
	h := &AutoRouteHandlers{}
	h.SetIndexRefresher(stub)

	rr, out := postRefresh(h)
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, want 200", rr.Code)
	}
	if out["refreshed"] != true {
		t.Fatalf("执行后 refreshed = %v, want true", out["refreshed"])
	}
	if _, ok := out["refreshed_at"]; !ok {
		t.Fatal("执行后响应缺少 refreshed_at")
	}
	if v, ok := out["skipped"]; ok && v == true {
		t.Fatal("执行后不应携带 skipped=true")
	}
}

// statusRefresher 探测失败（旧形态 stub 只有 RefreshOnce）时走回退路径，
// 行为与修复前一致 —— 不破坏既有装配形态。
func TestHandleRefreshFallsBackToPlainRefresher(t *testing.T) {
	stub := &plainStub{}
	h := &AutoRouteHandlers{}
	h.SetIndexRefresher(stub)

	rr, out := postRefresh(h)
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, want 200", rr.Code)
	}
	if out["refreshed"] != true {
		t.Fatalf("回退路径 refreshed = %v, want true", out["refreshed"])
	}
	if stub.calls != 1 {
		t.Fatalf("回退路径应调用 RefreshOnce 一次，得到 %d", stub.calls)
	}
}

// 错误透传：RefreshOnceStatus 返回错误时保持 500 内部错误形态。
func TestHandleRefreshPropagatesError(t *testing.T) {
	stub := &statusStub{err: context.DeadlineExceeded}
	h := &AutoRouteHandlers{}
	h.SetIndexRefresher(stub)

	rr, _ := postRefresh(h)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP 状态 = %d, want 500", rr.Code)
	}
}
