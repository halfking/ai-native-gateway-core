// Package streaming — summarize_test.go
//
// 文本加工三件套（/v1/summarize | /v1/summarize/title | /v1/classify）的
// 行为锁定：
//   - 环回 chat 调用转发调用方 Authorization；
//   - title：候选解析、规则过滤（超长/禁用开头）、实体优先择优、全违规重试；
//   - classify：受控词表校验、词表外 task_type 触发重试后 502；
//   - summarize：短文本单次路径、长文本 map-reduce 路径与段降级；
//   - reasoning 混入 content（<think> 块）被剥离。
package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newSummarizeTestServer 起一个挂三件套路由与环回 chat 桩的 httptest 服务。
// chatStub 为 nil 时返回空 content。返回服务地址与收到的 chat 载荷清单。
func newSummarizeTestServer(t *testing.T, chatStub http.HandlerFunc) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	svc := NewSummarizeService()
	h := NewSummarizeHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("/v1/summarize", h)
	mux.Handle("/v1/summarize/title", h)
	mux.Handle("/v1/classify", h)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		// 读出后重新填回：包装层要记录载荷，chatStub 还要自己 decode——
		// body 只能消费一次，不回填的话 stub 的 decode 拿到空体，按
		// messages[0] 分发的 stub 会 panic（客户端只见 EOF，极难排查）。
		bodyBytes, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(bodyBytes, &body)
		got = append(got, body)
		if chatStub != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			chatStub(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"model":"stub-llm","choices":[{"message":{"content":"{}"}}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// 服务级环回基址钉到测试服务（同包直设字段，避免按 Host 推导）。
	svc.baseURL = srv.URL
	return srv, &got
}

// summarizePost 直发一条三件套请求。
func summarizePost(t *testing.T, srvURL, path, body string) (*http.Response, []byte) {
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
	buf, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, buf
}

func TestSummarizeTitleHappyPathPicksEntityCandidate(t *testing.T) {
	srv, calls := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-caller-key" {
			t.Errorf("loopback must forward caller Authorization, got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Gateway-Internal-Purpose") != "summarize" {
			t.Errorf("internal purpose = %q", r.Header.Get("X-Gateway-Internal-Purpose"))
		}
		_, _ = w.Write([]byte(`{"model":"qwen2.5:7b","choices":[{"message":{"content":"[\"关于网关问题的说明\",\"llm 网关 503 排查与修复记录\",\"网关问题小结\"]"}}]}`))
	})
	resp, body := summarizePost(t, srv.URL, "/v1/summarize/title", `{"model":"qwen2.5:7b","text":"今天下午我们围绕 211 服务器上 llm 网关容器 b 的 503 错误做了完整的排查"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out SummarizeTitleResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not json: %v", err)
	}
	// 「关于」开头被过滤；含实体（llm/503）的候选胜出。
	if out.Title != "llm 网关 503 排查与修复记录" {
		t.Fatalf("title = %q candidates = %v", out.Title, out.Candidates)
	}
	if len(out.Candidates) != 2 {
		t.Fatalf("candidates = %v (banned opening must be filtered)", out.Candidates)
	}
	if out.LLMModel != "qwen2.5:7b" {
		t.Fatalf("llm_model = %q", out.LLMModel)
	}
	if len(*calls) != 1 {
		t.Fatalf("chat calls = %d", len(*calls))
	}
	// 环回载荷断言：title 默认 reasoning_effort=none、temperature=0.3。
	payload := (*calls)[0]
	if payload["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %v, want none (title default)", payload["reasoning_effort"])
	}
	if payload["temperature"] != 0.3 {
		t.Fatalf("temperature = %v", payload["temperature"])
	}
}

func TestSummarizeTitleAllInvalidRetriesThen502(t *testing.T) {
	var n int
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"[\"关于某事的一些说明\",\"有关另外一个话题\"]"}}]}`))
	})
	resp, _ := summarizePost(t, srv.URL, "/v1/summarize/title", `{"model":"m","text":"内容"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 after retry", resp.StatusCode)
	}
	if n != 2 {
		t.Fatalf("llm calls = %d, want 2 (initial + rules retry)", n)
	}
}

func TestSummarizeTitleMarkdownFenceParses(t *testing.T) {
	fenced := "```json\n[\"修复 llm 网关 503\"]\n```"
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]any{
			"model":   "m",
			"choices": []any{map[string]any{"message": map[string]any{"content": fenced}}},
		})
		_, _ = w.Write(payload)
	})
	resp, body := summarizePost(t, srv.URL, "/v1/summarize/title", `{"model":"m","text":"内容"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out SummarizeTitleResult
	_ = json.Unmarshal(body, &out)
	if out.Title != "修复 llm 网关 503" {
		t.Fatalf("title = %q", out.Title)
	}
}

func TestClassifyVocabValidationAndRetry(t *testing.T) {
	var n int
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			// 词表外类型 → 必须被判违规并重试。
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"{\"task_type\":\"玄学操作\",\"sub_types\":[],\"confidence\":0.9,\"keywords\":[],\"reason\":\"x\"}"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"{\"task_type\":\"运维执行\",\"sub_types\":[\"巡检\",\"自造标签\"],\"confidence\":0.9,\"keywords\":[\"网关\"],\"reason\":\"r\"}"}}]}`))
	})
	resp, body := summarizePost(t, srv.URL, "/v1/classify", `{"model":"m","text":"下午三点网络设备巡检"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out ClassifyResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if out.TaskType != "运维执行" {
		t.Fatalf("task_type = %q", out.TaskType)
	}
	// 自造 sub_type 被过滤，词表内的保留。
	if len(out.SubTypes) != 1 || out.SubTypes[0] != "巡检" {
		t.Fatalf("sub_types = %v", out.SubTypes)
	}
	if n != 2 {
		t.Fatalf("llm calls = %d, want 2", n)
	}
}

func TestClassifyCustomVocabOverride(t *testing.T) {
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"{\"task_type\":\"网络运维\",\"sub_types\":[],\"confidence\":0.9,\"keywords\":[],\"reason\":\"r\"}"}}]}`))
	})
	resp, body := summarizePost(t, srv.URL, "/v1/classify",
		`{"model":"m","text":"交换机割接","vocab":[["网络运维",["割接"]],["桌面运维",["装机"]]]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"网络运维"`) {
		t.Fatalf("custom vocab output missing: %s", body)
	}
}

func TestClassifyInvalidVocabIs400(t *testing.T) {
	srv, _ := newSummarizeTestServer(t, nil)
	resp, _ := summarizePost(t, srv.URL, "/v1/classify", `{"model":"m","text":"x","vocab":"not-an-array"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSummarizeShortPathSingleCall(t *testing.T) {
	var n int
	srv, calls := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"model":"qwen3.8:27b-mlx","choices":[{"message":{"content":"# 标题\n\n## 一句话回顾\nx\n\n## 时间线\n- y\n\n## 关键决议\n| 决议 |\n\n## 待办\n| 事项 |\n"}}]}`))
	})
	resp, body := summarizePost(t, srv.URL, "/v1/summarize", `{"model":"qwen3.8:27b-mlx","text":"短会话内容"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out SummarizeResult
	_ = json.Unmarshal(body, &out)
	if out.Pipeline != "single" || n != 1 {
		t.Fatalf("pipeline = %q calls = %d, want single/1", out.Pipeline, n)
	}
	if len(*calls) != 1 || (*calls)[0]["reasoning_effort"] != "low" {
		t.Fatalf("summarize default reasoning_effort = %v, want low", (*calls)[0]["reasoning_effort"])
	}
}

func TestSummarizeShortPathMissingSectionsRetries(t *testing.T) {
	var n int
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"## 只有回顾没有决议和待办"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"## 一句话回顾 a\n## 时间线 b\n## 关键决议 c\n## 待办 d"}}]}`))
	})
	resp, _ := summarizePost(t, srv.URL, "/v1/summarize", `{"model":"m","text":"内容"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if n != 2 {
		t.Fatalf("llm calls = %d, want 2 (structure retry)", n)
	}
}

// TestSummarizeLongPathMapReduce：>3000 字输入 → map×N + reduce + render，
// 段降级（map 输出非 JSON 时重试一次后以原文摘录入 reduce）不中断管线。
func TestSummarizeLongPathMapReduce(t *testing.T) {
	var callKinds []string // map | reduce | render（按 system prompt 特征识别）
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgs := body["messages"].([]any)
		sys := msgs[0].(map[string]any)["content"].(string)
		switch {
		case strings.Contains(sys, "结构化抽取器"):
			callKinds = append(callKinds, "map")
			_, _ = w.Write([]byte(`{"model":"map-m","choices":[{"message":{"content":"{\"segment_range\":\"[1,2]\",\"topics\":[]}"}}]}`))
		case strings.Contains(sys, "合并它们"):
			callKinds = append(callKinds, "reduce")
			_, _ = w.Write([]byte(`{"model":"map-m","choices":[{"message":{"content":"{\"timeline\":[]}"}}]}`))
		default:
			callKinds = append(callKinds, "render")
			_, _ = w.Write([]byte(`{"model":"render-m","choices":[{"message":{"content":"## 一句话回顾 a\n## 时间线 b\n## 关键决议 c\n## 待办 d"}}]}`))
		}
	})
	long := strings.Repeat("运维: 今天排查 252 出向流量问题。\n", 400) // 21 字/行 × 400 = 8400 字 → 2 段（8000/段）
	resp, body := summarizePost(t, srv.URL, "/v1/summarize", fmt.Sprintf(`{"model":"render-m","map_model":"map-m","text":%q}`, long))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var out SummarizeResult
	_ = json.Unmarshal(body, &out)
	if out.Pipeline != "map-reduce" || out.Chunks != 2 {
		t.Fatalf("pipeline=%q chunks=%d", out.Pipeline, out.Chunks)
	}
	if len(callKinds) != 4 || callKinds[0] != "map" || callKinds[1] != "map" || callKinds[2] != "reduce" || callKinds[3] != "render" {
		t.Fatalf("call kinds = %v, want [map map reduce render]", callKinds)
	}
	if out.LLMModel != "render-m" {
		t.Fatalf("llm_model = %q (render 阶段的实际模型)", out.LLMModel)
	}
}

// TestSummarizeMapStageDegradesToExcerpt：map 输出两次都不是 JSON →
// 以降级摘录继续（一次逻辑调用不因单段格式失败而全灭）。
func TestSummarizeMapStageDegradesToExcerpt(t *testing.T) {
	var mapCalls int
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgs := body["messages"].([]any)
		sys := msgs[0].(map[string]any)["content"].(string)
		if strings.Contains(sys, "结构化抽取器") {
			mapCalls++
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"我不会输出 JSON，抱歉。"}}]}`))
			return
		}
		if strings.Contains(sys, "合并它们") {
			_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"{\"timeline\":[]}"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"## 一句话回顾 a\n## 时间线 b\n## 关键决议 c\n## 待办 d"}}]}`))
	})
	long := strings.Repeat("会话内容若干。", 600) // 7 字 × 600 = 4200 字 ≥3000 → 长路径；chunk_chars 2000 → 3 段
	resp, body := summarizePost(t, srv.URL, "/v1/summarize", fmt.Sprintf(`{"model":"m","text":%q,"chunk_chars":2000}`, long))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	// 3 段 × (首发 + 一次重试) = 6 次 map 调用，全部失败后降级为原文摘录。
	if mapCalls != 6 {
		t.Fatalf("map calls = %d, want 6 (2 per chunk × 3 chunks)", mapCalls)
	}
}

// TestSummarizeTextTooLongIs400：段数超上限必须 400 拒绝而不是悄悄截断。
func TestSummarizeTextTooLongIs400(t *testing.T) {
	srv, _ := newSummarizeTestServer(t, nil)
	veryLong := strings.Repeat("字", maxSummarizeChunks*2000+100)
	resp, _ := summarizePost(t, srv.URL, "/v1/summarize", fmt.Sprintf(`{"model":"m","text":%q,"chunk_chars":2000}`, veryLong))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestSummarizeLoopback429RetriesOnce：环回撞 key RPM（429）等一个窗口
// 重试一次，第二次成功则整体成功。
func TestSummarizeLoopback429RetriesOnce(t *testing.T) {
	var n int
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"[\"标题实体 503\"]"}}]}`))
	})
	resp, body := summarizePost(t, srv.URL, "/v1/summarize/title", `{"model":"m","text":"内容"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if n != 2 {
		t.Fatalf("llm calls = %d, want 2 (429 + retry)", n)
	}
}

// TestSummarizeThinkingUnsupportedStripsEffort：非 thinking 模型对
// reasoning_effort 回 400（ollama "does not support thinking"）时剥掉
// effort 重发一次——第二次载荷必须不含 reasoning_effort（2026-10-27 实测
// 根因：summarize 默认 effort=low + qwen2.5:7b 每发必 400 ⇒ map 层全灭）。
func TestSummarizeThinkingUnsupportedStripsEffort(t *testing.T) {
	var bodies []map[string]any
	srv, _ := newSummarizeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		if _, has := b["reasoning_effort"]; has {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"\"qwen2.5:7b\" does not support thinking","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"qwen2.5:7b","choices":[{"message":{"content":"# t\n## 一句话回顾 a\n## 时间线 b\n## 关键决议 c\n## 待办 d"}}]}`))
	})
	resp, body := summarizePost(t, srv.URL, "/v1/summarize", `{"model":"qwen2.5:7b","text":"短内容"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if len(bodies) != 2 {
		t.Fatalf("llm calls = %d, want 2 (effort-400 + stripped retry)", len(bodies))
	}
	if _, has := bodies[1]["reasoning_effort"]; has {
		t.Fatalf("retry payload must NOT carry reasoning_effort")
	}
	var out SummarizeResult
	_ = json.Unmarshal(body, &out)
	if out.Pipeline != "single" || out.LLMModel != "qwen2.5:7b" {
		t.Fatalf("pipeline=%q llm_model=%q", out.Pipeline, out.LLMModel)
	}
}

func TestStripThinkBlock(t *testing.T) {
	cases := map[string]string{
		"<think>推理过程</think>答案":                "答案",
		"前文<think>a</think>中<think>b</think>后": "前文中后",
		"<think>未闭合":                           "",
		"没有思考块":                                "没有思考块",
	}
	for in, want := range cases {
		if got := stripThinkBlock(in); got != want {
			t.Errorf("stripThinkBlock(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitSummarizeChunksPrefersNewlineBoundary(t *testing.T) {
	line := strings.Repeat("字", 100) + "\n"
	text := strings.Repeat(line, 40) // 4040 字 → 2 个满段 + 1 个余量段
	chunks := splitSummarizeChunks(text, 2000)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (two full + remainder)", len(chunks))
	}
	for i, c := range chunks {
		if rc := len([]rune(c)); rc > 2000 || rc == 0 {
			t.Errorf("chunk %d size %d out of bounds", i, rc)
		}
	}
	// 断口必须落在换行边界：段内容被 TrimSpace 剥掉段尾换行，重组时
	// 补回换行必须能还原原文（末段尾换行同样被剥，容掉）——若断口撕裂
	// 了行，重组必然对不上。
	if rejoined := chunks[0] + "\n" + chunks[1] + "\n" + chunks[2]; rejoined != strings.TrimSuffix(text, "\n") {
		t.Errorf("rejoin mismatch: chunks must split at newline boundaries")
	}
}

func TestSummarizeRequiresModelAndText(t *testing.T) {
	srv, _ := newSummarizeTestServer(t, nil)
	for _, path := range []string{"/v1/summarize", "/v1/summarize/title", "/v1/classify"} {
		resp, _ := summarizePost(t, srv.URL, path, `{"model":"m"}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400 (text required)", path, resp.StatusCode)
		}
	}
}

// TestSummarizeUnknownPathIs404：分发器对未注册路径必须 404。
func TestSummarizeUnknownPathIs404(t *testing.T) {
	srv, _ := newSummarizeTestServer(t, nil)
	resp, _ := summarizePost(t, srv.URL, "/v1/summarize/other", `{"model":"m","text":"x"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSummarizeMethodNotAllowed：GET 必须 405 + Allow 头。
func TestSummarizeMethodNotAllowed(t *testing.T) {
	srv, _ := newSummarizeTestServer(t, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/summarize", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("status = %d Allow = %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
}

// 编译期钉住时限常量不被悄悄改坏（10min 总预算是长文管线的承诺面）。
func TestSummarizeDurations(t *testing.T) {
	if maxSummarizeDuration != 10*time.Minute {
		t.Fatalf("maxSummarizeDuration = %v", maxSummarizeDuration)
	}
}
