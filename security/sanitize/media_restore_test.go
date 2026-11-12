package sanitize

// media_restore_test.go — RestoreMediaBody 四分支钉测（2026-11-13）。
//
// 契约（media_restore.go 头注）：直通 / 还原 / 打码 / fail-closed 拦截。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func newMediaRestoreTestInterceptor(t *testing.T) *SanitizeRestoreInterceptor {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	require.NoError(t, err)
	return it
}

// 直通：无映射且无保留标记 ⇒ (nil,false,nil)，响应零改写。
func TestRestoreMediaBodyPassthrough(t *testing.T) {
	it := newMediaRestoreTestInterceptor(t)
	body := []byte(`{"created":1,"data":[{"url":"https://img.example/1.png"}]}`)
	out, changed, err := it.RestoreMediaBody(context.Background(), body)
	require.NoError(t, err)
	require.False(t, changed)
	require.Nil(t, out)
}

// 还原：ctx 映射在场，revised_prompt 中的占位符换回原文。
func TestRestoreMediaBodyRestoresPlaceholder(t *testing.T) {
	it := newMediaRestoreTestInterceptor(t)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
	})
	body := []byte(`{"created":1,"data":[{"revised_prompt":"海报里有 {SENSITIVE:phone:1}","url":"https://x/1.png"}]}`)
	out, changed, err := it.RestoreMediaBody(ctx, body)
	require.NoError(t, err)
	require.True(t, changed)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	data := got["data"].([]any)[0].(map[string]any)
	require.Contains(t, data["revised_prompt"], "13800138000")
	require.NotContains(t, data["revised_prompt"], "{SENSITIVE:phone:")
	require.Equal(t, "https://x/1.png", data["url"], "无占位符的叶不得改写")
}

// 打码：映射缺席但响应带保留标记（中间件未启用/被绕过时的兜底）——
// 未知标记必须被 [REDACTED] 化，不得原样下发内部标记。
func TestRestoreMediaBodyMasksUnknownMarker(t *testing.T) {
	it := newMediaRestoreTestInterceptor(t)
	body := []byte(`{"data":[{"revised_prompt":"see {SENSITIVE:phone:9}"}]}`)
	out, changed, err := it.RestoreMediaBody(context.Background(), body)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, string(out), "{SENSITIVE:phone:9}")
}

// fail-closed：截断 JSON 携带保留标记 ⇒ ErrMediaRestoreBlocked，handler 502。
func TestRestoreMediaBodyBlocksMalformedWithMarker(t *testing.T) {
	it := newMediaRestoreTestInterceptor(t)
	body := []byte(`{"data":[{"revised_prompt":"see {SENSITIVE:phone:1}`)
	out, changed, err := it.RestoreMediaBody(context.Background(), body)
	require.ErrorIs(t, err, ErrMediaRestoreBlocked)
	require.False(t, changed)
	require.Nil(t, out)
}

// fail-closed：键名携带标记 ⇒ 行走器报错上抛（不半还原下发）。
func TestRestoreMediaBodyBlocksMarkerInKey(t *testing.T) {
	it := newMediaRestoreTestInterceptor(t)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
	})
	body := []byte(`{"data":{"{SENSITIVE:phone:1}":"x"}}`)
	out, _, err := it.RestoreMediaBody(ctx, body)
	require.Error(t, err)
	require.Nil(t, out)
}
