package streaming

import (
	"strings"
	"testing"
)

func TestStripChunkFieldsForVendorUsesMatchingVendor(t *testing.T) {
	tests := []struct {
		name       string
		vendor     string
		payload    string
		wantAbsent string
		wantCode   int
	}{
		{
			name:       "minimax error is detected",
			vendor:     " MiniMax ",
			payload:    `{"base_resp":{"status_code":1008,"status_msg":"quota"}}`,
			wantAbsent: "base_resp",
			wantCode:   1008,
		},
		{
			name:       "doubao does not use minimax error classifier",
			vendor:     "DOUBAO",
			payload:    `{"base_resp":{"status_code":1008,"status_msg":"provider metadata"},"doubao_request_id":"private"}`,
			wantAbsent: "doubao_request_id",
		},
		{
			name:       "empty catalog detects zhipu fields",
			vendor:     "",
			payload:    `{"zhipu_request_id":"private","choices":[]}`,
			wantAbsent: "zhipu_request_id",
		},
		{
			name:       "empty catalog detects deepseek fields",
			vendor:     "",
			payload:    `{"deepseek_request_id":"private","choices":[]}`,
			wantAbsent: "deepseek_request_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, code, _ := stripChunkFieldsForVendor("data: "+tt.payload+"\n", tt.vendor, sanitizerForVendor(tt.vendor))
			if code != tt.wantCode {
				t.Fatalf("error code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantCode != 0 {
				if line != "data: "+tt.payload+"\n" {
					t.Fatalf("error line changed before caller handles it: %s", line)
				}
				return
			}
			if strings.Contains(line, tt.wantAbsent) {
				t.Fatalf("sanitized line still contains %q: %s", tt.wantAbsent, line)
			}
		})
	}
}

func TestResolveStreamVendorDoesNotMatchNestedUserText(t *testing.T) {
	payload := `{"choices":[{"delta":{"content":"mentions \\"base_resp\\" in documentation"}}]}`
	vendor, stripFn := resolveStreamVendor(payload, "", nil)
	if vendor != "" || stripFn != nil {
		t.Fatalf("nested text inferred vendor %q with sanitizer %v", vendor, stripFn != nil)
	}
}

func sanitizerForVendor(vendor string) func([]byte) []byte {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "minimax":
		return StripMinimaxFieldsBody
	case "doubao":
		return StripDoubaoFieldsBody
	case "zhipu", "glm":
		return StripZhipuFieldsBody
	case "deepseek":
		return StripDeepSeekFieldsBody
	default:
		return nil
	}
}
