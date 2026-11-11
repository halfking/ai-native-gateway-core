// parse_gemini_raw_redaction_test.go —— Gemini 未识别 part 脱敏的钉测（★ 2026-11-11 48h 审计 P1）
//
// ★ 缺陷：★ parseGeminiContents 对「不是对象 / 没有分支认领」的 part，把**整个原始
// ★ part**（map{"raw": ...}）当 anomaly 的 metadata 上报 ⇒ ★ 用户文本 / 工具调用
// ★ 参数 / inline media 的 base64 **逐字**进 ir.anomaly 事件 ⇒ ★ 随
// ★ slog.Warn("ir.anomaly") 进可全文检索的日志面、并随 format-anomaly 采集落库
// ★ ⇒ ★ 内容外泄面，且 rule 71 的模式脱敏对普通用户内容不命中 ⇒ ★ 兜不住。
//
// ★ 修法（与 redactReasoningItem 同族，parse_responses.go 2026-11-11 P2-I）：
// ★ 只上报描述性字段（bytes / 顶层键名），不报内容本身。
//
// ★ 变异臂：★ 把 `redactRawPart(rawPart)` 换回 `map[string]any{"raw": string(rawPart)}` ⇒ ★★ 用例红。

package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseGeminiUnknownPartMetadataRedacted 钉住 P1：
//
// ★ 断言的是**上报出去的 metadata**（★ 而不是助记函数本身）⇒ ★★ 守的是「外泄面真的被堵住」。
func TestParseGeminiUnknownPartMetadataRedacted(t *testing.T) {
	const secretText = "SECRET-USER-TEXT-MUST-NOT-REACH-LOGS"
	const secretB64 = "U0VDUkVULUJJTkFSWS1NURURJQS1CTE9C"

	var got []AnomalyEvent
	prev := SetAnomalyReporter(func(ev AnomalyEvent) { got = append(got, ev) })
	defer SetAnomalyReporter(prev)

	// 未被任何分支认领的 part：无 text / inlineData / fileData / functionCall /
	// functionResponse / thought ⇒ 走最终兜底（raw + anomaly 上报）。
	// 内容里塞用户文本与 base64 形态的 blob，模拟真实外泄载荷。
	contents := `[{"role":"user","parts":[
		{"unknownGeminiKey":"` + secretText + `","anotherKey":"` + secretB64 + `"}
	]}]`

	msgs, err := parseGeminiContents(json.RawMessage(contents))
	if err != nil {
		t.Fatalf("parseGeminiContents: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 anomaly event for the unclaimed part, got %d", len(got))
	}

	blob, err := json.Marshal(got[0].Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	blobStr := string(blob)

	// ★ 外泄面必须堵死
	if strings.Contains(blobStr, secretText) {
		t.Fatalf("user text leaked into anomaly metadata: %s", blobStr)
	}
	if strings.Contains(blobStr, secretB64) {
		t.Fatalf("inline media base64 leaked into anomaly metadata: %s", blobStr)
	}

	// ★ 诊断信息不能丢（★ 脱敏不是「什么都不报」）：bytes 与顶层键名必须在
	md := got[0].Metadata
	if b, ok := md["bytes"].(int); !ok || b <= 0 {
		t.Fatalf("metadata.bytes = %v, want positive int", md["bytes"])
	}
	keys, ok := md["keys"].([]string)
	if !ok || len(keys) == 0 {
		t.Fatalf("metadata.keys = %v, want non-empty []string", md["keys"])
	}
	// ★ 键名是 schema 名（unknownGeminiKey 等），不是用户内容 ⇒ 可以留
	joined := strings.Join(keys, ",")
	for _, want := range []string{"unknownGeminiKey", "anotherKey"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("metadata.keys missing %q: %v", want, keys)
		}
	}
	// ★ 原文这个键不该出现
	if _, present := md["raw"]; present {
		t.Fatalf("metadata must not carry key \"raw\" (★ 只保留描述性字段)")
	}
}
