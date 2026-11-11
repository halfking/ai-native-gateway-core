package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// renderThroughReplaceAttr 把一条 slog 记录经 replaceAttr（内层 JSONHandler
// 的真实脱敏路径）渲染成落盘 JSON，返回解析后的顶层字段。
func renderThroughReplaceAttr(t *testing.T, fn func(l *slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: replaceAttr})
	l := slog.New(h)
	fn(l)
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("rendered log is not JSON: %v\nraw: %s", err, buf.String())
	}
	return out
}

// TestNestedSlogAnyRedacted 钉住 2026-11-11（48h 审计）收口的 rule 71 嵌套
// 脱敏缺口：slog.Any 携带的 map[string]any 此前**原样落盘**——嵌套对象里的
// api_key / token（键判定）与命中敏感模式的字符串（值判定）全部穿透进可
// 全文检索的日志面。门：① 嵌套敏感键被替换；② 嵌套敏感值被模式脱敏；
// ③ 非敏感嵌套内容**保持原样**（不制造噪声）；④ 数组逐元素；⑤ map[string]string。
func TestNestedSlogAnyRedacted(t *testing.T) {
	secret := "sk-prod-secret-abcdef1234567890"
	out := renderThroughReplaceAttr(t, func(l *slog.Logger) {
		l.Info("audit probe",
			"detail", map[string]any{
				"api_key": secret,               // ① 嵌套敏感键
				"note":    "bearer " + secret,   // ② 嵌套敏感值（bearer 模式）
				"model":   "gpt-4o",             // ③ 非敏感必须原样
				"deep":    map[string]any{"token": "tok-123"}, // ① 两层（键用枚举表内的 token）
				"items":   []any{"ok-value", "sk-" + strings.Repeat("x", 20)}, // ④
			},
			"labels", map[string]string{"authorization": "Bearer abc.def", "env": "prod"}, // ⑤
		)
	})

	detail, _ := out["detail"].(map[string]any)
	if detail == nil {
		t.Fatalf("detail missing from output: %v", out)
	}
	if detail["api_key"] != RedactedPlaceholder {
		t.Fatalf("nested sensitive key leaked: %v", detail["api_key"])
	}
	if note, _ := detail["note"].(string); strings.Contains(note, secret) || strings.Contains(note, "bearer ") {
		t.Fatalf("nested pattern-sensitive value leaked: %q", note)
	}
	if detail["model"] != "gpt-4o" {
		t.Fatalf("non-sensitive nested value must stay verbatim, got %v", detail["model"])
	}
	deep, _ := detail["deep"].(map[string]any)
	if deep == nil || deep["token"] != RedactedPlaceholder {
		t.Fatalf("two-level sensitive key leaked: %v", detail["deep"])
	}
	items, _ := detail["items"].([]any)
	if len(items) != 2 || items[0] != "ok-value" || strings.Contains(items[1].(string), "sk-") {
		t.Fatalf("slice elements not walked: %v", detail["items"])
	}
	labels, _ := out["labels"].(map[string]any)
	if labels == nil || labels["authorization"] != RedactedPlaceholder || labels["env"] != "prod" {
		t.Fatalf("map[string]string not walked: %v", out["labels"])
	}

	// 全文兜底：整行不得出现密钥本体。
	if strings.Contains(bufString(out), secret) {
		t.Fatal("raw secret appears anywhere in the rendered line")
	}
}

// TestNestedRedactSelfReferenceTerminates 钉住深度封顶：自引用 map 必须终止
//（不得栈溢出/死循环），到顶输出 depth 占位。
func TestNestedRedactSelfReferenceTerminates(t *testing.T) {
	self := map[string]any{}
	self["self"] = self
	out := renderThroughReplaceAttr(t, func(l *slog.Logger) {
		l.Info("cycle probe", "detail", self)
	})
	detail, _ := out["detail"].(map[string]any)
	if detail == nil {
		t.Fatalf("detail missing: %v", out)
	}
	// 走 maxRedactDepth 层后必然出现 depth 占位；若在此处挂死/爆栈即门红。
	_, hasDepth := walkUntilDepthMarker(detail, 0)
	if !hasDepth {
		t.Fatal("self-referential map must terminate with a depth placeholder")
	}
}

func walkUntilDepthMarker(v any, depth int) (any, bool) {
	if s, ok := v.(string); ok && strings.Contains(s, "[depth]") {
		return v, true
	}
	if depth > maxRedactDepth+2 {
		return nil, false
	}
	switch t := v.(type) {
	case map[string]any:
		for _, av := range t {
			if got, ok := walkUntilDepthMarker(av, depth+1); ok {
				return got, true
			}
		}
	case []any:
		for _, av := range t {
			if got, ok := walkUntilDepthMarker(av, depth+1); ok {
				return got, true
			}
		}
	}
	return nil, false
}

func bufString(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// TestTopLevelStringRedactionUnchanged 反向守卫：本轮新增的递归路径不得改变
// 既有顶层语义（顶层敏感键替换 + 长值截断）。
func TestTopLevelStringRedactionUnchanged(t *testing.T) {
	out := renderThroughReplaceAttr(t, func(l *slog.Logger) {
		l.Info("compat probe",
			"api_key", "sk-toplevel-secret-abcdef123456",
			// 非 hex 字符（z）避开「长十六进制」值模式——那是上轮已登记的
			// 过度脱敏疣，不是本门的目标。
			"blob", strings.Repeat("z", maxValueLength+100),
		)
	})
	if out["api_key"] != RedactedPlaceholder {
		t.Fatalf("top-level sensitive key behavior changed: %v", out["api_key"])
	}
	blob, _ := out["blob"].(string)
	if !strings.HasSuffix(blob, "…[truncated]") || len(strings.TrimSuffix(blob, "…[truncated]")) != maxValueLength {
		t.Fatalf("top-level truncation behavior changed: len=%d suffix=%q", len(blob), blob[max(0, len(blob)-20):])
	}
}
