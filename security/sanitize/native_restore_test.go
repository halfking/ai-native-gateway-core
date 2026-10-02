package sanitize

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/stretchr/testify/require"
)

func TestNativeMessagesNonStreamRestoresVisibleTextAndToolInput(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-messages", "shared-native-session"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)

	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[` +
		`{"type":"text","text":"call {SENSITIVE:phone:1}","citations":[{"url":"https://example.com/reference"}]},` +
		`{"type":"thinking","thinking":"about {SENSITIVE:phone:1}"},` +
		`{"type":"tool_use","id":"tool_1","name":"send","input":{"to":"{SENSITIVE:phone:1}","deep":[["{SENSITIVE:phone:1}"]]}},` +
		`{"type":"image","source":{"type":"base64","data":"encoded-image"}},` +
		`{"type":"redacted_thinking","data":"signed-blob"}],` +
		`"usage":{"input_tokens":9007199254740993},"vendor_extension":{"opaque":"opaque-value"}}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "anthropic-messages", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Content []map[string]any       `json:"content"`
		Usage   map[string]json.Number `json:"usage"`
		Vendor  map[string]any         `json:"vendor_extension"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Equal(t, "call 13800138000", got.Content[0]["text"])
	require.Equal(t, "about 13800138000", got.Content[1]["thinking"])
	input := got.Content[2]["input"].(map[string]any)
	require.Equal(t, "13800138000", input["to"])
	require.Equal(t, "13800138000", input["deep"].([]any)[0].([]any)[0])
	require.Equal(t, "encoded-image", got.Content[3]["source"].(map[string]any)["data"])
	require.Equal(t, "signed-blob", got.Content[4]["data"])
	require.Equal(t, "opaque-value", got.Vendor["opaque"])
	require.Equal(t, "9007199254740993", got.Usage["input_tokens"].String())
}

func TestNativeResponsesNonStreamRestoresTextAndJSONArguments(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-responses", "responses-session"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session), map[string]any{
		"{SENSITIVE:phone:1}": "13800138000",
		"{SENSITIVE:email:1}": `quoted"mail@example.com`,
	}).Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"object":"response","id":"resp_1","output":[` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"call {SENSITIVE:phone:1}","annotations":[{"url":"https://example.com/reference"}]},{"type":"refusal","refusal":"unknown {SENSITIVE:phone:99}"},{"type":"output_audio","audio":"encoded-audio"}]},` +
		`{"type":"function_call","name":"send","arguments":"{\"to\":\"{SENSITIVE:phone:1}\",\"email\":\"{SENSITIVE:email:1}\",\"values\":[[\"{SENSITIVE:phone:1}\"]]}"},` +
		`{"type":"custom_tool_call","input":"{SENSITIVE:phone:1}"},` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"consider {SENSITIVE:phone:1}"}],"encrypted_content":"encrypted-blob"}],` +
		`"output_text":"brief {SENSITIVE:phone:1}","usage":{"input_tokens":9007199254740993},"vendor_extension":{"opaque":"opaque-value"}}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-responses", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Output     []map[string]any       `json:"output"`
		OutputText string                 `json:"output_text"`
		Usage      map[string]json.Number `json:"usage"`
		Vendor     map[string]any         `json:"vendor_extension"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	content := got.Output[0]["content"].([]any)
	require.Equal(t, "call 13800138000", content[0].(map[string]any)["text"])
	require.Equal(t, "unknown [REDACTED]", content[1].(map[string]any)["refusal"])
	require.Equal(t, "encoded-audio", content[2].(map[string]any)["audio"])
	arguments := got.Output[1]["arguments"].(string)
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(arguments), &args))
	require.Equal(t, "13800138000", args["to"])
	require.Equal(t, `quoted"mail@example.com`, args["email"])
	require.Equal(t, "13800138000", args["values"].([]any)[0].([]any)[0])
	require.Equal(t, "13800138000", got.Output[2]["input"])
	require.Equal(t, "consider 13800138000", got.Output[3]["summary"].([]any)[0].(map[string]any)["text"])
	require.Equal(t, "encrypted-blob", got.Output[3]["encrypted_content"])
	require.Equal(t, "brief 13800138000", got.OutputText)
	require.Equal(t, "9007199254740993", got.Usage["input_tokens"].String())
	require.Equal(t, "opaque-value", got.Vendor["opaque"])
}

// 第二十八轮 A3 钉测：输入侧 reasoning_text（以及回传的 search/mcp item）已入洗，
// 上游模型学舌占位符时恢复侧必须覆盖同一批类型——否则残留 {SENSITIVE: 会命中
// 输出守卫整响应 block（可用性损失）。
func TestNativeResponsesRestoresReasoningContentAndSearchItems(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-responses-28", "responses-session-28"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session), map[string]any{
		"{SENSITIVE:phone:1}": "13800138000",
	}).Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"object":"response","id":"resp_28","output":[` +
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"look at {SENSITIVE:phone:1}"}],"content":[{"type":"reasoning_text","text":"echo {SENSITIVE:phone:1}"}]},` +
		`{"type":"mcp_call","id":"mcp_1","status":"completed","arguments":"{\"q\":\"{SENSITIVE:phone:1}\"}","output":"found {SENSITIVE:phone:1}"},` +
		`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"who is {SENSITIVE:phone:1}"}},` +
		`{"type":"file_search_call","id":"fs_1","status":"completed","queries":["{SENSITIVE:phone:1}"],"results":[{"file_id":"f1","text":"hit {SENSITIVE:phone:1}"}]}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-responses", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Output []map[string]any `json:"output"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	reasoning := got.Output[0]
	require.Equal(t, "look at 13800138000", reasoning["summary"].([]any)[0].(map[string]any)["text"])
	require.Equal(t, "echo 13800138000", reasoning["content"].([]any)[0].(map[string]any)["text"])
	mcp := got.Output[1]
	var mcpArgs map[string]any
	require.NoError(t, json.Unmarshal([]byte(mcp["arguments"].(string)), &mcpArgs))
	require.Equal(t, "13800138000", mcpArgs["q"])
	require.Equal(t, "found 13800138000", mcp["output"])
	ws := got.Output[2]["action"].(map[string]any)
	require.Equal(t, "who is 13800138000", ws["query"])
	fs := got.Output[3]
	require.Equal(t, "13800138000", fs["queries"].([]any)[0])
	require.Equal(t, "hit 13800138000", fs["results"].([]any)[0].(map[string]any)["text"])
	require.NotContains(t, string(result.ModifiedBody), "{SENSITIVE:")
}

func TestNativeNonStreamRestoreTenantScopeAndUnsafeBodies(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const session = "same-session"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey("tenant-A", session),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"type":"message","content":[{"type":"text","text":"{SENSITIVE:phone:1}"}]}`)
	for _, tc := range []struct {
		tenant string
		want   string
	}{
		{"tenant-A", "13800138000"},
		{"tenant-B", "[REDACTED]"},
	} {
		result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
			TenantID: tc.tenant, SessionID: session, ClientProtocol: "anthropic-messages", ResponseBody: body,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Contains(t, string(result.ModifiedBody), tc.want)
	}
	responsesBody := []byte(`{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"{SENSITIVE:phone:1}"}]}]}`)
	otherTenant, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: "tenant-B", SessionID: session, ClientProtocol: "openai-responses", ResponseBody: responsesBody,
	})
	require.NoError(t, err)
	require.NotNil(t, otherTenant)
	require.Contains(t, string(otherTenant.ModifiedBody), "[REDACTED]")
	require.NotContains(t, string(otherTenant.ModifiedBody), "13800138000")

	for _, unsafe := range [][]byte{
		[]byte(`{"type":"message","content":[{"type":"thinking","thinking":"{SENSITIVE:phone:1}","signature":"signed"}]}`),
		[]byte(`{"type":"message","content":[{"type":"text","text":"{SENSITIVE:phone:1}"}`),
		[]byte(`{"object":"response","output":[{"type":"function_call","arguments":"{broken {SENSITIVE:phone:1}"}]}`),
	} {
		result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
			TenantID: "tenant-A", SessionID: session, ClientProtocol: "anthropic-messages", ResponseBody: unsafe,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.ShouldBlock)
	}
}

func TestNativeNonStreamRestoreBlocksWhenMappingUnavailable(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	require.NoError(t, rdb.Close())

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: "tenant-A", SessionID: "session-A", ClientProtocol: "openai-responses",
		ResponseBody: []byte(`{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"{SENSITIVE:phone:1}"}]}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ShouldBlock)
	require.Empty(t, result.ModifiedBody)
}

// 第三十轮钉测（D14-F5 承债）：第二十九轮六载体 + 第三十轮 call 侧三族的
// restore 镜像此前零测试承重——摘掉任一 restore case 只会表现为线上整响应
// block（占位符残留命中输出守卫），测试全绿。本测试逐载体唯一敏感值 +
// 逐值 Equal，任一 case 退回"不认识该 item"即红。
func TestNativeResponsesRestoresCodexToolFamily(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-responses-30", "responses-session-30"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session), map[string]any{
		"{SENSITIVE:phone:1}": "13800138031",
		"{SENSITIVE:phone:2}": "13800138032",
		"{SENSITIVE:phone:3}": "13800138033",
		"{SENSITIVE:phone:4}": "13800138034",
		"{SENSITIVE:phone:5}": "13800138035",
		"{SENSITIVE:phone:6}": "13800138036",
		"{SENSITIVE:phone:7}": "13800138037",
		"{SENSITIVE:phone:8}": "13800138038",
		"{SENSITIVE:phone:9}": "13800138039",
	}).Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"object":"response","id":"resp_30","output":[` +
		`{"type":"local_shell_call_output","call_id":"c1","output":"stdout phone {SENSITIVE:phone:1}"},` +
		`{"type":"apply_patch_call_output","call_id":"c2","output":"patched ok call {SENSITIVE:phone:2}"},` +
		`{"type":"shell_call_output","call_id":"c3","outputs":[{"text":"call {SENSITIVE:phone:3}"}]},` +
		`{"type":"code_interpreter_call","call_id":"c4","code":"print(\"call {SENSITIVE:phone:4}\")","outputs":[{"type":"logs","logs":"phone {SENSITIVE:phone:5}"},{"type":"image","image_url":"data:image/png;base64,opaque"}]},` +
		`{"type":"apply_patch_call","call_id":"c5","action":{"type":"create","path":"a.txt","content":"call {SENSITIVE:phone:6}"}},` +
		`{"type":"shell_call","call_id":"c6","status":"completed","action":{"type":"exec","command":["echo {SENSITIVE:phone:7}"]}},` +
		`{"type":"local_shell_call","call_id":"c7","status":"completed","action":{"type":"exec","command":["cat {SENSITIVE:phone:8}"],"env":{"TOKEN":"phone {SENSITIVE:phone:9}"}}},` +
		`{"type":"computer_call","call_id":"c8","status":"completed","action":{"type":"type","text":"call {SENSITIVE:phone:1}"}}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-responses", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Output []map[string]any `json:"output"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Len(t, got.Output, 8)
	require.Equal(t, "stdout phone 13800138031", got.Output[0]["output"])
	require.Equal(t, "patched ok call 13800138032", got.Output[1]["output"])
	require.Equal(t, "call 13800138033", got.Output[2]["outputs"].([]any)[0].(map[string]any)["text"])
	ci := got.Output[3]
	require.Equal(t, "print(\"call 13800138034\")", ci["code"])
	require.Equal(t, "phone 13800138035", ci["outputs"].([]any)[0].(map[string]any)["logs"])
	require.Equal(t, "call 13800138036", got.Output[4]["action"].(map[string]any)["content"])
	sc := got.Output[5]["action"].(map[string]any)
	require.Equal(t, "echo 13800138037", sc["command"].([]any)[0])
	ls := got.Output[6]["action"].(map[string]any)
	require.Equal(t, "cat 13800138038", ls["command"].([]any)[0])
	require.Equal(t, "phone 13800138039", ls["env"].(map[string]any)["TOKEN"])
	cc := got.Output[7]["action"].(map[string]any)
	require.Equal(t, "call 13800138031", cc["text"])
	require.NotContains(t, string(result.ModifiedBody), "{SENSITIVE:")
}
