package streaming

// modality_e2e_test.go — Phase 2 Layer A E2E matrix (multimodal testing plan).
//
// This file supplements modality_detect_test.go with the three coverage gaps
// identified against the 00-test-plan.md matrix:
//
//   - T-09: tri-modal mixed request (text + image + audio) → priority video
//     (existing priority tests only cover 2-modality pairs)
//   - T-11: malformed base64 in image_url → detection layer is structural
//     only, so it returns "vision" (does not validate payload content, does
//     not panic). Upstream is responsible for rejecting invalid data.
//   - T-20: Gemini native fileData (vs inlineData already covered) with
//     image/audio/video mimeType → derives modality from MIME prefix
//
// Matrix mapping (T-01..T-20) to existing tests:
//
//	T-01 text             → TestDetectRequestModality_TextOnly
//	T-02 vision url       → TestDetectRequestModality_Vision "openai image_url http URL"
//	T-03 vision base64    → TestDetectRequestModality_Vision "openai image_url data URI"
//	T-04 anthropic source → TestDetectRequestModality_Vision "anthropic image base64"
//	T-05 audio input_audio→ TestDetectRequestModality_Audio "openai input_audio"
//	T-06 whisper upload   → (multipart, not detectRequestModality scope; Layer B)
//	T-07 video gemini     → TestDetectRequestModality_GeminiInlineData "video/mp4 inlineData"
//	T-08 video_url        → TestDetectRequestModality_Video "openai video_url"
//	T-09 tri-modal mixed  → TestE2E_DetectModality_Supplement (below)
//	T-10 invalid URL      → structural: returns vision (URL reachability is upstream's job)
//	T-11 malformed base64 → TestE2E_DetectModality_Supplement (below)
//	T-12 oversized image  → structural: returns vision (size limit is upstream's job)
//	T-13 empty file upload→ (multipart, not detectRequestModality scope; Layer B)
//	T-18 image+audio prio → TestDetectRequestModality_Priority "image+audio → audio"
//	T-20 gemini fileData  → TestE2E_DetectModality_Supplement (below)

import "testing"

// TestE2E_DetectModality_Supplement covers the three gaps above.
func TestE2E_DetectModality_Supplement(t *testing.T) {
	cases := []struct {
		id   string
		name string
		body string
		want string
	}{
		{
			"T-09", "tri-modal text+image+audio → video priority when video also present",
			`{"messages":[{"role":"user","content":[
				{"type":"text","text":"describe"},
				{"type":"image","source":{"type":"url","url":"https://x/a.png"}},
				{"type":"input_audio","input_audio":{"data":"AAA","format":"wav"}},
				{"type":"video","source":{"type":"url","url":"https://x/b.mp4"}}
			]}]}`,
			"video",
		},
		{
			"T-11", "malformed base64 in image_url → structural vision, no panic",
			`{"messages":[{"role":"user","content":[
				{"type":"image_url","image_url":{"url":"data:image/png;base64,!!!this-is-not-valid-base64!!!"}}
			]}]}`,
			"vision",
		},
		{
			"T-20a", "gemini fileData image mimeType → vision",
			`{"contents":[{"role":"user","parts":[
				{"fileData":{"mimeType":"image/png","fileUri":"https://x/a.png"}}
			]}]}`,
			"vision",
		},
		{
			"T-20b", "gemini fileData audio mimeType → audio",
			`{"contents":[{"role":"user","parts":[
				{"fileData":{"mimeType":"audio/wav","fileUri":"https://x/a.wav"}}
			]}]}`,
			"audio",
		},
		{
			"T-20c", "gemini fileData video mimeType → video",
			`{"contents":[{"role":"user","parts":[
				{"fileData":{"mimeType":"video/mp4","fileUri":"https://x/a.mp4"}}
			]}]}`,
			"video",
		},
	}
	for _, tc := range cases {
		t.Run(tc.id+"_"+tc.name, func(t *testing.T) {
			got := detectRequestModality([]byte(tc.body))
			if got != tc.want {
				t.Errorf("detectRequestModality = %q, want %q", got, tc.want)
			}
		})
	}
}
