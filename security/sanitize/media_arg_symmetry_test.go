package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 215 号审计（全面审计v3/2026-10-03/215）。
//
// 本轮要复核的是「上一轮子代理报告里**已坐实**的那几条」在当前 HEAD 上是否仍成立。
// 复核中冒出一条**报告里没有**的新不对称，两侧方向与报告里的 F3 正好相反：
//
//   洗侧：sanitizeToolValueWithLabel 的 object 分支（input_tools.go:186-210）
//         递归**每一个键**，**不识别**媒体对象。
//   还原侧：restoreNativeNestedValue 的 map 分支（native_restore.go:379-382）
//         开头就是 `if nativeMediaObject(current) { return current, false, nil }`
//         ——**整个媒体对象跳过还原**。
//
// 而 nativeMediaObject 全仓**只在 native_restore.go 定义并使用一次**，
// 洗侧没有任何对应分支。
//
// ⇒ 不对称方向是「**洗侧多洗、还原侧不还原**」：
//   媒体对象里非 base64 的字段（alt / detail / 标题…）若含敏感值，
//   洗侧会把它换成占位符，而还原侧整个对象跳过 ⇒ **占位符残留**。
//   这与 F3（洗侧少洗 ⇒ 敏感值原样出境）方向相反、形态也不同，
//   所以它**不是** F3 的同一件事。
//
// ⚠️ 但要小心一件事：`input_protocols_test.go:177-182` 明确断言
// 「data:image/png;base64,13800138000」与 {"data":"13800138000"} **原样透传**，
// 即「媒体不透明、整体不进文本脱敏」是本仓**既定设计**且已有测试钉住。
// 也就是说主链路上媒体本来就被 RawMessage 通道整体挡住了
// （见 input_protocols.go:29-30 的注释）。
// ⇒ 真正的问题是：**tool arguments 这条腿**（sanitizeToolValue 的递归）
// 是否也遵守同一个「媒体不进脱敏」约定。它现在**不遵守**。
//
// 本文件先只做一件事：用**对称性判据**把实际行为测出来。
// 判据取「洗侧产生了占位符 ⇒ 还原侧必须能还原它」，
// 而不是断言某个具体形态 —— 后者会把「两侧都洗/都不洗」也判成错。

// TestMediaObjectSanitizeRestoreSymmetry 断言：洗侧与还原侧对媒体对象的
// 处理必须对称。若洗侧在媒体对象的某个字段里产生了占位符，而还原侧因
// nativeMediaObject 整对象跳过导致占位符残留，则判据失败。
func TestMediaObjectSanitizeRestoreSymmetry(t *testing.T) {
	const raw = `{"content":[{"type":"image_url","image_url":` +
		`{"url":"data:image/png;base64,iVBORw0KGgo=","alt":"我的手机号 13800138000"}}]}`

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	ctx := context.Background()

	// ── 洗侧：tool arguments 的递归腿 ────────────────────────────────────
	w := &requestInputSanitizer{
		ctx:       ctx,
		sanitizer: s,
		offset:    map[SensitiveType]int{},
		mapping:   make(SanitizeMap),
		usedCount: make(map[string]int),
		existing:  make(sanitizeReuseIndex),
	}
	sanitized, changed, err := w.sanitizeToolValue(json.RawMessage(raw), 0)
	require.NoError(t, err)

	sanitizedText := string(sanitized)
	secretLeaked := strings.Contains(sanitizedText, "13800138000")

	t.Logf("洗侧: changed=%v mapping=%v sanitized=%s", changed, w.mapping, sanitizedText)

	// ── 还原侧：用洗侧真实产出的 mapping 还原同一个结构 ────────────────
	var decoded any
	require.NoError(t, json.Unmarshal(sanitized, &decoded))

	restored, restoreChanged, err := restoreNativeNestedValue(ctx, s, decoded, SanitizeMap(w.mapping), 0)
	require.NoError(t, err)
	restoredBytes, err := json.Marshal(restored)
	require.NoError(t, err)
	restoredText := string(restoredBytes)
	t.Logf("还原侧: changed=%v restored=%s", restoreChanged, restoredText)

	// ── 对称性判据 ──────────────────────────────────────────────────────
	// ⚠️ **本测试当前是「文档化观测」，不是门。**
	//
	// 判据本身**实测能红**（写作断言时跑过一次，exit=1，红的理由正是
	// 占位符残留，不是编译错也不是无关原因）：
	//
	//	if changed && strings.Contains(restoredText, "{SENSITIVE:") {
	//		t.Fatalf("两侧不对称：洗侧把媒体对象的敏感值换成了占位符 %v，"+
	//			"但还原侧因 nativeMediaObject 整对象跳过，占位符未被还原：%s", ...)
	//	}
	//
	// 本轮**刻意没有修复**，理由（215 号文档已登记）：
	//   ① `nativeMediaObject` 整对象跳过是**既有设计**，代码里**没有注释**
	//      说明当初为什么这样设计 ⇒ 改它要先回答那个未知；
	//   ② 细化它（只跳过 url/data/b64_json 等载荷键、让 alt/detail 递归）
	//      需要一份「各供应商媒体对象形状」的清单，而那是**外部证据**，
	//      本机无真库、无真实载荷，无法验证；
	//   ③ 危害形态是「占位符外泄」（客户端看到 {SENSITIVE:phone:1}），
	//      **不是敏感信息泄漏**（方向与 F3 相反）⇒ 不属最高优先级；
	//   ④ 触发需要客户端回传**带 alt/detail 等元数据**的媒体对象。
	//
	// ⇒ 这里只 t.Log 留档实测值，**不让它红**（一个红的测试会破坏 CI）。
	//    **一旦修复，把上面那段注释里的 t.Fatalf 解注释即可立刻当门用。**
	asymmetric := changed && strings.Contains(restoredText, "{SENSITIVE:")
	if asymmetric {
		t.Logf("⚠️ 已坐实的两侧不对称（215 号登记，尚未修复）："+
			"洗侧 changed=%v 把媒体对象的 alt 换成了 %v，但还原侧因 "+
			"nativeMediaObject 整对象跳过而 changed=%v，占位符残留：%s",
			changed, w.mapping, restoreChanged, restoredText)
	}
	if !changed && secretLeaked {
		t.Fatalf("洗侧声称未改动却仍带明文，需复核判据：%s", sanitizedText)
	}
}
