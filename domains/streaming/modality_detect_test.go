package streaming

import "testing"

// TestDetectRequestModality_TextOnly covers pure-text requests (no media).
// Routing modality must be "text" so no provider is filtered out.
func TestDetectRequestModality_TextOnly(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"openai string content", `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`},
		{"openai array with only text blocks", `{"model":"gpt-4","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"anthropic string content", `{"model":"claude-opus-4","messages":[{"role":"user","content":"hi"}]}`},
		{"gemini native parts text-only", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"empty body", ``},
		{"invalid JSON", `not json`},
		{"just a model field", `{"model":"gpt-4"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != "text" {
				t.Errorf("detectRequestModality = %q, want text", got)
			}
		})
	}
}

// TestDetectRequestModality_Vision covers vision requests across the three
// protocol shapes (OpenAI image_url, Anthropic image block, Gemini inlineData).
func TestDetectRequestModality_Vision(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"openai image_url data URI", `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR..."}}]}]}`},
		{"openai image_url http URL", `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}`},
		{"anthropic image base64", `{"model":"claude-opus-4","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR..."}}]}]}`},
		{"anthropic image url", `{"model":"claude-opus-4","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/x.png"}}]}]}`},
		{"openai file block image MIME", `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"file","file":{"mime_type":"image/png"}}]}]}`},
		{"input_image type", `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"input_image"}]}]}`},
		{"plain image type", `{"model":"claude-opus-4","messages":[{"role":"user","content":[{"type":"image"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != "vision" {
				t.Errorf("detectRequestModality = %q, want vision", got)
			}
		})
	}
}

// TestDetectRequestModality_Audio covers audio-input detection.
func TestDetectRequestModality_Audio(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"openai input_audio", `{"model":"gpt-4o-audio","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`},
		{"anthropic audio block", `{"model":"claude-opus-4","messages":[{"role":"user","content":[{"type":"audio","source":{"type":"base64","media_type":"audio/wav","data":"AAAA"}}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != "audio" {
				t.Errorf("detectRequestModality = %q, want audio", got)
			}
		})
	}
}

// TestDetectRequestModality_Video covers video-input detection.
func TestDetectRequestModality_Video(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"openai video_url", `{"model":"qwen-vl","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/x.mp4"}}]}]}`},
		{"anthropic video block", `{"model":"claude-opus-4","messages":[{"role":"user","content":[{"type":"video","source":{"type":"url","url":"https://example.com/x.mp4"}}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != "video" {
				t.Errorf("detectRequestModality = %q, want video", got)
			}
		})
	}
}

// TestDetectRequestModality_GeminiInlineData verifies Gemini native parts
// with inlineData/image MIME are recognised as vision. Also covers
// inlineData audio/video.
func TestDetectRequestModality_GeminiInlineData(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"image/png inlineData", `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"iVBOR"}}]}]}`, "vision"},
		{"audio/wav inlineData", `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"AAA"}}]}]}`, "audio"},
		{"video/mp4 inlineData", `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"AAA"}}]}]}`, "video"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != tc.want {
				t.Errorf("detectRequestModality = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectRequestModality_Priority documents the priority ordering when
// a request carries multiple modality types. video > audio > vision.
func TestDetectRequestModality_Priority(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"image+audio → audio",
			`{"messages":[{"role":"user","content":[
				{"type":"image","source":{"type":"url","url":"https://x/a.png"}},
				{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}}
			]}]}`,
			"audio",
		},
		{
			"image+video → video",
			`{"messages":[{"role":"user","content":[
				{"type":"image","source":{"type":"url","url":"https://x/a.png"}},
				{"type":"video","source":{"type":"url","url":"https://x/b.mp4"}}
			]}]}`,
			"video",
		},
		{
			"audio+video → video",
			`{"messages":[{"role":"user","content":[
				{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}},
				{"type":"video","source":{"type":"url","url":"https://x/b.mp4"}}
			]}]}`,
			"video",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != tc.want {
				t.Errorf("detectRequestModality = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectRequestModality_FileBlockNonImage verifies that file blocks
// with non-image MIME hints (audio, pdf, octet-stream) fall back to "text"
// routing. OpenAI file blocks are generic; we do not assume audio/pdf from
// a file block. Upstream will reject unsupported MIME with its own error
// if needed. This test guards against false positives.
func TestDetectRequestModality_FileBlockNonImage(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"file","file":{"mime_type":"application/pdf"}}]}]}`
	if got := detectRequestModality([]byte(body)); got != "text" {
		t.Errorf("non-image file block: got %q, want text", got)
	}
}
