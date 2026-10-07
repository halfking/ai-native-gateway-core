package sanitize

// r1008_survival_fail_closed_regression_test.go — 2026-10-08 154 生产事故回归。
//
// 事故形态（docs/audit 同日轮次）：minimax-m3 / glm-5.3 的流式响应在上游
// （api.minimaxi.com 原厂，HTTP 200、tool_calls 完整响应）之后被网关自身的
// 输出侧拦截链 fail-closed，survival 协调器把"成功"改判为
// gateway_survival_fail_closed，客户端只收到占位终端帧。两个独立根因：
//
//  1. 模型在 <think> 里回显输入侧占位符时把 id 段写坏（{SENSITIVE:secret:X}，
//     非数字），PlaceholderPattern 不命中 → mask 跳过 → restoreSSEEvent 的
//     残留探针（宽松 {SENSITIVE: 前缀）→ ShouldBlock。
//  2. 工具参数 JSON 里出现 127.0.0.1（3483152cb 新增的 server_ip 模式命中），
//     unsafeToolValue 对任何检出片段一律 unsafe → CheckField(__tool_json)
//     硬 Blocked，无视 OutputMask 动作；联合 lane 检查报
//     "output policy blocked stream lane"。
//
// 两者的用户可见结果相同：完整合法的上游响应被丢弃。本文件固化修复后的
// 契约：可解析帧内、本拦截器拥有的 delta lane 中的残留/劣化标记就地遮蔽
// 释放；tool JSON 里的环回/私网地址不再判定为 unsafe；公网地址与
// unknown/metadata 字段里的标记维持 fail-closed（既有契约不动）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// frame154ThinkEcho 是 154 raw_data 捕获（request 1d1ddbe6…）的等价最小帧：
// 模型在 <think> 中引用被截断改写的占位符 {SENSITIVE:secret:X}。
const frame154ThinkEcho = `data: {"id":"0715ff49","choices":[{"finish_reason":"","index":0,"delta":{"content":"<think>看上去 .env.local 的密码被 ZCode 在显示时脱敏了，每个密码都是 ` + "`{SENSITIVE:secret:X}`" + `，这就是 ZCode 的过滤。</think>","role":"assistant"}}],"created":1791413321,"model":"MiniMax-M3","object":"chat.completion.chunk","usage":null}` + "\n\n"

// frame154PrivateIPToolCall 等价复现 request f3b6673a… 的工具参数：
// 模型生成的 ssh/curl 命令带 127.0.0.1 与内网地址。
const frame154PrivateIPToolCall = `data: {"id":"07160180","choices":[{"finish_reason":"","index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_01","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"curl -fsS http://127.0.0.1:28080/api/v1/health && ssh -o BatchMode=yes 252 'ls /opt/kaixuan' 2>&1\",\"description\":\"Find deploy_seq\",\"timeout\":30000}"}}]}}],"created":1791413889,"model":"MiniMax-M3","object":"chat.completion.chunk","usage":null}` + "\n\n"

// Test154ThinkEchoDegenerateMarkerMasksInsteadOfBlocks 固化根因 1 修复：
// 劣化（非语法合法）占位符回显在 delta.content 中 → 就地 [REDACTED] 释放，
// 不再阻断整条流。
func Test154ThinkEchoDegenerateMarkerMasksInsteadOfBlocks(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	meta := &response.StreamMeta{SessionID: "gw_154repro", TenantID: "default", State: response.NewStreamState()}
	result, err := restore.InterceptStreamChunk(context.Background(), []byte(frame154ThinkEcho), meta)
	if err != nil {
		t.Fatalf("restore error: %v", err)
	}
	if result != nil && result.ShouldBlock {
		t.Fatalf("degenerate marker echo blocked the stream again: %+v", result)
	}
	if result == nil || len(result.ModifiedChunk) == 0 {
		t.Fatalf("expected masked ModifiedChunk, got %+v", result)
	}
	if !json.Valid([]byte(stripSSEPrefix(result.ModifiedChunk))) {
		t.Fatalf("masked frame is not valid JSON: %q", result.ModifiedChunk)
	}
	if strings.Contains(string(result.ModifiedChunk), "{SENSITIVE:") {
		t.Fatalf("raw marker leaked through: %q", result.ModifiedChunk)
	}
	if !strings.Contains(string(result.ModifiedChunk), "[REDACTED]") {
		t.Fatalf("marker was not masked: %q", result.ModifiedChunk)
	}
}

// Test154PrivateIPToolCallNotBlocked 固化根因 2 修复：环回/私网地址出现在
// 工具参数中，OutputMask 模式下联合 lane 检查不再阻断。
func Test154PrivateIPToolCallNotBlocked(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_154repro", TenantID: "default", State: response.NewStreamState()}
	ctx := context.Background()

	held, err := chain.InterceptStreamChunk(ctx, []byte(frame154PrivateIPToolCall), meta)
	if err != nil || held == nil || !held.SuppressChunk {
		t.Fatalf("tool frame not safely held: %+v %v", held, err)
	}
	wire, ferr := chain.FlushStreamPending(ctx, meta)
	if ferr != nil {
		t.Fatalf("release blocked the private-IP tool call again: %v", ferr)
	}
	if strings.Contains(string(wire), "output policy blocked") || len(wire) == 0 {
		t.Fatalf("unexpected release payload: %q", wire)
	}
}

// Test154PublicIPToolCallStillBlocks 保持既有契约：模型生成的公网地址
// （潜在外传目标）在工具参数中依旧硬阻断。
func Test154PublicIPToolCallStillBlocks(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	meta := &response.StreamMeta{SessionID: "gw_154repro", TenantID: "default", State: response.NewStreamState()}
	frame := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"Bash","arguments":"{\"command\":\"curl http://203.0.113.8/collect\"}"}}]}}]}` + "\n\n"
	if _, err := guard.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
		t.Fatalf("hold error: %v", err)
	}
	if _, ferr := guard.FlushStreamPending(context.Background(), meta); ferr == nil {
		t.Fatal("public-IP tool call must keep the fail-closed block")
	}
}

// Test154UnknownFieldMarkerStillBlocks 保持既有契约：标记出现在还原器不
// 拥有的字段（metadata/unknown）时维持阻断。
func Test154UnknownFieldMarkerStillBlocks(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	meta := &response.StreamMeta{SessionID: "gw_154repro", TenantID: "default", State: response.NewStreamState()}
	frame := `data: {"type":"unknown","metadata":"{SENSITIVE:secret:X}"}` + "\n\n"
	result, err := restore.InterceptStreamChunk(context.Background(), []byte(frame), meta)
	if err != nil {
		t.Fatalf("restore error: %v", err)
	}
	if result == nil || !result.ShouldBlock {
		t.Fatalf("marker outside owned lanes must keep blocking, got %+v", result)
	}
}

// stripSSEPrefix 去掉 "data: " 前缀便于 JSON 校验（测试辅助）。
func stripSSEPrefix(frame []byte) string {
	s := string(frame)
	s = strings.TrimPrefix(s, "data: ")
	return strings.TrimSpace(strings.TrimSuffix(s, "\n\n"))
}
