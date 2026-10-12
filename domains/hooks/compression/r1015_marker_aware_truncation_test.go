package compression

// 2026-10-11 回归门：压缩链路四处字节/字符级截断不得切裂
// {SENSITIVE:type:index} 占位符。
//
// 截裂的后果：残缺标记进入摘要输入（摘要 LLM 会把它带回摘要文本）或直接
// 进入出向请求；模型回显后该占位符的还原永久失效，残缺内部标记还会让
// 响应侧残留探针阻断整条响应。所有截断边界现在走 internal/markerbound，
// 落在标记内的切点移到标记边界（保留侧保住完整标记）。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const truncMarker = "{SENSITIVE:phone:1}"

// assertNoSplitMarker 断言 text 中若出现 {SENSITIVE: 片段，必为完整合法标记。
func assertNoSplitMarker(t *testing.T, text string) {
	t.Helper()
	rest := text
	for {
		idx := strings.Index(rest, "{SENSITIVE:")
		if idx < 0 {
			// 也防裸形态残片：sensitive: 后跟被截断的类型/编号。
			if i := strings.Index(rest, "ENSITIVE:"); i >= 0 {
				t.Fatalf("marker fragment without opening brace: %q", rest)
			}
			return
		}
		end := strings.Index(rest[idx:], "}")
		if end < 0 {
			t.Fatalf("unterminated marker fragment in output: %q", rest[idx:])
		}
		rest = rest[idx+end+1:]
	}
}

func TestTruncateForLogKeepsBoundaryMarkerWhole(t *testing.T) {
	prefix := strings.Repeat("a", 505)
	text := prefix + truncMarker + "tail"
	// 切点 512 落在标记 [505,524) 内部。
	got := truncateForLog([]byte(text), 512)
	require.Contains(t, got, truncMarker, "boundary marker must survive whole")
	assertNoSplitMarker(t, got)
	// 无标记的普通文本照旧按长度截断。
	plain := strings.Repeat("b", 600)
	require.Len(t, truncateForLog([]byte(plain), 512), 512)
}

func TestTrimTextToTokenBudgetKeepsBoundaryMarkerWhole(t *testing.T) {
	text := strings.Repeat("a", 102) + truncMarker
	// len=121；budget=5 → maxChars=17 → 切点 104，落在标记 [102,121) 内。
	got := trimTextToTokenBudget(text, 5)
	require.Contains(t, got, truncMarker)
	assertNoSplitMarker(t, got)
	plain := strings.Repeat("b", 100)
	require.Equal(t, plain[83:], trimTextToTokenBudget(plain, 5))
}

func TestExtractSummaryTextKeepsBoundaryMarkerWhole(t *testing.T) {
	// 机械兜底摘要的产物直接进出向 body 与 SessionCache——这里是最高危的
	// 截断点。首条 user 消息的标记横跨 500 字节切点。
	long := strings.Repeat("context ", 61) + truncMarker + strings.Repeat("x", 100)
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"sys"},` +
		`{"role":"user","content":` + mustJSON(t, long) + `},` +
		`{"role":"assistant","content":` + mustJSON(t, strings.Repeat("ans ", 200)) + `},` +
		`{"role":"user","content":` + mustJSON(t, strings.Repeat("tail ", 100)+truncMarker) + `}` +
		`]}`)
	summary := extractSummaryText(body, CutPlan{SystemCount: 1, CutIndex: 2})
	require.NotEmpty(t, summary)
	assertNoSplitMarker(t, summary)
	require.Contains(t, summary, truncMarker)
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}
