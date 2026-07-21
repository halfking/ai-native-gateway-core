package ir

import (
	"strings"
	"testing"
)

func TestSerializeAnthropicRejectsUnsupportedMedia(t *testing.T) {
	for _, typ := range []string{"audio", "input_audio", "video"} {
		t.Run(typ, func(t *testing.T) {
			_, err := SerializeAnthropic(&InternalRequest{
				Model:    "claude",
				Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: typ}}}},
			})
			if err == nil || !strings.Contains(err.Error(), "unsupported_modality") {
				t.Fatalf("err=%v, want unsupported_modality", err)
			}
		})
	}
}
