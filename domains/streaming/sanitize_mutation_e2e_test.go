package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// mutateLikeAModel re-spells every issued marker the way a model actually
// mutates one while carrying it through generated output: a reflowed colon
// run, a lowercased spelling, and a zero-padded index.
//
// This is the whole failure the tolerant restore stage exists for: each marker
// is still the same marker, but no longer byte-identical, so an exact-match
// restore cannot find it.
func mutateLikeAModel(text string, issued map[string]string) string {
	styles := map[string]func(string) string{
		// reflowed out of "key: value" context
		"{SENSITIVE:internal_ip:1}": func(t string) string { return "{SENSITIVE: internal_ip : 1}" },
		// lowercased and zero-padded
		"{SENSITIVE:secret:1}": func(t string) string { return "{sensitive:secret:01}" },
		// spaced around the separator
		"{SENSITIVE:secret:2}": func(t string) string { return "{SENSITIVE : secret : 2}" },
	}
	out := text
	for token, style := range styles {
		if _, ok := issued[token]; ok {
			out = strings.ReplaceAll(out, token, style(token))
		}
	}
	return out
}

// TestSanitizeRoundTripSurvivesModelMutation is the end-to-end regression for
// the reported problem: a request carrying a host address and credentials must
// reach the provider with placeholders, and must come back to the client with
// the original values intact even though the model re-spelled every marker on
// the way out.
func TestSanitizeRoundTripSurvivesModelMutation(t *testing.T) {
	const (
		tenant     = "tenant-mutation-e2e"
		sessionID  = "gw_mutation_e2e"
		hostIP     = "10.24.7.91"
		password   = "Sup3rS3cret!"
		apiToken   = "sk-ABCDEF0123456789abcdef"
		clientBody = `{"model":"m","stream":true,"messages":[{"role":"user","content":` +
			`"deploy target; host: ` + hostIP + `; password: ` + password + `; api_key: ` + apiToken + `"}]}`
	)

	mini, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mini.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	mw, err := sanitize.NewSanitizeInputMiddleware(s, rdb, time.Minute)
	require.NoError(t, err)

	// --- inbound leg: the real input middleware rewrites the client request ---
	capture := &correlationCaptureExecutor{}
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.SetSanitizeInputMiddleware(mw.Wrap)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: tenant, ApplicationID: 7}})
	h.provider = durableEndpointResolver{}
	h.executor = &executors.Executor{}
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	h.survivalAttemptExec = capture

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(clientBody))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("X-Gw-Session-Id", sessionID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.NotNil(t, capture.params, "request never reached the provider boundary")
	providerBody := string(capture.params.BodyBytes)
	require.Contains(t, providerBody, "{SENSITIVE:", "provider body must carry placeholders")
	for _, secret := range []string{hostIP, password, apiToken} {
		require.NotContains(t, providerBody, secret, "raw secret must not reach the provider")
	}

	// --- outbound leg: the model re-spells every marker it was shown ---
	values, err := rdb.HGetAll(context.Background(),
		sanitize.SanitizeRedisKey(sanitize.HashTenant(tenant), sessionID)).Result()
	require.NoError(t, err)
	require.NotEmpty(t, values, "no session map to restore from")
	for _, secret := range []string{hostIP, password, apiToken} {
		require.Contains(t, strings.Join(mapValues(values), ","), secret, "session map lost a value")
	}

	modelReply := `{"choices":[{"message":{"role":"assistant","content":` +
		`"To deploy, connect to {SENSITIVE:internal_ip:1} with password {SENSITIVE:secret:1}` +
		` and token {SENSITIVE:secret:2}"}}]}`
	modelReply = mutateLikeAModel(modelReply, values)
	for token := range values {
		require.NotContains(t, modelReply, token,
			"fixture must exercise a re-spelled marker, not an exact echo")
	}

	restore, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	result, err := restore.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: sessionID, ClientProtocol: "openai-chat",
		ResponseBody: []byte(modelReply),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "a re-spelled marker must not fail the response closed")

	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Len(t, got.Choices, 1)
	require.Equal(t, "To deploy, connect to "+hostIP+" with password "+password+" and token "+apiToken,
		got.Choices[0].Message.Content)
	require.NotContains(t, string(result.ModifiedBody), "SENSITIVE", "no internal marker may reach the client")
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}

// TestSanitizeRoundTripForgedMarkerIsStillMasked keeps the tolerant stage from
// widening what the client can see: a marker this session never issued is still
// redacted even when the model re-spells it.
func TestSanitizeRoundTripForgedMarkerIsStillMasked(t *testing.T) {
	const tenant, sessionID = "tenant-mutation-forged", "gw_mutation_forged"

	mini, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mini.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	require.NoError(t, rdb.HSet(context.Background(),
		sanitize.SanitizeRedisKey(sanitize.HashTenant(tenant), sessionID),
		"{SENSITIVE:secret:1}", "Sup3rS3cret!").Err())

	restore, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	require.NoError(t, err)
	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"real {SENSITIVE: secret : 1} forged {SENSITIVE: secret : 9}"}}]}`)

	result, err := restore.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: sessionID, ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)

	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &got))
	require.Equal(t, "real Sup3rS3cret! forged [REDACTED]", got.Choices[0].Message.Content)
	require.NotContains(t, string(result.ModifiedBody), "SENSITIVE")
}
