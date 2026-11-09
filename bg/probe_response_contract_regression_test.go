package bg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompleteProbeJSONBody(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		readErr error
		want    string
	}{
		{name: "complete json", body: []byte(`{"ok":true}`), want: `{"ok":true}`},
		{name: "json with whitespace", body: []byte(" \n{\"ok\":true}\n "), want: " \n{\"ok\":true}\n "},
		{name: "truncated json", body: []byte(`{"ok":`)},
		{name: "plain text", body: []byte("upstream unavailable")},
		{name: "read failure", body: []byte(`{"ok":true}`), readErr: io.ErrUnexpectedEOF},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, completeProbeJSONBody(tt.body, tt.readErr))
		})
	}
}

func TestCompleteProbeJSONBodyRedactsSecretsWithoutBreakingJSON(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"Bearer abcdefghijklmnop sk-abcdefghijklmnopqrstuvwxyz"}}],"token":"short-secret"}`)
	got := completeProbeJSONBody(body, nil)
	require.NotEmpty(t, got)
	require.True(t, json.Valid([]byte(got)))
	require.NotContains(t, got, "abcdefghijklmnop")
	require.NotContains(t, got, "sk-abcdefghijklmnopqrstuvwxyz")
	require.NotContains(t, got, "short-secret")
}

func TestProbeResponseBodyContractRejectsNonJSONReadFailureAndOversize(t *testing.T) {
	longValid := `{"choices":[{"message":{"content":"` + strings.Repeat("x", credentialProbeBodyLimit) + `"}}]}`
	tests := []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "non json",
			transport: probeResponseTransport(func() *http.Response {
				return probeHTTPResponse(http.StatusOK, "upstream unavailable")
			}),
		},
		{
			name: "read failure",
			transport: probeResponseTransport(func() *http.Response {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &probeReadErrorBody{}}
			}),
		},
		{
			name: "oversize",
			transport: probeResponseTransport(func() *http.Response {
				return probeHTTPResponse(http.StatusOK, longValid)
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.transport, Timeout: time.Second}
			result := (&ActiveProbeExecutor{httpClient: client}).Run(context.Background(), &ProbeTarget{
				RawModel: "gpt-test", BaseURL: "http://probe.test", Protocol: "openai-completions",
			})
			require.NotNil(t, result)
			require.Empty(t, result.ResponseBody)
			if tt.name == "non json" {
				require.NotEmpty(t, result.RespPreview)
			}
		})
	}
}

func TestProbeEmitterKeepsPreviewSeparateFromCompleteBody(t *testing.T) {
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("pong ", 160) + `"}}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`
	preview := truncatePreview(body, 500)
	entry := buildProbeRequestLogEntry(7, 1, "default", "gpt-test", "gpt-test", "direct", "", 1, &ProbeResult{
		Status: ProbeStatusSuccess, RespPreview: preview, ResponseBody: body,
	})
	require.NotNil(t, entry.ResponsePreview)
	require.Equal(t, preview, *entry.ResponsePreview)
	require.NotNil(t, entry.ResponseBody)
	require.Equal(t, body, *entry.ResponseBody)
}

func TestNodeProbeKeepsPreviewSeparateFromCompleteBody(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"` + strings.Repeat("pong ", 160) + `"}}],"usage":{"prompt_tokens":17,"completion_tokens":3}}`)
	result := nodeProbeRoundResult{httpStatus: http.StatusOK}
	setNodeProbeResponse(&result, "chat", body, nil)
	require.LessOrEqual(t, len(result.responseBody), 512)
	require.Equal(t, string(body), result.responseJSONBody)

	result = nodeProbeRoundResult{httpStatus: http.StatusOK}
	setNodeProbeResponse(&result, "chat", body, io.ErrUnexpectedEOF)
	require.Empty(t, result.responseJSONBody)
}

func probeResponseTransport(response func() *http.Response) http.RoundTripper {
	return probeResponseRoundTripper(func(*http.Request) (*http.Response, error) {
		return response(), nil
	})
}

type probeResponseRoundTripper func(*http.Request) (*http.Response, error)

func (rt probeResponseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt(req)
}

type probeReadErrorBody struct{}

func (probeReadErrorBody) Read(p []byte) (int, error) {
	return copy(p, `{"partial":`), io.ErrUnexpectedEOF
}

func (probeReadErrorBody) Close() error { return nil }

func probeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
