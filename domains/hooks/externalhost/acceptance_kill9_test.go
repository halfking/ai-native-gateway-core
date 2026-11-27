package externalhost

// W3 验收③（方案 §4 W3 验收标准）：钩子进程 kill -9 后网关零错误直通 100 请求。
//
// 2026-11-27 验收轮：既有 TestOnRequestCrashPassesThrough 只验证单请求连接断开
// 的归因（FailureCrash + 原样直通）；本测试按方案原文做端到端验收——
//
//	真实 unix socket HTTP 服务器充当钩子进程 → kill -9 杀掉 →
//	随后 100 个请求全部零错误直通（Decision=Continue、无 block、无 panic）。
//
// 熔断器语义在本验收中一并覆盖：kill -9 后前 N 次请求真实调用失败（超时/崩溃），
// 熔断打开后剩余请求被跳过（circuit_open）——两者都满足"零错误直通"：
// 客户请求不受影响，钩子故障被收敛为可观测的跳过。

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/externalhost/contract"
)

// TestAcceptanceHookKilledThen100RequestsPassThrough 复现方案 W3 验收③：
// kill -9 后网关零错误直通 100 请求。
func TestAcceptanceHookKilledThen100RequestsPassThrough(t *testing.T) {
	// 1. 起一个真实 unix socket HTTP 钩子进程替身。
	// macOS 对 unix socket 路径有 104 字节硬限——t.TempDir() 路径太长会
	// bind: invalid argument，改用系统临时目录根下的短路径。
	socketPath := filepath.Join(os.TempDir(), "exthook-acceptance.sock")
	_ = os.Remove(socketPath) // 残留清理（上一轮崩溃留下的 socket 文件会 bind 失败）
	defer os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}

	var served atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc(contract.ContractPath+"/on_request", func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// 合同版本必须回显（宿主 ValidateReply 校验一致性）。
		_, _ = w.Write([]byte(`{"contract_version":"` + contract.ContractVersion + `","decision":"continue"}`))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	host := New(Config{SocketPath: socketPath, Timeout: contract.DefaultTimeout})

	// 2. kill 前的请求正常走钩子（确认链路活着）。
	out := host.OnRequest(context.Background(), localCtx(), &contract.RequestPayload{Method: "POST"})
	if out.Decision != contract.DecisionContinue || out.Failure != contract.FailureNone {
		t.Fatalf("pre-kill request should pass with hook, got %+v", out)
	}
	if served.Load() != 1 {
		t.Fatalf("hook should have served 1 request, served %d", served.Load())
	}

	// 3. kill -9 等价动作：硬关 listener + 立即关服务器（不优雅关闭）。
	_ = ln.Close()
	_ = srv.Close()

	// 4. kill 后 100 个请求：全部零错误直通。
	//
	// 熔断器语义下这可能分两段：熔断打开前的真实调用失败（FailureTimeout/
	// FailureCrash，Decision=Continue 原样直通；按 W3.2 这些请求的
	// OnRequestSucceeded=false ⇒ onResponse 不触发——这是合同语义，
	// 不算"请求错误"），以及熔断打开后的跳过（circuit_open，请求照常放行）。
	// 验收口径 = 100 个请求没有一个被 block、没有一个被改写、没有一个 panic。
	const total = 100
	integrator := NewPipelineIntegratorWithCircuit(host, DefaultCircuitThreshold, DefaultCircuitOpenWindow)

	for i := 0; i < total; i++ {
		rewritten, reqCtx, blockErr := integrator.InterceptRequest(
			context.Background(), localCtx(), &contract.RequestPayload{Method: "POST"})
		if blockErr != nil {
			t.Fatalf("request %d blocked after hook killed: %v", i+1, blockErr)
		}
		if rewritten != nil {
			t.Fatalf("request %d got unexpected rewrite: %+v", i+1, rewritten)
		}
		if reqCtx == nil {
			t.Fatalf("request %d got nil request context", i+1)
		}
	}

	// 5. 熔断确已打开（kill -9 后连续失败达到阈值）。
	if _, open := integrator.circuit.State(); !open {
		t.Fatal("circuit should be open after hook process killed and repeated failures")
	}

	// 6. 统计口径：100 请求中熔断跳过的比例可见（观测面验证）。
	stats := integrator.Stats()
	if stats.RequestTotal != total {
		t.Fatalf("request total = %d, want %d", stats.RequestTotal, total)
	}
	t.Logf("acceptance: %d requests, %d real failures before circuit opened, %d skipped by circuit",
		stats.RequestTotal, stats.RequestFailed, stats.RequestFailed)
}
