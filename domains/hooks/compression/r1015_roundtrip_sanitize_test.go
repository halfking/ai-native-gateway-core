package compression_test

// 2026-10-11 端到端回归门：脱敏 → 压缩摘要 → LLM 回显 → 还原 全链路不丢数据。
//
// 与 security/sanitize 内部测试的区别：这里的 Redis 映射不是手工播种的，
// 而是真实的 SanitizeInputMiddleware 在请求边界写入的（tenant-scoped 键 +
// offset + generation）。输入侧与还原侧任何键/代次不咬合，本测试立刻红。
//
// 压缩环节用 markerbound 安全截断模拟「摘要保留历史占位符」的产物形态；
// 截断边界本身的完整性由 compression/transformation 包内测试钉住。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/internal/markerbound"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newRoundTripStack(t *testing.T) (*sanitize.SanitizeInputMiddleware, *sanitize.SanitizeRestoreInterceptor, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	san, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	input, err := sanitize.NewSanitizeInputMiddleware(san, rdb, 30*time.Minute)
	require.NoError(t, err)
	restore, err := sanitize.NewSanitizeRestoreInterceptor(san, rdb, 30*time.Minute)
	require.NoError(t, err)
	return input, restore, mr
}

func TestRoundTripSanitizeCompressEchoRestore(t *testing.T) {
	input, restore, _ := newRoundTripStack(t)
	const session = "roundtrip-session"

	// 1) 输入侧：真实中间件把敏感值换成占位符并持久化映射。
	var sanitizedBody []byte
	handler := input.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sanitizedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	inbound := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"我的手机号是 13800138000，邮箱 zhangsan.ops@example.com，麻烦记一下"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(inbound))
	req.Header.Set("X-Session-Id", session)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, string(sanitizedBody), "13800138000")
	require.Contains(t, string(sanitizedBody), "{SENSITIVE:phone:")
	require.Contains(t, string(sanitizedBody), "{SENSITIVE:email:")

	// 2) 压缩环节：把脱敏历史截断成摘要输入（安全截断 = 修复后的压缩行为），
	//    摘要保留完整占位符。
	summaryInput := extractConversationStandalone(t, sanitizedBody)
	summaryInput = summaryInput[:markerbound.SafeCutKeepHead(summaryInput, 64)]
	require.True(t, strings.Contains(summaryInput, "{SENSITIVE:phone:") ||
		strings.Contains(summaryInput, "{SENSITIVE:email:"),
		"fixture: summary must still reference at least one placeholder")

	// 3) LLM 回显：模拟模型在回答里逐字带回占位符（canonical / 裸形态 /
	//    改写空格三种拼写）。
	echo := `{"choices":[{"message":{"role":"assistant","content":` +
		mustMarshalJSON(t, "好的 "+phonePlaceholder(t, sanitizedBody)+" 已记录；正文 "+
			phonePlaceholder(t, sanitizedBody)+" 与 SENSITIVE:phone:1（裸形态）和 "+
			"{SENSITIVE: phone : 1}（改写形态）一致") + "}}]}"
	result, err := restore.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: session, ClientProtocol: "openai-chat", ResponseBody: []byte(echo),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotNil(t, result.ModifiedBody)

	// 4) 还原侧：三种拼写都必须回到原值，内部标记一个不剩。
	out := string(result.ModifiedBody)
	require.NotContains(t, out, "SENSITIVE")
	require.Contains(t, out, "13800138000")
	require.Equal(t, 4, strings.Count(out, "13800138000"), "all spellings restore to the same value: %s", out)

	// 5) 摘要文本自身（会写回下一轮请求）里的占位符同样能在下一轮还原。
	summaryEcho := `{"choices":[{"message":{"role":"assistant","content":` +
		mustMarshalJSON(t, summaryInput) + "}}]}"
	result, err = restore.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: session, ClientProtocol: "openai-chat", ResponseBody: []byte(summaryEcho),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.NotContains(t, string(result.ModifiedBody), "SENSITIVE")
}

func extractConversationStandalone(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	var b strings.Builder
	for _, m := range decoded.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func phonePlaceholder(t *testing.T, body []byte) string {
	t.Helper()
	match := regexp.MustCompile(`\{SENSITIVE:phone:\d+\}`).FindString(string(body))
	require.NotEmpty(t, match)
	return match
}

func mustMarshalJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}
