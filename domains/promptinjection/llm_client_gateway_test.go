package promptinjection

// llm_client_gateway_test.go — 分析用的网关 LLM 客户端钉测（2026-11-26）。
//
// 钉两件事（都是 211 首批真实分析的实测结论）：
//  1. 输出额度：4000 时约三分之一的回答在额度处被截断（分析模型带推理，推理 token 也
//     计入输出额度）⇒ 额度是 analyzerMaxTokens，且请求体里发的就是它。
//  2. 结束原因按**提示词**记、能被分析端读到——此前分析端留了一个「可选扩展」接口，
//     生产客户端却从未实现 ⇒ 211 上所有分析的 finish_reason 都是空的。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newGatewayClientForTest(t *testing.T, handler http.HandlerFunc) *GatewayLLMClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewGatewayLLMClient("glm-5.3-flash", "/chat/completions", "test-key", srv.URL)
	c.SetTimeout(10 * time.Second)
	return c
}

func TestGatewayLLMClient_SendsAnalyzerMaxTokens(t *testing.T) {
	var (
		got          map[string]any
		markerHeader string
	)
	c := newGatewayClientForTest(t, func(w http.ResponseWriter, r *http.Request) {
		markerHeader = r.Header.Get("X-Llmgw-Pi-Analyzer")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"root_cause\":\"x\"}"},"finish_reason":"stop"}],"usage":{"total_tokens":12}}`)
	})
	content, tokens, err := c.Analyze(context.Background(), "prompt")
	if err != nil || content == "" || tokens != 12 {
		t.Fatalf("Analyze = (%q, %d, %v)", content, tokens, err)
	}
	if got["max_tokens"] != float64(analyzerMaxTokens) {
		t.Errorf("请求体的输出额度应为 analyzerMaxTokens=%d，实得 %v", analyzerMaxTokens, got["max_tokens"])
	}
	if analyzerMaxTokens < 8000 {
		t.Errorf("输出额度不该低于 8000（211 实测 4000 时约 1/3 的回答被截断），实得 %d", analyzerMaxTokens)
	}
	if got["temperature"] != 0.3 || got["model"] != "glm-5.3-flash" {
		t.Errorf("温度 / 模型不该被这次改动碰到：%v / %v", got["temperature"], got["model"])
	}
	if markerHeader == "" {
		t.Errorf("内部标记头 X-Llmgw-Pi-Analyzer 丢了")
	}
}

func TestGatewayLLMClient_RecordsFinishReasonPerPrompt(t *testing.T) {
	c := newGatewayClientForTest(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		finish := "stop"
		if strings.Contains(req.Messages[0].Content, "long-one") {
			finish = "length"
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"`+finish+`"}],"usage":{"total_tokens":1}}`)
	})
	if _, _, err := c.Analyze(context.Background(), "analyse the long-one"); err != nil {
		t.Fatal(err)
	}
	if got := c.FinishReasonForCall("analyse the long-one"); got != "length" {
		t.Errorf("按提示词读结束原因应为 length，得 %q", got)
	}
	if got := c.FinishReasonForCall("never sent"); got != "" {
		t.Errorf("没发过的提示词不应有结束原因，得 %q", got)
	}
	// 另一个提示词各有各的值（并发的两次分析不串台）。
	if _, _, err := c.Analyze(context.Background(), "analyse another"); err != nil {
		t.Fatal(err)
	}
	if c.FinishReasonForCall("analyse the long-one") != "length" || c.FinishReasonForCall("analyse another") != "stop" {
		t.Errorf("两次调用的结束原因串台了：%q / %q",
			c.FinishReasonForCall("analyse the long-one"), c.FinishReasonForCall("analyse another"))
	}
}

// finishReasonStub 是带「按提示词报结束原因」扩展的桩客户端。
type finishReasonStub struct {
	response string
	finish   string
}

func (s *finishReasonStub) Analyze(context.Context, string) (string, int, error) {
	return s.response, 1, nil
}

func (s *finishReasonStub) FinishReasonForCall(string) string { return s.finish }

// 没有扩展的桩：分析端只能靠输出形态推断。
type plainStub struct{ response string }

func (s *plainStub) Analyze(context.Context, string) (string, int, error) { return s.response, 1, nil }

const truncatedAnalysis = `{"root_cause":"上游流被掐断","error_category":"upstream_interrupted","confidence":0.8,
	"suggested_fix":"重试","fix_plan":{"summary":"重试即可","manual_steps":[{"step":1,"title":"重试","kind":"client","risk":"low"},{"step":2,"title":"核对"`

const completeAnalysis = `{"root_cause":"上游流被掐断","error_category":"upstream_interrupted","confidence":0.8,
	"suggested_fix":"重试","fix_plan":{"summary":"重试即可","manual_steps":[{"step":1,"title":"重试","kind":"client","risk":"low"}]}}`

func TestNoteFinishReason_RecordedOrInferredHonestly(t *testing.T) {
	diag := func(r *AnalysisResult) map[string]any {
		t.Helper()
		pd, _ := r.AnalysisDetails["parse_diagnostics"].(map[string]any)
		if pd == nil {
			t.Fatalf("没有 parse_diagnostics: %#v", r.AnalysisDetails)
		}
		return pd
	}

	// ① 客户端报了 length ⇒ 原话记录。
	ea := &ErrorAnalyzer{llmClient: &finishReasonStub{response: truncatedAnalysis, finish: "Length"}}
	res, _ := parseAnalysisResponse(truncatedAnalysis)
	ea.noteFinishReason(res, "p")
	if pd := diag(res); pd["finish_reason"] != "length" || pd["finish_reason_source"] != nil {
		t.Errorf("客户端原话应照记、不标推断：%v", pd)
	}

	// ② 客户端没报、解析器看出被截断 ⇒ 记 length 并**标明是推断的**。
	ea = &ErrorAnalyzer{llmClient: &plainStub{response: truncatedAnalysis}}
	res, _ = parseAnalysisResponse(truncatedAnalysis)
	ea.noteFinishReason(res, "p")
	pd := diag(res)
	if pd["finish_reason"] != "length" || pd["finish_reason_source"] != "inferred_from_truncated_output" {
		t.Errorf("截断且无客户端读数 ⇒ 应记 length 并标推断：%v", pd)
	}

	// ③ 完整输出、客户端没报 ⇒ 不编造结束原因。
	ea = &ErrorAnalyzer{llmClient: &plainStub{response: completeAnalysis}}
	res, _ = parseAnalysisResponse(completeAnalysis)
	ea.noteFinishReason(res, "p")
	if pd := diag(res); pd["finish_reason"] != nil {
		t.Errorf("完整输出且无客户端读数时不该编造结束原因：%v", pd)
	}

	// ④ 客户端说 stop、但输出确实不完整 ⇒ 两个事实都留着（不拿 stop 盖掉 truncated）。
	ea = &ErrorAnalyzer{llmClient: &finishReasonStub{response: truncatedAnalysis, finish: "stop"}}
	res, _ = parseAnalysisResponse(truncatedAnalysis)
	ea.noteFinishReason(res, "p")
	if pd := diag(res); pd["finish_reason"] != "stop" || pd["truncated"] != true {
		t.Errorf("客户端说 stop 而输出不完整 ⇒ 两个事实都留：%v", pd)
	}
}

// 生产客户端必须真的实现了那个可选扩展（此前它从未实现 ⇒ 读数恒空）。
func TestGatewayLLMClient_ImplementsFinishReasonExtension(t *testing.T) {
	var _ finishReasonForCaller = (*GatewayLLMClient)(nil)
}
