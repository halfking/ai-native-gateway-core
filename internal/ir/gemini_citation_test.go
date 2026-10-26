package ir

import (
	"encoding/json"
	"testing"
)

// ★ 2026-10-27（Gemini 转换面补全轮）：厂方文档
// （docs/vendor-formats/gemini.md §特殊字段）写明
// 「`citationMetadata`：Gemini 在响应中提供引用元数据，IR 需要捕获并可在
// 序列化为 OpenAI 时映射为 annotations」。
// ⇒ 而本仓此前**IR 里没有承载它的字段** ⇒ Gemini 上游的引用在解析那一步
// 就被丢掉了（本文件钉住这条契约）。

const geminiCitationBody = `{
  "candidates": [{
    "content": {"role": "model", "parts": [{"text": "Gemini 在 2024 年发布。"}]},
    "finishReason": "STOP",
    "citationMetadata": {
      "citations": [
        {"startIndex": 0, "endIndex": 20, "uri": "https://example.com/blog", "title": "官方公告"},
        {"startIndex": 21, "endIndex": 30, "uri": "https://example.com/docs", "title": "文档"}
      ]
    }
  }],
  "usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 7, "totalTokenCount": 12},
  "modelVersion": "gemini-2.5-pro"
}`

// TestParseGeminiResponse_CapturesCitationMetadata is the positive leg:
// the IR must carry the citations.
func TestParseGeminiResponse_CapturesCitationMetadata(t *testing.T) {
	resp, err := ParseGeminiResponse([]byte(geminiCitationBody))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if len(resp.Citations) != 2 {
		t.Fatalf("citations = %d, want 2 (%+v)", len(resp.Citations), resp.Citations)
	}
	first := resp.Citations[0]
	if first.URL != "https://example.com/blog" {
		t.Errorf("URL = %q, want the gemini `uri` value", first.URL)
	}
	if first.Title != "官方公告" {
		t.Errorf("Title = %q", first.Title)
	}
	if first.StartIndex != 0 || first.EndIndex != 20 {
		t.Errorf("span = [%d,%d], want [0,20]", first.StartIndex, first.EndIndex)
	}
	if second := resp.Citations[1]; second.URL != "https://example.com/docs" || second.StartIndex != 21 {
		t.Errorf("second citation = %+v", second)
	}
}

// TestParseGeminiResponse_CitationWithoutURIIsDropped is the negative leg:
// a citation with no URI is not actionable for a client (it cannot be
// clicked) ⇒ it must not enter the IR (and must not produce an annotation
// with an empty url).
func TestParseGeminiResponse_CitationWithoutURIIsDropped(t *testing.T) {
	body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"x"}]},
	  "citationMetadata":{"citations":[{"title":"no uri","startIndex":0}]}}]}`
	resp, err := ParseGeminiResponse([]byte(body))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	if len(resp.Citations) != 0 {
		t.Fatalf("citations = %+v, want none (no uri ⇒ not actionable)", resp.Citations)
	}
}

// TestSerializeOpenAIResponse_EmitsAnnotations pins the mandated mapping
// direction: IR citations → OpenAI `annotations` (type "url_citation").
func TestSerializeOpenAIResponse_EmitsAnnotations(t *testing.T) {
	resp, err := ParseGeminiResponse([]byte(geminiCitationBody))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	out, err := SerializeOpenAIResponse(resp, "gpt-4o")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Annotations []struct {
					Type        string `json:"type"`
					URLCitation struct {
						URL        string `json:"url"`
						Title      string `json:"title"`
						StartIndex *int   `json:"start_index"`
						EndIndex   *int   `json:"end_index"`
					} `json:"url_citation"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal openai response: %v (body=%s)", err, out)
	}
	if len(wire.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(wire.Choices))
	}
	annotations := wire.Choices[0].Message.Annotations
	if len(annotations) != 2 {
		t.Fatalf("annotations = %d, want 2 (body=%s)", len(annotations), out)
	}
	if annotations[0].Type != "url_citation" {
		t.Errorf("annotation type = %q, want url_citation", annotations[0].Type)
	}
	if annotations[0].URLCitation.URL != "https://example.com/blog" {
		t.Errorf("annotation url = %q", annotations[0].URLCitation.URL)
	}
	if annotations[0].URLCitation.Title != "官方公告" {
		t.Errorf("annotation title = %q", annotations[0].URLCitation.Title)
	}
	if annotations[0].URLCitation.EndIndex == nil || *annotations[0].URLCitation.EndIndex != 20 {
		t.Errorf("annotation end_index = %v, want 20", annotations[0].URLCitation.EndIndex)
	}
}

// TestSerializeOpenAIResponse_NoCitationsMeansNoAnnotationsKey is the
// reverse nail: a response with no citations must not grow an empty
// `annotations` array ⇒ the wire shape of ordinary responses is unchanged.
func TestSerializeOpenAIResponse_NoCitationsMeansNoAnnotationsKey(t *testing.T) {
	resp, err := ParseGeminiResponse([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}]}`))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	out, err := SerializeOpenAIResponse(resp, "gpt-4o")
	if err != nil {
		t.Fatalf("SerializeOpenAIResponse: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices, _ := wire["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in %s", out)
	}
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	if _, ok := message["annotations"]; ok {
		t.Fatalf("annotations must be absent when there are no citations: %s", out)
	}
}

// TestSerializeGeminiResponse_RoundTripsCitationMetadata pins the Gemini
// side of the round-trip: the native shape uses `uri` (not `url`).
func TestSerializeGeminiResponse_RoundTripsCitationMetadata(t *testing.T) {
	resp, err := ParseGeminiResponse([]byte(geminiCitationBody))
	if err != nil {
		t.Fatalf("ParseGeminiResponse: %v", err)
	}
	out, err := SerializeGeminiResponse(resp, "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("SerializeGeminiResponse: %v", err)
	}
	var wire struct {
		Candidates []struct {
			CitationMetadata *struct {
				Citations []map[string]any `json:"citations"`
			} `json:"citationMetadata"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatalf("unmarshal gemini response: %v (body=%s)", err, out)
	}
	if len(wire.Candidates) != 1 || wire.Candidates[0].CitationMetadata == nil {
		t.Fatalf("citationMetadata missing in %s", out)
	}
	citations := wire.Candidates[0].CitationMetadata.Citations
	if len(citations) != 2 {
		t.Fatalf("citations = %d, want 2", len(citations))
	}
	if uri, _ := citations[0]["uri"].(string); uri != "https://example.com/blog" {
		t.Errorf("gemini citation uri = %q, want the `uri` field name", uri)
	}
	if _, ok := citations[0]["url"]; ok {
		t.Errorf("gemini citation must not use the `url` field name: %+v", citations[0])
	}
	if start, _ := citations[0]["startIndex"].(float64); start != 0 {
		t.Errorf("startIndex = %v, want 0 (present because the source had it)", start)
	}
}
