package sanitize

// /v1/responses 入站「占位符→回显→还原」端到端钉测（2026-11-13 24h 审计轮④，
// 收口上轮登记「批5 后 responses 入站缺端到端钉测」）。
//
// # 这条链此前缺的是什么
//
// /v1/responses 的**两半各自有钉**：入站半（input_protocols_test.go 的
// sanitizeEnvelope("/v1/responses") 用例）只断言「请求体里的 PII 变成了
// 占位符」；出站半（native_restore_test.go 的 openai-responses 用例）**手工
// HSet 映射表**再调还原拦截器——映射键是测试自己写的，不是入站腿产出的。
// ⇒ 两半之间（入站写出的占位符/映射键格式 ↔ 出站还原读的键格式）没有
// 任何量具咬合：批5 把 responses 入站迁到 IR 主路径时若改了入站键格式，
// 出站半照样全绿（键格式漂移不可见，§704「守卫在缺陷处没有牙」同构）。
//
// # 本文件的端到端性质（与既有钉的差别）
//
//   - 入站腿：**真实** SanitizeInputMiddleware（含 Redis 映射落盘）跑
//     /v1/responses 请求——映射表只由这一腿写入，测试不手工 HSet；
//   - 回显腿：模型视角，把入站改写后的**真实占位符**（从改写体里提取，
//     不硬编码索引）放进 responses 形状的输出；
//   - 还原腿：**真实** SanitizeRestoreInterceptor 按同一 (tenant, session)
//     读 Redis 映射还原——键格式漂移在此当场红。
//   - fail-closed 臂：模型**伪造**映射里不存在的占位符（键序号越界）⇒
//     recognized responses 形状 + 残余标记 ⇒ 按设计整流拦断（ShouldBlock）。
//
// 覆盖两条输出车道：output_text 可见文本 + function_call.arguments
// （tool 车道的还原对 responses 桥的 arguments 流尤其要紧——占位符进参数
// 是 ZCode/bash 类客户端的常态路径）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/redis/go-redis/v9"
)

const responsesE2ESession = "session-e2e-responses"

// runResponsesInboundLeg 跑真实入站中间件，返回（改写体、捕获到的占位符）。
func runResponsesInboundLeg(t *testing.T, rdb *redis.Client, inputText string) ([]byte, string) {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	mw, err := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	var captured []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read sanitized body: %v", err)
		}
		captured = raw
		w.WriteHeader(http.StatusOK)
	})
	handler := mw.Wrap(inner)
	body := `{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` + inputText + `"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("X-Gw-Session-Id", responsesE2ESession)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound leg returned %d (want 200); body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("inner handler never ran — middleware swallowed the request")
	}
	// 入站半断言：PII 不在改写体里（占位符在）。
	if strings.Contains(string(captured), inputText) {
		t.Fatalf("入站腿未脱敏：PII 原文仍在改写体中:\n%s", captured)
	}
	ph := extractPlaceholderForTest(t, captured)
	if ph == "" {
		t.Fatalf("改写体中没有可提取的占位符:\n%s", captured)
	}
	return captured, ph
}

// extractPlaceholderForTest 从改写体里提取第一个 {SENSITIVE:...} 占位符
// （JSON 字符串转义后的形态也是逐字提取——占位符词表不含引号字符）。
func extractPlaceholderForTest(t *testing.T, body []byte) string {
	t.Helper()
	const open = "{SENSITIVE:"
	start := strings.Index(string(body), open)
	if start < 0 {
		return ""
	}
	end := strings.Index(string(body)[start:], "}")
	if end < 0 {
		t.Fatalf("占位符起始标记在场但闭合花括号缺失:\n%s", body)
	}
	return string(body)[start : start+end+1]
}

func TestResponsesInboundEchoRestoreEndToEnd(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	inputText := "my phone is 13800138000, call me back"

	// ── 入站腿（真实中间件；映射只由它写入）──
	_, ph := runResponsesInboundLeg(t, rdb, inputText)

	// ── 回显腿：模型把占位符回显进 responses 输出的两条车道 ──
	// arguments 字段是「JSON 字符串里再套一层 JSON」⇒ 内层引号要按外层
	// JSON 规则转义成 \"（裸字符串里 \\ 不转义，必须显式拼）。
	escapedArgs := strings.ReplaceAll(`{"number":"`+ph+`"}`, `"`, `\"`)
	echoBody := []byte(`{` +
		`"object":"response",` +
		`"id":"resp-e2e",` +
		`"output":[` +
		`{"type":"message","role":"assistant","id":"msg-1","status":"completed","content":[{"type":"output_text","text":"sure, dialing ` + ph + ` now"}]},` +
		`{"type":"function_call","call_id":"call-1","name":"dial","arguments":"` + escapedArgs + `"}` +
		`],` +
		`"status":"completed"}`)

	// ── 还原腿（真实拦截器；读入站腿写的同一份 Redis 映射）──
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatalf("restore interceptor: %v", err)
	}
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID:       "_unknown",
		SessionID:      responsesE2ESession,
		ClientProtocol: "openai-responses",
		ResponseBody:   echoBody,
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	if result == nil {
		t.Fatal("restore leg returned nil result — placeholder echo passed through unrestored")
	}
	if result.ShouldBlock {
		t.Fatalf("已知占位符的回显不得拦断（映射由入站腿写入、键应可还原）: %+v", result)
	}
	out := string(result.ModifiedBody)
	if out == "" {
		t.Fatal("restore leg produced empty body")
	}
	var got struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(result.ModifiedBody, &got); err != nil {
		t.Fatalf("decode restored body: %v", err)
	}
	var textLane, argLane string
	for _, item := range got.Output {
		switch item.Type {
		case "message":
			if len(item.Content) > 0 {
				textLane = item.Content[0].Text
			}
		case "function_call":
			argLane = item.Arguments
		}
	}
	if !strings.Contains(textLane, "13800138000") || strings.Contains(textLane, ph) {
		t.Fatalf("文本车道还原不完整: %q", textLane)
	}
	var argObj map[string]any
	if err := json.Unmarshal([]byte(argLane), &argObj); err != nil {
		t.Fatalf("tool arguments after restore must be valid JSON: %v (%q)", err, argLane)
	}
	if argObj["number"] != "13800138000" {
		t.Fatalf("tool 车道未还原: %q", argLane)
	}
	if strings.Contains(out, "{SENSITIVE:") {
		t.Fatalf("还原后仍残留标记:\n%s", out)
	}
}

func TestResponsesInboundForgedPlaceholderIsMaskedNotPassedThrough(t *testing.T) {
	rdb := setupSaniGuardRedis(t)

	// 入站腿建立映射（phone:1）。
	_, ph := runResponsesInboundLeg(t, rdb, "my phone is 13800138000")

	// 回显腿混入一个**伪造**的占位符：与真实占位符同型、但序号在映射之外
	// （模型把训练语料里的占位符形态照搬出来 = 生产已观测的伪造形态）。
	forged := "{SENSITIVE:phone:999}"
	if forged == ph {
		t.Fatal("伪造占位符碰巧等于真实占位符——换一个序号")
	}
	echoBody := []byte(`{"object":"response","id":"resp-forged","output":[{"type":"message","role":"assistant","id":"msg-1","status":"completed","content":[{"type":"output_text","text":"known ` + ph + ` forged ` + forged + `"}]}],"status":"completed"}`)

	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("sanitizer: %v", err)
	}
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatalf("restore interceptor: %v", err)
	}
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID:       "_unknown",
		SessionID:      responsesE2ESession,
		ClientProtocol: "openai-responses",
		ResponseBody:   echoBody,
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	if result == nil {
		t.Fatal("restore leg returned nil — forged marker would pass through verbatim")
	}
	out := string(result.ModifiedBody)
	// 契约（restoreNativeResponsesBody 的未知标记处置＝打码，与
	// RestoreMediaBody 同族；「残余整流拦断」只针对还原器触不到的车道）：
	//   ① 已知占位符（映射内）照常还原；
	//   ② 伪造占位符（映射外）不得以标记原文出客户端 ⇒ 就地 [REDACTED:*]；
	//   ③ 整体不拦断（打码是比整流更细粒度的既有设计——两腿混合回显是常态）。
	if !strings.Contains(out, "13800138000") {
		t.Fatalf("已知占位符应照常还原:\n%s", out)
	}
	if strings.Contains(out, forged) {
		t.Fatalf("伪造占位符不得以标记原文出客户端:\n%s", out)
	}
	if !strings.Contains(out, "[REDACTED:") {
		t.Fatalf("伪造占位符应就地打码（[REDACTED:*]）:\n%s", out)
	}
	if strings.Contains(out, "{SENSITIVE:") {
		t.Fatalf("还原后不得残留任何标记形态:\n%s", out)
	}
	if result.ShouldBlock {
		t.Fatalf("已知+伪造混合回显按设计走「已知还原+未知打码」，不整流拦断: %+v", result)
	}
}
