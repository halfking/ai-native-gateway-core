package sanitize

// media_envelope_test.go — 2026-11-13 脱敏域逐条审计 P2 收口钉测。
//
// 背景：/v1/images/generations 与 /v1/videos/generations 此前完全绕过
// SmartSaniGuard（sanitizeEnvelope 的 switch 没有 media 路径分支，default
// 分支找 messages 找不到 ⇒ 原样直通；端点自身也不挂中间件/还原）。
// 本文件钉输入侧：media 路径的 prompt 必须入洗、元数据必须不动、
// 无 prompt 的 body 不报错。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newMediaTestMiddleware(t *testing.T) *SanitizeInputMiddleware {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, nil, 0)
	require.NoError(t, err)
	return mw
}

// 请求侧：media prompt 中的敏感值必须被替换成占位符，且只有 prompt 动。
func TestMediaEnvelopePromptSanitized(t *testing.T) {
	mw := newMediaTestMiddleware(t)

	var upstreamBody []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = readBody(r)
		w.WriteHeader(http.StatusOK)
	})

	for _, path := range []string{"/v1/images/generations", "/v1/videos/generations"} {
		t.Run(path, func(t *testing.T) {
			upstreamBody = nil
			body := `{"model":"dall-e-3","prompt":"画一张包含 13800138000 的海报","size":"1024x1024","n":1}`
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req = req.WithContext(WithAuthenticatedTenant(req.Context(), "default"))
			mw.Wrap(inner).ServeHTTP(httptest.NewRecorder(), req)

			var got map[string]any
			require.NoError(t, json.Unmarshal(upstreamBody, &got))
			prompt, _ := got["prompt"].(string)
			require.Contains(t, prompt, "{SENSITIVE:phone:")
			require.NotContains(t, prompt, "13800138000", "敏感值不得出上游")
			// 元数据不动
			require.Equal(t, "dall-e-3", got["model"])
			require.Equal(t, "1024x1024", got["size"])
			require.Equal(t, float64(1), got["n"])
		})
	}
}

// 无 prompt 的 media body 必须直通（n/model-only 等形态不报错）。
func TestMediaEnvelopeNoPromptPassthrough(t *testing.T) {
	mw := newMediaTestMiddleware(t)
	var upstreamBody []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = readBody(r)
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"dall-e-3"}`))
	req = req.WithContext(WithAuthenticatedTenant(req.Context(), "default"))
	rec := httptest.NewRecorder()
	mw.Wrap(inner).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, bytes.Equal(upstreamBody, []byte(`{"model":"dall-e-3"}`)) || json.Valid(upstreamBody))
}
