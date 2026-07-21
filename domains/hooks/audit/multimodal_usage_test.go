package audit

import "testing"

func TestStreamCaptureSummaryIncludesMultimodalUsage(t *testing.T) {
	capture := NewStreamCapture()
	reasoning, image, audio, video, provider := 7, 11, 13, 17, 19
	capture.ObserveMultimodalUsage(&reasoning, &image, &audio, &video, &provider)
	summary := capture.SummaryAsMap()
	for key, want := range map[string]int{
		"reasoning_tokens": 7,
		"image_tokens":     11,
		"audio_tokens":     13,
		"video_tokens":     17,
		"provider_tokens":  19,
	} {
		got, ok := summary[key].(int)
		if !ok || got != want {
			t.Fatalf("summary[%q]=%v, want %d", key, summary[key], want)
		}
	}
}
