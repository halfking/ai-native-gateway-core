package audit

import "testing"

func TestStreamCaptureSummaryIncludesUsage(t *testing.T) {
	capture := NewStreamCapture()
	prompt, completion, cacheRead, cacheWrite := 7, 11, 13, 17
	capture.ObserveUsage(&prompt, &completion, &cacheRead, &cacheWrite)
	summary := capture.SummaryAsMap()
	for key, want := range map[string]int{
		"prompt_tokens":      7,
		"completion_tokens":  11,
		"cache_read_tokens":  13,
		"cache_write_tokens": 17,
	} {
		got, ok := summary[key].(int)
		if !ok || got != want {
			t.Fatalf("summary[%q]=%v, want %d", key, summary[key], want)
		}
	}
}
