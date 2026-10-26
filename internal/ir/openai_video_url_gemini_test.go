package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// OpenAI 线格式的 video_url → Gemini 上游（R148 审计轮，2026-10-27）。
//
// ★ 背景：2026-10-26 那轮把 video 补进了分级模态门的接受臂
// （provider/client.go 的 candidateQuerySQLBase），于是「OpenAI 客户端发
// video_url → 路由到 Gemini 模型」从「503 无候选」变成了「有候选」。
//
// ★ 但那一轮只完成了**Gemini → OpenAI** 这一个方向：它修的是
// serializeOpenAIAudioBlock / serializeOpenAIVideoBlock 认不认
// IR 的 Type="file_uri"。**反方向**（OpenAI 线格式的 video_url 块解析成
// IR 之后，Gemini 序列化器能不能把它发出去）**没人碰**：
//
//   - parse_openai.go 此前没有 "video_url" 分支 ⇒ 落进 default ⇒
//     block.Video 恒为 nil；
//   - serialize_gemini.go 此前既没有 "video_url" 分支、**也没有 default
//     分支** ⇒ switch 落空 ⇒ **整块被静默丢弃**。
//
// ⇒ 用户视角：请求 200，模型也答了，但**视频从来没到上游**。
// 形状正确的三个协议（OpenAI/Anthropic/Responses）靠各自的 default 分支
// 把 RawContent 原样发出，所以这个洞在前三个协议上看不见。

// parseOpenAIVideoURLBody parses an OpenAI chat-completions body carrying a
// single video_url content block.
func parseOpenAIVideoURLBody(t *testing.T, body string) *InternalRequest {
	t.Helper()
	irReq, err := ParseOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	return irReq
}

// TestOpenAIVideoURL_ParsesIntoTypedVideo pins that an OpenAI video_url block
// now carries a typed payload (MediaSource) instead of only raw bytes —
// without that payload the Gemini serializer has nothing to emit.
func TestOpenAIVideoURL_ParsesIntoTypedVideo(t *testing.T) {
	irReq := parseOpenAIVideoURLBody(t, `{
		"model": "gemini-2.5-pro",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "what happens here"},
				{"type": "video_url", "video_url": {"url": "https://example.com/clip.mp4"}}
			]
		}]
	}`)

	if len(irReq.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(irReq.Messages))
	}
	content := irReq.Messages[0].Content
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(content))
	}
	vid := content[1]
	if vid.Video == nil {
		t.Fatal("video_url block has no typed Video payload ⇒ the Gemini serializer would have nothing to emit")
	}
	if vid.Video.URL != "https://example.com/clip.mp4" {
		t.Errorf("Video.URL = %q, want the block's url", vid.Video.URL)
	}
	if vid.Video.Type != "url" {
		t.Errorf("Video.Type = %q, want url", vid.Video.Type)
	}
}

// TestOpenAIVideoURL_ReachesGeminiUpstream is the actual bug pin: the video
// must survive the OpenAI → IR → Gemini conversion as a Gemini fileData part.
//
// Before R148 this produced an empty parts array (the block was dropped by
// serialize_gemini.go's switch) while the request still succeeded.
func TestOpenAIVideoURL_ReachesGeminiUpstream(t *testing.T) {
	irReq := parseOpenAIVideoURLBody(t, `{
		"model": "gemini-2.5-pro",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "what happens here"},
				{"type": "video_url", "video_url": {"url": "https://example.com/clip.mp4"}}
			]
		}]
	}`)

	out, err := SerializeGemini(irReq)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}
	var m struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text     string `json:"text"`
				FileData struct {
					MimeType string `json:"mimeType"`
					FileURI  string `json:"fileUri"`
				} `json:"fileData"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal Gemini body: %v", err)
	}
	if len(m.Contents) != 1 || len(m.Contents[0].Parts) != 2 {
		t.Fatalf("expected 1 content with 2 parts (text + video), got %d/%d: %s",
			len(m.Contents), len(m.Contents[0].Parts), out)
	}
	fd := m.Contents[0].Parts[1].FileData
	if fd.FileURI != "https://example.com/clip.mp4" {
		t.Errorf("Gemini fileUri = %q, want the video URL to survive the protocol conversion", fd.FileURI)
	}
	// ★ The MIME type must be a syntactically valid one. The old inline
	// expression produced "video/" (MediaType and Format both empty) — a
	// malformed mimeType that Gemini rejects with 400.
	if !strings.HasPrefix(fd.MimeType, "video/") || fd.MimeType == "video/" {
		t.Errorf("Gemini mimeType = %q, want a concrete video/* type (never the empty-suffix %q)", fd.MimeType, "video/")
	}
}

// TestOpenAIVideoURL_OpenAIWireUnchanged pins the "only touch what must
// change" half of the fix: the OpenAI wire form of a video_url block must be
// byte-identical to what it was before R148 (the default branch used to
// pass the captured raw JSON through verbatim).
//
// If this ever changes, the R148 fix has leaked into the three protocols it
// was explicitly not supposed to touch.
func TestOpenAIVideoURL_OpenAIWireUnchanged(t *testing.T) {
	irReq := parseOpenAIVideoURLBody(t, `{
		"model": "gpt-4o",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "what happens here"},
				{"type": "video_url", "video_url": {"url": "https://example.com/clip.mp4"}}
			]
		}]
	}`)

	out, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if !strings.Contains(string(out), `"video_url":{"url":"https://example.com/clip.mp4"}`) {
		t.Errorf("OpenAI wire form changed: %s", out)
	}
}

// TestOpenAIVideoURL_EmptyURIIsNotEmitted is the negative control: the pin
// above must not be satisfiable by "emit something". A video_url block with
// no URL has no Gemini representation, so no fileData part may be produced
// (emitting fileUri:"" is a malformed request — the same reasoning the
// image/audio paths already document).
func TestOpenAIVideoURL_EmptyURIIsNotEmitted(t *testing.T) {
	irReq := parseOpenAIVideoURLBody(t, `{
		"model": "gemini-2.5-pro",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "hello"},
				{"type": "video_url", "video_url": {"url": ""}}
			]
		}]
	}`)

	out, err := SerializeGemini(irReq)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}
	if strings.Contains(string(out), "fileData") {
		t.Errorf("an empty-URL video_url must not produce a Gemini fileData part: %s", out)
	}
}
