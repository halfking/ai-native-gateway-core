package streaming

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSessionResolutionPrecedence_BodyWinsOverHeader covers the body-side
// session resolution helper used by the chat handler since 2026-07-27. The
// body-supplied session id must take precedence over any client-supplied
// header, because provisional header values are not yet written at this
// point in the request lifecycle.
func TestSessionResolutionPrecedence_BodyWinsOverHeader(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		header     string
		wantResult string
	}{
		{
			name:       "body only",
			body:       `{"messages":[{"role":"user","content":"hi"}],"session_id":"gw_body"}`,
			header:     "",
			wantResult: "gw_body",
		},
		{
			name:       "header only",
			body:       `{"messages":[{"role":"user","content":"hi"}]}`,
			header:     "gw_header",
			wantResult: "gw_header",
		},
		{
			name:       "body wins over header",
			body:       `{"messages":[{"role":"user","content":"hi"}],"session_id":"gw_body"}`,
			header:     "gw_header",
			wantResult: "gw_body",
		},
		{
			name:       "missing both yields empty",
			body:       `{"messages":[{"role":"user","content":"hi"}]}`,
			header:     "",
			wantResult: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(tc.body))
			if tc.header != "" {
				req.Header.Set("X-Gw-Session-Id", tc.header)
			}
			fromBody := extractSessionIDFromBody([]byte(tc.body))
			fromHeader := extractSessionIDFromHeaders(req)
			got := fromBody
			if got == "" {
				got = fromHeader
			}
			if got != tc.wantResult {
				t.Fatalf("resolution: got %q want %q (body=%q header=%q)", got, tc.wantResult, fromBody, fromHeader)
			}
		})
	}
}

func TestSessionResolutionPrecedence_AllProtocolEntrypoints(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"session_id":"gw_body"}`)
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			req.Header.Set("X-Gw-Session-Id", "gw_header")
			if got := extractSessionIDFromRequest(req, body); got != "gw_body" {
				t.Fatalf("extractSessionIDFromRequest() = %q, want body session gw_body", got)
			}
		})
	}
}

// helper that writes the provisional session id into the request header
// for early-failure logging. It must never overwrite a value the
// request has already supplied.
func TestApplyProvisionalGatewaySessionHeader_DoesNotOverwrite(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Session-Id", "gw_client_provided")
	applyProvisionalGatewaySessionHeader(req, "gw_provisional_new")
	if got := req.Header.Get("X-Gw-Session-Id"); got != "gw_client_provided" {
		t.Fatalf("X-Gw-Session-Id was overwritten: got %q", got)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	applyProvisionalGatewaySessionHeader(req2, "gw_provisional_only")
	if got := req2.Header.Get("X-Gw-Session-Id"); !strings.HasPrefix(got, "gw_") {
		t.Fatalf("provisional session id not applied: got %q", got)
	}
}

// TestResponsesHasTools covers the helper used to populate
// ExecParams.ToolsRequested on the /v1/responses handler. Without this
// fix the executor always saw ToolsRequested=false and the streaming
// bridge would skip the XML→structured tool-call coercion pass.
func TestResponsesHasTools(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "no tools",
			body: `{"model":"gpt-4o","input":"hi"}`,
			want: false,
		},
		{
			name: "top-level tools",
			body: `{"model":"gpt-4o","input":"hi","tools":[{"type":"function","name":"get_weather"}]}`,
			want: true,
		},
		{
			name: "input item with tools",
			body: `{"model":"gpt-4o","input":[{"role":"user","content":"hi","tools":[{"type":"function","name":"echo"}]}]}`,
			want: true,
		},
		{
			name: "tools explicitly null",
			body: `{"model":"gpt-4o","input":"hi","tools":null}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := decodeResponsesRequestForTest([]byte(tc.body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := responsesHasTools(req); got != tc.want {
				t.Fatalf("responsesHasTools() = %v, want %v", got, tc.want)
			}
		})
	}
}

// decodeResponsesRequestForTest mirrors responsesRequestBody.UnmarshalJSON
// without depending on the request-context plumbing the real path uses.
func decodeResponsesRequestForTest(body []byte) (*responsesRequestBody, error) {
	type alias responsesRequestBody
	var decoded alias
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	delete(raw, "model")
	delete(raw, "input")
	delete(raw, "instructions")
	delete(raw, "max_output_tokens")
	delete(raw, "stream")
	delete(raw, "temperature")
	delete(raw, "top_p")
	out := responsesRequestBody(decoded)
	out.Extra = raw
	return &out, nil
}
