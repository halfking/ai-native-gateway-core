package sanitize

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/stretchr/testify/require"
)

func TestChatNonStreamRestoresAllVisibleTextLanes(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-chat", "chat-visible-lanes"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"choices":[` +
		`{"message":{"role":"assistant","content":"answer {SENSITIVE:phone:1}","refusal":"unknown {SENSITIVE:phone:99}","reasoning_content":"think {SENSITIVE:phone:1}","function_call":{"arguments":"{\"to\":\"{SENSITIVE:phone:1}\"}"},"tool_calls":[{"function":{"arguments":"{\"to\":\"{SENSITIVE:phone:1}\"}"}}]}},` +
		`{"text":"completion {SENSITIVE:phone:1}"}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	var got struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content          string `json:"content"`
				Refusal          string `json:"refusal"`
				ReasoningContent string `json:"reasoning_content"`
				FunctionCall     struct {
					Arguments string `json:"arguments"`
				} `json:"function_call"`
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Equal(t, "answer 13800138000", got.Choices[0].Message.Content)
	require.Equal(t, "unknown [REDACTED]", got.Choices[0].Message.Refusal)
	require.Equal(t, "think 13800138000", got.Choices[0].Message.ReasoningContent)
	require.Equal(t, "completion 13800138000", got.Choices[1].Text)
	for _, arguments := range []string{got.Choices[0].Message.FunctionCall.Arguments, got.Choices[0].Message.ToolCalls[0].Function.Arguments} {
		var arg map[string]any
		require.NoError(t, json.Unmarshal([]byte(arguments), &arg))
		require.Equal(t, "13800138000", arg["to"])
	}
	require.NotContains(t, string(result.ModifiedBody), "{SENSITIVE:")
}

func TestRecognizedNonStreamResponseBlocksUnrestoredMarker(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-residual", "session-residual"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	for _, body := range []string{
		`{"choices":[{"message":{"role":"assistant","content":"safe","vendor_unknown":"{SENSITIVE:phone:1}"}}]}`,
		`{"choices":[{"message":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"{SENSITIVE:phone:1}"}}]}}]}`,
		`{"type":"message","content":[{"type":"image","source":{"data":"{SENSITIVE:phone:1}"}}]}`,
		`{"object":"response","output":[{"type":"message","content":[{"type":"output_audio","audio":"{SENSITIVE:phone:1}"}]}]}`,
	} {
		result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
			TenantID: tenant, SessionID: session, ResponseBody: []byte(body),
		})
		require.NoError(t, err)
		require.NotNil(t, result, body)
		require.True(t, result.ShouldBlock, body)
		require.Empty(t, result.ModifiedBody, body)
	}
}

func TestEncodedToolArgumentMarkerIsRestored(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "tenant-escaped", "session-escaped"
	require.NoError(t, rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"object":"response","output":[{"type":"function_call","arguments":"{\"to\":\"\\u007bSENSITIVE:phone:1}\"}"}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.Contains(t, string(result.ModifiedBody), "13800138000")
	require.NotContains(t, string(result.ModifiedBody), "SENSITIVE:")
	otherTenant, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: "tenant-escaped-other", SessionID: session, ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, otherTenant)
	require.False(t, otherTenant.ShouldBlock)
	require.Contains(t, string(otherTenant.ModifiedBody), "[REDACTED]")
	require.NotContains(t, string(otherTenant.ModifiedBody), "13800138000")
}
