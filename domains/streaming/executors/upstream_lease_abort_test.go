package executors

// upstream_lease_abort_test.go — R32（2026-10-02，P2-A 钉测）：detached
// surviving-stream 的 upstream 上下文必须对「forwarder 钉入的租约中止源」保持
// 可达，同时对客户端断开保持免疫。12h 审计 P2-A 的实证：WithoutCancel 剥整条
// 取消链，租约丢失后最长流照跑至自然结束。
//
// 承重口径：变异（撤 upstreamContext 的 abort 并回臂）→ 红 → 还原 → 绿。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestUpstreamContext_DetachedStreamTerminatesOnLeaseAbortButSurvivesClientCancel(t *testing.T) {
	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()
	// forwarder 的组合形态：abort 源挂 WithoutCancel(fwdCtx) —— 客户端断开
	// （clientCancel）不触及它，租约中止（abortCancel）单独触发。
	abortCtx, abortCancel := context.WithCancel(context.WithoutCancel(clientCtx))
	defer abortCancel()
	fwdCtx := dispatch.WithDetachedStreamAbort(clientCtx, abortCtx)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(fwdCtx)
	params := &ExecParams{R: req, IsStream: true, StreamSurvivesClientCancel: true}
	upCtx, upCancel := (&Executor{}).upstreamContext(params, time.Second)
	defer upCancel()

	clientCancel()
	select {
	case <-upCtx.Done():
		t.Fatalf("detached stream must survive client cancel, got err=%v", upCtx.Err())
	case <-time.After(80 * time.Millisecond):
	}

	abortCancel()
	select {
	case <-upCtx.Done():
		if errors.Is(upCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("lease abort must surface as cancellation, got %v", upCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("lease-abort signal did not reach the detached upstream context (P2-A regression)")
	}
}

func TestUpstreamContext_NoAbortStampKeepsLegacyDetachedBehavior(t *testing.T) {
	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(clientCtx)
	params := &ExecParams{R: req, IsStream: true, StreamSurvivesClientCancel: true}
	upCtx, upCancel := (&Executor{}).upstreamContext(params, time.Second)
	defer upCancel()

	clientCancel()
	select {
	case <-upCtx.Done():
		t.Fatalf("legacy detached stream must survive client cancel, got err=%v", upCtx.Err())
	case <-time.After(80 * time.Millisecond):
	}
	if _, ok := upCtx.Deadline(); !ok {
		t.Fatal("legacy detached stream must keep its wall-clock deadline")
	}
}
