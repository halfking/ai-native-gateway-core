package streaming

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// TestParseMiniMaxBaseResp verifies the base_resp.status_code parser.
func TestParseMiniMaxBaseResp(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantStatusCode int
		wantStatusMsg  string
		wantIsError    bool
	}{
		{
			name:           "success (status_code=0)",
			body:           `{"id":"req1","base_resp":{"status_code":0,"status_msg":"Success"},"choices":[{"message":{"content":"hello"}}]}`,
			wantStatusCode: 0,
			wantStatusMsg:  "Success",
			wantIsError:    false,
		},
		{
			name:           "rate_limit (status_code=1002)",
			body:           `{"base_resp":{"status_code":1002,"status_msg":"Rate limit exceeded"}}`,
			wantStatusCode: 1002,
			wantStatusMsg:  "Rate limit exceeded",
			wantIsError:    true,
		},
		{
			name:           "insufficient_balance (status_code=1008)",
			body:           `{"base_resp":{"status_code":1008,"status_msg":"Insufficient balance"}}`,
			wantStatusCode: 1008,
			wantStatusMsg:  "Insufficient balance",
			wantIsError:    true,
		},
		{
			name:           "content_filter (status_code=1027)",
			body:           `{"base_resp":{"status_code":1027,"status_msg":"Output invalid"}}`,
			wantStatusCode: 1027,
			wantStatusMsg:  "Output invalid",
			wantIsError:    true,
		},
		{
			name:           "context_length (status_code=1039)",
			body:           `{"base_resp":{"status_code":1039,"status_msg":"Token limit exceeded"}}`,
			wantStatusCode: 1039,
			wantStatusMsg:  "Token limit exceeded",
			wantIsError:    true,
		},
		{
			name:           "no base_resp field",
			body:           `{"id":"req1","choices":[{"message":{"content":"hello"}}]}`,
			wantStatusCode: 0,
			wantStatusMsg:  "",
			wantIsError:    false,
		},
		{
			name:           "invalid JSON",
			body:           `{broken`,
			wantStatusCode: 0,
			wantStatusMsg:  "",
			wantIsError:    false,
		},
		{
			name:           "empty body",
			body:           ``,
			wantStatusCode: 0,
			wantStatusMsg:  "",
			wantIsError:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg, isErr := ParseMiniMaxBaseResp([]byte(tc.body))
			assert.Equal(t, tc.wantStatusCode, code, "status_code")
			assert.Equal(t, tc.wantStatusMsg, msg, "status_msg")
			assert.Equal(t, tc.wantIsError, isErr, "isError")
		})
	}
}

// TestClassifyMiniMaxStatusCode verifies error kind mapping.
func TestClassifyMiniMaxStatusCode(t *testing.T) {
	cases := []struct {
		statusCode int
		wantKind   errorsx.ErrorKind
	}{
		{0, ""},
		{1001, errorsx.KindTimeout},
		{1002, errorsx.KindRateLimit},
		{1004, errorsx.KindAuth},
		{1008, errorsx.KindQuota},
		{1013, errorsx.KindUpstreamDown},
		{1027, errorsx.KindContentFilter},
		{1039, errorsx.KindContextLength},
		{2013, errorsx.KindClientBug},
		{9999, errorsx.KindUpstreamDown}, // unknown code → upstream_down
	}

	for _, tc := range cases {
		t.Run(string(tc.wantKind), func(t *testing.T) {
			got := ClassifyMiniMaxStatusCode(tc.statusCode)
			assert.Equal(t, tc.wantKind, got)
		})
	}
}

// TestFormatMiniMaxError verifies error message formatting.
func TestFormatMiniMaxError(t *testing.T) {
	cases := []struct {
		code int
		msg  string
		want string
	}{
		{1002, "Rate limit exceeded", "MiniMax error 1002: Rate limit exceeded"},
		{1008, "", "MiniMax error 1008"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := FormatMiniMaxError(tc.code, tc.msg)
			assert.Equal(t, tc.want, got)
		})
	}
}
