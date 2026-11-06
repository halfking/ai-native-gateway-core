// safety/recovery_event_guards_test.go
//
// D09 · safety —— 恢复事件链路的守卫面（2026-11-07 探测时间点规划轮）。
//
// SF-01 恢复事件回调的 nil/零值安全：RearmCredentialFailures（F5 挂在四处
//       onQuotaRecovered 上）对 nil 执行面 / 非法 credID 必须静默 no-op——
//       恢复 tick 的 goroutine 有顶层 recover，但守卫在上游就该拦住 panic。
// SF-02 选模守卫：ResolveRecoveryProbeModel 对 nil DB / credID<=0 返回空不 panic
//       （F1 接线在恢复 tick 的异步 dispatch 里跑，panic 会击穿 dispatch）。
// SF-03 梯子保守性：未知/空 kind 走通用 7 步梯（有界 6h 封顶），不产生零/负间隔。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D09-node-state-selfcheck/safety/...

package safety

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestSafety_RearmCredentialFailuresNilGuards(t *testing.T) {
	var nilWorker *bg.NodeProbeWorker
	nilWorker.RearmCredentialFailures(7) // nil worker 不 panic
	w := &bg.NodeProbeWorker{}
	w.RearmCredentialFailures(0)  // 非法 credID no-op
	w.RearmCredentialFailures(-3) // 负数 no-op
	w.RearmCredentialFailures(7)  // 无 DB no-op
}

func TestSafety_ResolveRecoveryProbeModelNilGuards(t *testing.T) {
	m, err := bg.ResolveRecoveryProbeModel(context.Background(), nil, 7)
	if m != "" || err != nil {
		t.Errorf("nil db: want (\"\", nil), got (%q, %v)", m, err)
	}
	m, err = bg.ResolveRecoveryProbeModel(context.Background(), nil, 0)
	if m != "" || err != nil {
		t.Errorf("credID=0: want (\"\", nil), got (%q, %v)", m, err)
	}
}

func TestSafety_UnknownKindKeepsBoundedGenericLadder(t *testing.T) {
	var empty errorsx.ErrorKind
	for attempt := 1; attempt <= 30; attempt++ {
		d := bg.ProbeBackoffForKind(empty, attempt)
		if d <= 0 {
			t.Fatalf("empty kind attempt=%d 非正间隔 %v", attempt, d)
		}
		if d > 6*time.Hour {
			t.Fatalf("empty kind attempt=%d 间隔 %v 超出通用梯 6h 封顶", attempt, d)
		}
	}
}
