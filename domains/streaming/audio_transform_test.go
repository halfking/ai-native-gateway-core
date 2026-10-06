// Package streaming — audio_transform_test.go
//
// refine/analyze 转写后处理二件套的行为锁定：
//   - 环回 chat 调用转发 Authorization、命中自身 /v1/chat/completions；
//   - refine：LLM JSON 解析、空输出保护（不拿空串覆盖调用方文本）、
//     热词缺失回执、include_corrections 门控；
//   - analyze：滚动摘要（prior_summary 进 prompt）、style/max_points 归一；
//   - LLM 输出带 markdown 代码栏时仍可解析。
package streaming

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/upstream"
)

// newTransformTestServer 起一个同时挂 refine/analyze 与环回 chat 桩的
// httptest 服务，返回服务地址。chatStub 收到 prompt 相关字段供断言。
func newTransformTestServer(t *testing.T, chatStub http.HandlerFunc) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	svc := NewAudioService(&fakeAudioResolver{}, upstream.New())
	ts := NewAudioTransformService(svc)
	h := NewAudioTransformHandler(ts)
	mux := http.NewServeMux()
	mux.Handle("/v1/audio/refine", h)
	mux.Handle("/v1/audio/analyze", h)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		if chatStub != nil {
			chatStub(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"model":"stub-llm","choices":[{"message":{"content":"{}"}}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &got
}

func transformPost(t *testing.T, srvURL, path, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srvURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-caller-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 2048)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	_ = resp.Body.Close()
	return resp, buf
}

func TestRefineHappyPath(t *testing.T) {
	srv, calls := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-caller-key" {
			t.Errorf("loopback must forward caller Authorization, got %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"model":"glm-5.2-air","choices":[{"message":{"content":"{\"refined\":\"欢迎大家体验达摩院推出的语音识别模型。\",\"corrections\":[{\"from\":\"打磨院\",\"to\":\"达摩院\",\"reason\":\"热词\"}]}"}}]}`))
	})
	resp, body := transformPost(t, srv.URL, "/v1/audio/refine", `{
		"model":"glm-5.2-air","text":"欢迎大家体验打磨院推出的语音识别模型",
		"language":"zh","hotwords":["达摩院"],"context":"产品评审","include_corrections":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out RefineResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if out.Refined != "欢迎大家体验达摩院推出的语音识别模型。" {
		t.Fatalf("refined = %q", out.Refined)
	}
	if len(out.Corrections) != 1 || out.Corrections[0].To != "达摩院" {
		t.Fatalf("corrections = %+v", out.Corrections)
	}
	if out.LLMModel != "glm-5.2-air" {
		t.Fatalf("llm_model = %q", out.LLMModel)
	}
	// prompt 断言：热词与场景必须进入 user 消息。
	if len(*calls) != 1 {
		t.Fatalf("chat calls = %d", len(*calls))
	}
	msgs := (*calls)[0]["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "达摩院") || !strings.Contains(user, "产品评审") {
		t.Fatalf("user prompt missing hotwords/context: %s", user)
	}
}

func TestRefineMissingHotwordsReceipt(t *testing.T) {
	srv, _ := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"refined\":\"今天讨论预算\"}"}}]}`))
	})
	_, body := transformPost(t, srv.URL, "/v1/audio/refine",
		`{"model":"m","text":"今天讨论预算","hotwords":["达摩院","预算"]}`)
	var out RefineResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not json: %v", err)
	}
	// 「预算」已出现，不得进缺失清单；「达摩院」没出现，必须回执。
	if len(out.IgnoredWords) != 1 || out.IgnoredWords[0] != "达摩院" {
		t.Fatalf("ignored hotwords = %v", out.IgnoredWords)
	}
}

func TestRefineEmptyRefinedIs502NotSilent(t *testing.T) {
	srv, _ := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// LLM 返回了解析得开的 JSON 但 refined 为空——必须报错而不是
		// 用空串覆盖调用方文本。
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"refined\":\"\"}"}}]}`))
	})
	resp, body := transformPost(t, srv.URL, "/v1/audio/refine", `{"model":"m","text":"原文"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
}

func TestRefineMarkdownFencedJSONParses(t *testing.T) {
	srv, _ := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// markdown 代码栏出现在 LLM 的 content 里（chat 响应体本身仍是 JSON）。
		content := "```json\n{\"refined\":\"带代码栏输出\"}\n```"
		payload := map[string]any{"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		}}
		_ = json.NewEncoder(w).Encode(payload)
	})
	resp, body := transformPost(t, srv.URL, "/v1/audio/refine", `{"model":"m","text":"原文"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "带代码栏输出") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestAnalyzeRollingSummary(t *testing.T) {
	srv, calls := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"{\"summary\":\"合并后的最新全文摘要\",\"key_points\":[\"预算 50 万\"],\"decisions\":[\"下周启动\"],` +
			`\"action_items\":[{\"text\":\"出方案\",\"owner\":\"张三\"}],\"open_questions\":[\"供应商未定\"],` +
			`\"hints\":[\"预算数字尚未最终确认，建议追问\"],\"topics\":[\"预算\",\"排期\"]}"}}]}`))
	})
	resp, body := transformPost(t, srv.URL, "/v1/audio/analyze", `{
		"model":"glm-5.2-air","transcript":"新增片段文本","prior_summary":"上一轮摘要","style":"meeting","language":"zh","max_points":5}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out AnalyzeResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if out.Summary != "合并后的最新全文摘要" || len(out.KeyPoints) != 1 || len(out.Hints) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if len(out.ActionItems) != 1 || out.ActionItems[0].Owner != "张三" {
		t.Fatalf("action items = %+v", out.ActionItems)
	}
	msgs := (*calls)[0]["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "上一轮摘要") || !strings.Contains(user, "新增片段文本") {
		t.Fatalf("user prompt missing prior summary or transcript: %s", user)
	}
	sys := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "会议") || !strings.Contains(sys, "5 条") {
		t.Fatalf("system prompt missing style/max_points: %s", sys)
	}
}

func TestAnalyzeEmptySummaryIs502(t *testing.T) {
	srv, _ := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"\",\"key_points\":[]}"}}]}`))
	})
	resp, body := transformPost(t, srv.URL, "/v1/audio/analyze", `{"model":"m","transcript":"x"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
}

func TestAnalyzeChatUpstreamErrorMapping(t *testing.T) {
	// 环回 chat 429 → 沿 writeAudioError 口径映射 429，而不是 502。
	srv, _ := newTransformTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
	})
	resp, body := transformPost(t, srv.URL, "/v1/audio/analyze", `{"model":"m","transcript":"x"}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
}

func TestRefineRequiresModelAndText(t *testing.T) {
	srv, _ := newTransformTestServer(t, nil)
	resp, _ := transformPost(t, srv.URL, "/v1/audio/refine", `{"model":"m"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp, _ = transformPost(t, srv.URL, "/v1/audio/analyze", `{"transcript":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// MCP tools/call 两个新工具的往返（走与 HTTP 面相同的环回机制）。
func TestMCPTransformToolCalls(t *testing.T) {
	var mux *http.ServeMux
	var srv *httptest.Server
	svc := NewAudioService(&fakeAudioResolver{}, upstream.New())
	tsvc := NewAudioTransformService(svc)
	h := NewAudioMCPHandler(svc)
	h.SetTransformService(tsvc)
	mux = http.NewServeMux()
	mux.Handle("/v1/mcp", h)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		content := `{"refined":"精修文本"}`
		if len(body.Messages) > 0 {
			if sys, _ := body.Messages[0]["content"].(string); strings.Contains(sys, "分析器") {
				content = `{"summary":"滚动摘要","key_points":[]}`
			}
		}
		respPayload := map[string]any{"model": "stub", "choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		}}
		_ = json.NewEncoder(w).Encode(respPayload)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	call := func(id int, name, args string) map[string]any {
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": json.RawMessage(args)},
		})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/mcp", strings.NewReader(string(payload)))
		_ = req
		resp, err := http.Post(srv.URL+"/v1/mcp", "application/json", strings.NewReader(string(payload)))
		if err != nil {
			t.Fatalf("mcp post: %v", err)
		}
		var out map[string]any
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			t.Fatalf("mcp response not json: %v body=%s", uerr, raw)
		}
		return out
	}

	res := call(1, "refine_transcription", `{"text":"原始文本","model":"glm-5.2-air","hotwords":["热词"]}`)
	resMap, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in response: %v", res)
	}
	sc, ok := resMap["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", resMap)
	}
	if sc["refined"] != "精修文本" {
		t.Fatalf("structuredContent = %v", sc)
	}

	res = call(2, "analyze_transcription", `{"transcript":"片段","model":"glm-5.2-air","prior_summary":"旧摘要"}`)
	resMap, ok = res["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in response: %v", res)
	}
	sc, ok = resMap["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", resMap)
	}
	if _, ok := sc["summary"]; !ok {
		t.Fatalf("analyze structuredContent missing summary: %v", sc)
	}
}
