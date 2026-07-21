package anthropic

import (
	"strings"
	"testing"
)

func TestConvertChatRequestToAnthropicRejectsUnsupportedMedia(t *testing.T) {
	for _, typ := range []string{"input_audio", "video_url", "file"} {
		t.Run(typ, func(t *testing.T) {
			body := `{"model":"claude","messages":[{"role":"user","content":[{"type":"` + typ + `"}]}]}`
			_, err := ConvertChatRequestToAnthropic([]byte(body))
			if err == nil || !strings.Contains(err.Error(), "unsupported_modality") {
				t.Fatalf("err=%v, want unsupported_modality", err)
			}
		})
	}
}
