package outputcompliance

// 2026-11-20 211 生产事件回归（request d2b7d79a… / trace 20e262a1…，
// glm-5.3-flash via apiclaude.cc，client_request_id dc450968…）。
//
// ## 事件形态
//
// 上游在工具参数增量发到一半时断流（executor 已按 upstream_down 分类记账：
// "anthropic stream: upstream mid-stream JSON error" / "non-resumable stream
// interrupted"），finish() 的 FlushStreamPending 把 lane
// `anthropic.block.1.partial_json` 里只累积到 **2 字节**的截断前缀（`{"`）
// 当「上游发来的完整参数」判 json.Valid ⇒ tool_json_invalid fail-closed ⇒
// ① 客户端可见终帧被换成 output_policy_blocked（retryable=false + 「报告
// 网关管理员」），把本该呈现的上游断流错误顶掉；② 审核面收到一张「策略
// 拦截」单，而真因（上游中断）executor 早已记过账。
//
// ## 契约（三条一起钉）
//
//  1. 断流（无终帧 flush）+ 截断参数 lane ⇒ **放行不判**：流本身已在失败，
//     策略判决只会覆盖真错误归因；
//  2. 完整收尾（有终帧）+ 非法参数 ⇒ **照拦**：tool_json_invalid 的合同是
//     「上游**发完了**的参数不是合法 JSON」（rv-2089 形态），不因本修复松动；
//  3. 断流 + 真实规则命中（凭据/外传目的地）⇒ **照拦**：豁免只认
//     tool_json_invalid 这一种「格式判不出」判决，内容规则不被断流豁免。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	compliance "github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// incidentChecker 复刻 security/sanitize outputSensitiveChecker.CheckField 在
// `__tool_json@*` 标签上的两段合同：非法 JSON ⇒ tool_json_invalid 守卫判决；
// 合法 JSON ⇒ 走内容检查（这里用 HARD_BLOCK_SHAPE 模拟真实规则硬拦）。
type incidentChecker struct{}

func (c *incidentChecker) Check(_ context.Context, _, text string) (*compliance.ComplianceResult, error) {
	return &compliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
}

func (c *incidentChecker) CheckField(_ context.Context, _, label, text string) (*compliance.ComplianceResult, error) {
	if label == "__tool_fragment" {
		// 与 sanitize 侧同款：增量片段不可独立判定，终态合并后再查。
		return &compliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	if strings.HasPrefix(label, "__tool_json") && !json.Valid([]byte(text)) {
		return &compliance.ComplianceResult{
			Blocked: true,
			Issues: []compliance.ComplianceIssue{{
				Type: "sensitive", Subtype: "tool_json_invalid", Severity: 8,
				Location: "$", RuleID: GuardToolJSONInvalidRuleID,
			}},
		}, nil
	}
	if strings.Contains(text, "HARD_BLOCK_SHAPE") {
		return &compliance.ComplianceResult{
			Blocked: true,
			Issues: []compliance.ComplianceIssue{{
				Type: "sensitive", Subtype: "secret", Severity: 8, RuleID: "test.hard_block",
			}},
		}, nil
	}
	return &compliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
}

// incidentAnthropicFrames 产出一对生产形态的帧：tool_use 声明帧（Bash，index 1）
// + 一段 input_json_delta 增量（partialJSON 就是被截断的参数前缀）。
func incidentAnthropicFrames(partialJSON string) [][]byte {
	declare, _ := json.Marshal(map[string]any{
		"type": "content_block_start", "index": 1,
		"content_block": map[string]any{"type": "tool_use", "id": "toolu_incident", "name": "Bash", "input": map[string]any{}},
	})
	delta, _ := json.Marshal(map[string]any{
		"type": "content_block_delta", "index": 1,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partialJSON},
	})
	return [][]byte{
		[]byte("event: content_block_start\ndata: " + string(declare) + "\n\n"),
		[]byte("event: content_block_delta\ndata: " + string(delta) + "\n\n"),
	}
}

func incidentInterceptor(t *testing.T) (*OutputComplianceInterceptor, *response.StreamMeta) {
	t.Helper()
	it := NewOutputComplianceInterceptor(&incidentChecker{}, nil)
	meta := &response.StreamMeta{TenantID: "t", RequestID: "req-incident", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	return it, meta
}

// 契约 1：断流（无终帧）+ 截断参数 lane ⇒ FlushStreamPending 放行、不产策略判决。
func TestInterruptedStreamTruncatedToolLaneReleasesNotBlocks(t *testing.T) {
	it, meta := incidentInterceptor(t)
	for _, frame := range incidentAnthropicFrames(`{"`) {
		result, err := it.processStreamChunk(context.Background(), frame, meta)
		if err != nil {
			t.Fatalf("frame=%q err=%v", frame, err)
		}
		_ = result
	}
	released, err := it.FlushStreamPending(context.Background(), meta)
	if err != nil {
		t.Fatalf("断流截断 lane 不得产策略判决（生产事件：把 upstream_down 顶成了 output_policy_blocked）: %v", err)
	}
	if !strings.Contains(string(released), "partial_json") {
		t.Fatalf("截断 lane 的帧应原样放行: %q", released)
	}
}

// 契约 2：完整收尾（message_stop 终帧）+ 同一截断前缀 ⇒ 仍按
// tool_json_invalid fail-closed（上游发完了还非法才是格式违规）。
func TestCompletedStreamMalformedToolJSONStillBlocks(t *testing.T) {
	it, meta := incidentInterceptor(t)
	for _, frame := range incidentAnthropicFrames(`{"`) {
		if _, err := it.processStreamChunk(context.Background(), frame, meta); err != nil {
			t.Fatalf("frame=%q err=%v", frame, err)
		}
	}
	terminal := []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	result, err := it.processStreamChunk(context.Background(), terminal, meta)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if result == nil || !result.ShouldBlock {
		t.Fatalf("终帧收尾 + 非法参数必须照拦: %+v", result)
	}
	if got := result.Metadata["policy_rule"]; got != GuardToolJSONInvalidRuleID {
		t.Fatalf("policy_rule=%v，期望 %s", got, GuardToolJSONInvalidRuleID)
	}
}

// 契约 3：断流（无终帧）+ 合法 JSON 参数里的真实规则命中 ⇒ 豁免不得扩大到内容规则。
func TestInterruptedStreamRealRuleHitStillBlocks(t *testing.T) {
	it, meta := incidentInterceptor(t)
	for _, frame := range incidentAnthropicFrames(`{"command":"echo HARD_BLOCK_SHAPE"}`) {
		if _, err := it.processStreamChunk(context.Background(), frame, meta); err != nil {
			t.Fatalf("frame=%q err=%v", frame, err)
		}
	}
	_, err := it.FlushStreamPending(context.Background(), meta)
	if err == nil {
		t.Fatal("断流不豁免真实规则命中（凭据/外传目的地照拦）")
	}
	if got := errorsx.PolicyBlockRuleID(err); got != "test.hard_block" {
		t.Fatalf("rule=%q，期望 test.hard_block", got)
	}
}

// incidentResponsesFrames 产出 Responses 协议形态的帧：output_item.added 声明
// function_call（Bash）+ 一段 function_call_arguments.delta（截断前缀）。
func incidentResponsesFrames(partialArgs string) [][]byte {
	added, _ := json.Marshal(map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "function_call", "id": "fc_incident", "name": "Bash", "arguments": ""},
	})
	delta, _ := json.Marshal(map[string]any{
		"type": "response.function_call_arguments.delta", "item_id": "fc_incident", "output_index": 0,
		"delta": partialArgs,
	})
	return [][]byte{
		[]byte("event: response.output_item.added\ndata: " + string(added) + "\n\n"),
		[]byte("event: response.function_call_arguments.delta\ndata: " + string(delta) + "\n\n"),
	}
}

// 契约 1b（失败终帧族，2026-11-20 L1 收口）：response.failed / response.incomplete
// 是「响应没写完」的失败终帧 ⇒ 参数 lane 是截断前缀 ⇒ 不判 tool_json_invalid，
// 失败终帧本身照常转发（客户端要看到真实失败原因）。
func TestFailedTerminalTruncatedToolLaneReleasesNotBlocks(t *testing.T) {
	for _, terminalType := range []string{"response.failed", "response.incomplete"} {
		t.Run(terminalType, func(t *testing.T) {
			it, meta := incidentInterceptor(t)
			for _, frame := range incidentResponsesFrames(`{"`) {
				if _, err := it.processStreamChunk(context.Background(), frame, meta); err != nil {
					t.Fatalf("frame=%q err=%v", frame, err)
				}
			}
			terminal, _ := json.Marshal(map[string]any{"type": terminalType})
			result, err := it.processStreamChunk(context.Background(),
				[]byte("event: "+terminalType+"\ndata: "+string(terminal)+"\n\n"), meta)
			if err != nil {
				t.Fatalf("失败终帧不得把截断 lane 判成 tool_json_invalid: %v", err)
			}
			if result == nil || result.ShouldBlock {
				t.Fatalf("失败终帧 + 截断参数必须放行（转发失败终帧）: %+v", result)
			}
			wire := result.ModifiedChunk
			if !strings.Contains(string(wire), terminalType) {
				t.Fatalf("失败终帧必须照常转发: %q", wire)
			}
			if !strings.Contains(string(wire), "function_call_arguments.delta") {
				t.Fatalf("截断参数帧应随失败终帧一并释放: %q", wire)
			}
		})
	}
}

// 契约 2b：Responses 协议**正常收尾**（response.completed）+ 截断/非法参数 ⇒
// 仍按 tool_json_invalid fail-closed（上游写完了还非法才是格式违规）。
func TestResponsesCompletedMalformedToolJSONStillBlocks(t *testing.T) {
	it, meta := incidentInterceptor(t)
	for _, frame := range incidentResponsesFrames(`{"`) {
		if _, err := it.processStreamChunk(context.Background(), frame, meta); err != nil {
			t.Fatalf("frame=%q err=%v", frame, err)
		}
	}
	completed, _ := json.Marshal(map[string]any{"type": "response.completed"})
	result, err := it.processStreamChunk(context.Background(),
		[]byte("event: response.completed\ndata: "+string(completed)+"\n\n"), meta)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if result == nil || !result.ShouldBlock {
		t.Fatalf("response.completed 收尾 + 非法参数必须照拦: %+v", result)
	}
	if got := result.Metadata["policy_rule"]; got != GuardToolJSONInvalidRuleID {
		t.Fatalf("policy_rule=%v，期望 %s", got, GuardToolJSONInvalidRuleID)
	}
}
