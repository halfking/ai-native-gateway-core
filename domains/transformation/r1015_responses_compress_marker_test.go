package transformation

// 2026-10-11 回归门：Responses 字符串 input 的砍半压缩不得切裂
// {SENSITIVE:type:index} 占位符（产物原样上行，截裂标记 = 内部残片
// 直接发给上游，该值还原永久失效）。边界逻辑在 internal/markerbound。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const responsesTrimMarker = "{SENSITIVE:phone:1}"

func assertNoSplitMarkerTransform(t *testing.T, text string) {
	t.Helper()
	rest := text
	for {
		idx := strings.Index(rest, "{SENSITIVE:")
		if idx < 0 {
			return
		}
		end := strings.Index(rest[idx:], "}")
		if end < 0 {
			t.Fatalf("unterminated marker fragment in output: %q", rest[idx:])
		}
		rest = rest[idx+end+1:]
	}
}

func TestTrimResponsesInputStringKeepsBoundaryMarkerWhole(t *testing.T) {
	request := map[string]json.RawMessage{"model": json.RawMessage(`"m"`)}
	// 219 rune：首个砍半切点 109 落在标记 [100,119) 内。
	input := strings.Repeat("a", 100) + responsesTrimMarker + strings.Repeat("z", 100)
	body, err := json.Marshal(map[string]any{"model": "m", "input": input})
	require.NoError(t, err)

	// 全长 ≈68 token > 50；第一轮砍半后保留 119 rune ≈ 40 token ≤ 50，
	// 恰在标记感知调整后的形态停住。
	out, meta := trimResponsesInputString(request, input, 50, body, "test")
	require.NotNil(t, meta)
	var decoded struct {
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(out, &decoded))
	assertNoSplitMarkerTransform(t, decoded.Input)
	require.Contains(t, decoded.Input, responsesTrimMarker)
}

func TestTrimResponsesInputStringTerminatesOnMarkerOnlyInput(t *testing.T) {
	request := map[string]json.RawMessage{"model": json.RawMessage(`"m"`)}
	input := responsesTrimMarker
	body, err := json.Marshal(map[string]any{"model": "m", "input": input})
	require.NoError(t, err)
	// 标记横跨整个砍半切点且起点为 0：保留侧保整枚会让循环停滞，
	// 此时改为在标记末尾整枚丢弃，循环必须终止。
	require.NotPanics(t, func() {
		_, _ = trimResponsesInputString(request, input, 0, body, "test")
	})
}
