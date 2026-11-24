package examples

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// ★★★ 2026-11-25（12h 修订审计轮 P1 的防回归钉测）★★★
//
// ★★ 为什么需要这道钉测：★★
// ★★ 22fcf788c 把 CaptureResponse 做成「每个响应一个 goroutine」的 fire-and-forget
// ★★ （domains/hooks/externalhost/pipeline_integration.go）⇒ ★★ 同一个 hook 实例
// ★★ 会被**多个 goroutine 并发**调用 OnResponse ⇒ ★★ 而参考实现 ComplianceHook
// ★★ 的 AuditLog append 无锁 ⇒ ★★ `go test -race` 实测报 2 处 DATA RACE
// ★★ （Read 与 Previous write 同指 AuditLog 的 append 那一行）。
// ★★
// ★★ 后果不止「丢审计条目」：★ slice 头（len/cap/ptr）被并发改写会损坏
// ★★ ⇒ ★★ 可能 panic；★ 且这是 INTEGRATION_GUIDE 推荐的首个可部署钩子。
//
// ★★ 本钉测**必须**在 -race 下跑（`go test -race`）⇒ ★★ 去掉锁它必红。
func TestComplianceHookAuditLogIsRaceFree(t *testing.T) {
	hook := NewComplianceHook()

	const workers = 16
	const perWorker = 24

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				inv := &contract.Invocation{
					Request: &contract.RequestPayload{
						Body: contract.Payload{Body: []byte(`{"model":"m","messages":[]}`)},
					},
					Response: &contract.ResponsePayload{
						Body: contract.Payload{Body: []byte(`{"content":"ok"}`)},
					},
					Context: &contract.Context{
						RequestID: fmt.Sprintf("req-%d-%d", worker, i),
						Session:   fmt.Sprintf("sess-%d", worker),
						Model:     "m",
						State:     map[string]string{"model": "m"},
					},
				}
				hook.OnResponse(inv)
				// 读侧也并发跑（GetAuditReport 与 append 竞争）。
				_ = hook.GetAuditReport()
			}
		}(w)
	}
	wg.Wait()

	// 审计条目数必须**精确**等于并发写入的总数（无锁时这里会少）。
	if got, want := len(hook.AuditEntries()), workers*perWorker; got != want {
		t.Fatalf("audit log lost entries: got %d, want %d", got, want)
	}
}

// TestComplianceHookAuditReportCountsAllEntries 钉住读侧的统计口径。
func TestComplianceHookAuditReportCountsAllEntries(t *testing.T) {
	hook := NewComplianceHook()
	inv := &contract.Invocation{
		Request:  &contract.RequestPayload{Body: contract.Payload{Body: []byte(`{}`)}},
		Response: &contract.ResponsePayload{Body: contract.Payload{Body: []byte(`{}`)}},
		Context: &contract.Context{
			RequestID: "r1",
			Model:     "m",
			State:     map[string]string{"model": "m"},
		},
	}
	hook.OnResponse(inv)
	report := hook.GetAuditReport()
	if report == "" {
		t.Fatalf("empty audit report")
	}
	if !strings.Contains(report, "Total Requests: 1") {
		t.Fatalf("audit report does not report the recorded entry:\n%s", report)
	}
}
