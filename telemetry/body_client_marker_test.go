package telemetry

import (
	"net/http/httptest"
	"testing"
)

// TestExtractClientTypeFromBody_Zcode verifies the parameter-level
// identification of the ZCode CLI. ZCode sends
// `metadata.zcode_version` in its body, and names its built-in tools
// `zcode_*` (e.g. `zcode_bash`, `zcode_edit`).
//
// 2026-09-21 audit (P1: client detection parity for domestic coding agents).
func TestExtractClientTypeFromBody_Zcode(t *testing.T) {
	t.Run("metadata.zcode_version", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4o","metadata":{"zcode_version":"1.2.3"},"messages":[{"role":"user","content":"hi"}]}`)
		if got := ExtractClientTypeFromBody(body, ""); got != "zcode" {
			t.Errorf("got %q, want %q", got, "zcode")
		}
	})
	t.Run("zcode_* tools", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet","tools":[{"type":"function","function":{"name":"zcode_bash"}},{"type":"function","function":{"name":"zcode_edit"}}]}`)
		if got := ExtractClientTypeFromBody(body, ""); got != "zcode" {
			t.Errorf("got %q, want %q", got, "zcode")
		}
	})
}

// TestExtractClientTypeFromBody_MiniMaxCode verifies the MiniMax Code
// parameter-level identification. MiniMax Code sets
// `metadata.client_type == "minimax_code"` and the `X-Code-Session-Id`
// header (which we accept as a header-only fallback so the detection still
// works for clients that send the header without a parseable body).
//
// 2026-09-21 audit.
func TestExtractClientTypeFromBody_MiniMaxCode(t *testing.T) {
	t.Run("metadata.client_type", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4.6","metadata":{"client_type":"minimax_code","user_id":"u_01HXX"},"messages":[{"role":"user","content":"hi"}]}`)
		if got := ExtractClientTypeFromBody(body, ""); got != "minimax-code" {
			t.Errorf("got %q, want %q", got, "minimax-code")
		}
	})
	t.Run("X-Code-Session-Id header only", func(t *testing.T) {
		// Empty body — only the header signal is available. The header-only
		// fallback must still resolve to minimax-code so requests with
		// stripped body (e.g. multipart upload paths) don't drop the
		// identification.
		if got := ExtractClientTypeFromBody(nil, "sess_01HYYYYY"); got != "minimax-code" {
			t.Errorf("got %q, want %q", got, "minimax-code")
		}
	})
	t.Run("metadata.client_type with hyphens", func(t *testing.T) {
		body := []byte(`{"metadata":{"client_type":"minimax-code"}}`)
		if got := ExtractClientTypeFromBody(body, ""); got != "minimax-code" {
			t.Errorf("got %q, want %q", got, "minimax-code")
		}
	})
}

// TestExtractClientTypeFromBody_DeepSeekCode verifies DeepSeek Code
// parameter-level identification. The most reliable signal is the
// `model` field starting with `deepseek-` (the model's vendor prefix);
// alternative signals are the `metadata.deepseek_session_id` field and
// the snake_case tool names `read_file`, `execute_command`, etc.
//
// 2026-09-21 audit.
func TestExtractClientTypeFromBody_DeepSeekCode(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"model deepseek-chat", `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`},
		{"model deepseek-reasoner", `{"model":"deepseek-reasoner","stream":true}`},
		{"model deepseek-v3", `{"model":"deepseek-v3-chat"}`},
		{"model deepseek-v3.2-exp", `{"model":"deepseek-v3.2-exp"}`},
		{"model deepseek-coder (legacy)", `{"model":"deepseek-coder"}`},
		{"metadata.deepseek_session_id", `{"metadata":{"deepseek_session_id":"sess_X"}}`},
		{"tool read_file", `{"tools":[{"type":"function","function":{"name":"read_file"}}]}`},
		{"tool execute_command", `{"tools":[{"type":"function","function":{"name":"execute_command"}}]}`},
		{"tool edit_file", `{"tools":[{"type":"function","function":{"name":"edit_file"}}]}`},
		{"tool search_files", `{"tools":[{"type":"function","function":{"name":"search_files"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractClientTypeFromBody([]byte(tc.body), ""); got != "deepseek-code" {
				t.Errorf("got %q, want %q", got, "deepseek-code")
			}
		})
	}
}

// TestExtractClientTypeFromBody_NoMatch ensures the function is silent
// on bodies that don't carry a marker — it must return "" and not panic
// or produce a false-positive.
func TestExtractClientTypeFromBody_NoMatch(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{}`),
		[]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		nil,
		[]byte(`{not json`),
	}
	for _, b := range bodies {
		if got := ExtractClientTypeFromBody(b, ""); got != "" {
			t.Errorf("body=%q: got %q, want empty", string(b), got)
		}
	}
}

// TestExtractAgentNameFromRequest_BodyMarker verifies the body-marker
// path inside ExtractAgentNameFromRequest runs when the User-Agent is
// generic but the body carries a marker.
func TestExtractAgentNameFromRequest_BodyMarker(t *testing.T) {
	// Generic Go HTTP client — no useful UA — but body has minimax-code marker.
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("User-Agent", "Go-http-client/1.1")
	body := []byte(`{"model":"claude-sonnet-4.6","metadata":{"client_type":"minimax_code"},"messages":[{"role":"user","content":"hi"}]}`)
	if got := ExtractAgentNameFromRequest(req, body); got != "minimax-code" {
		t.Errorf("got %q, want %q", got, "minimax-code")
	}
}
