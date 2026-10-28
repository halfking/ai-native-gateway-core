package streaming

import "testing"

// TestDetectRequestModality_ResponsesAPIInput covers the OpenAI Responses
// API's request shape.
//
// ★ Why this test exists — it pins a defect found in production:
//
//	The detector walked messages[] and Gemini's contents[] but not the
//	Responses API's input[]. So a /v1/responses request carrying an image
//	was classified as TEXT ⇒ the modality gate never applied to that path
//	⇒ the image went upstream, the upstream rejected it with 400, and the
//	gateway reported "All providers unavailable" (503 provider_unavailable).
//
//	That message is worse than the modality mismatch we now emit: it says
//	the PROVIDERS are down, when in fact the request was the problem.
//	Every /v1/responses client with an image in the payload hit this.
//
//	The negative control matters as much as the positive one: a detector
//	that answered "vision" for everything would satisfy the first case.
func TestDetectRequestModality_ResponsesAPIInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "input_image block",
			body: `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:image/png;base64,AAA"}]}]}`,
			want: modalityVision,
		},
		{
			name: "input_text only",
			body: `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			want: modalityText,
		},
		{
			name: "plain string input",
			body: `{"model":"m","input":"hello"`,
			want: modalityText,
		},
		{
			name: "input_audio block",
			body: `{"model":"m","input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}}]}]}`,
			want: modalityAudio,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectRequestModality([]byte(tc.body)); got != tc.want {
				t.Errorf("detectRequestModality = %q, want %q", got, tc.want)
			}
		})
	}
}
