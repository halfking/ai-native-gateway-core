package outputcompliance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// 195 号审计（全面审计v3/2026-10-03/195）：e0625c6a5 把
// tool_search_call / mcp_approval_request 收进了「双侧」——
// security/sanitize/input_protocols.go:297（入向洗）与
// security/sanitize/native_restore.go:188,196（出向还原）都新增了这两个载体。
//
// 但输出合规 lane 是**第三份**载体枚举，提交漏了它：
//   - domains/hooks/outputcompliance/protocol_text.go 的 addOutputItem
//     （非流式整体 body）
//   - 同文件 response.output_item.added 分支（流式 added 帧）
// 两处 switch 都没有这两个 type ⇒ 模型把敏感内容写进这两个载体的
// arguments 时，两道输出闸都看不见它。
//
// 本文件是判据：先在未修复代码上跑（必须红），修复后跑（必须绿）。
// 判据取「敏感值确实被改写」而不是「lane 存在」——后者会被
// 结构性改动绕过（只加空 case 也能过）。

// TestProtocolTextCoversEdgeToolCarrierLanes 覆盖非流式整体 body。
func TestProtocolTextCoversEdgeToolCarrierLanes(t *testing.T) {
	body := `{"object":"response","id":"resp","output":[` +
		`{"type":"tool_search_call","id":"ts1","arguments":"{\"phone\":\"13800138101\"}"},` +
		`{"type":"mcp_approval_request","id":"ma1","arguments":"{\"phone\":\"13800138102\"}"}]}`
	checker := &protocolChecker{}
	it := NewOutputComplianceInterceptor(checker, nil)
	result, err := it.processBody(context.Background(), &response.InterceptRequest{
		TenantID: "t", SessionID: "s", ResponseBody: []byte(body),
	})
	if err != nil {
		t.Fatalf("processBody: %v", err)
	}
	if result.ShouldBlock {
		t.Fatalf("unexpected block: %+v", result)
	}
	if len(result.ModifiedBody) == 0 {
		t.Fatalf("edge tool-carrier lane not inspected at all (observe-only): %+v", result)
	}
	got := string(result.ModifiedBody)
	for _, secret := range []string{
		"13800138101", // tool_search_call.arguments
		"13800138102", // mcp_approval_request.arguments
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("edge tool-carrier lane leaked %s: %s", secret, got)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.ModifiedBody, &decoded); err != nil {
		t.Fatalf("redacted body is not valid JSON: %v", err)
	}
	if decoded["id"] != "resp" {
		t.Fatalf("protocol metadata changed: %v", decoded["id"])
	}
}

// TestStreamCollectCoversEdgeToolCarrierItemFrames 覆盖流式 added 帧与
// done 快照。只修非流式会留下「流式直过」的半帧缝，所以两帧都要钉。
func TestStreamCollectCoversEdgeToolCarrierItemFrames(t *testing.T) {
	transform := func(label, original string) (string, int, bool, error) {
		loc := testPhone.FindStringIndex(original)
		if loc == nil {
			return original, 0, false, nil
		}
		return original[:loc[0]] + "[PHONE]" + original[loc[1]:], 1, false, nil
	}
	for _, tc := range []struct{ name, event string }{
		{
			name:  "added",
			event: `{"type":"response.output_item.added","output_index":0,"item":{"type":"tool_search_call","id":"ts1","arguments":"{\"phone\":\"13800138111\"}"}}`,
		},
		{
			name:  "done",
			event: `{"type":"response.output_item.done","output_index":0,"item":{"type":"mcp_approval_request","id":"ma1","arguments":"{\"phone\":\"13800138112\"}"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, issues, blocked, err := transformVisibleJSON([]byte(tc.event), true, false, transform)
			if err != nil || blocked {
				t.Fatalf("out=%s issues=%d blocked=%v err=%v", out, issues, blocked, err)
			}
			if issues == 0 {
				t.Fatalf("edge tool-carrier item frame not inspected (issues=0): %s", tc.event)
			}
			if strings.Contains(string(out), "1380013811") {
				t.Fatalf("edge tool-carrier frame leaked: %s", out)
			}
		})
	}
}
