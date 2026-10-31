package v2

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// persistedBytes 独立测量「这一段落库后占多少字节」。
//
// ★★ r1041 轮（2026-10-28）：★ **判据必须独立于实现** ——
// ★ 实现用 messagesWireSize 记账，★ 若【判据也用同一个函数】⇒
// ★ ★ 量具本身错了，测试会跟着一起错 ⇒ ★★ 假绿。
// ⇒ ★ 这里直接用 json.Marshal 复算（★ 落库就是 safeJSONMarshal ⇒ 同一形态），
// ★ ★ 并把「编不出来」也当成失败（★ 编不出来 = 落库直接报错 = 更糟）。
func persistedBytes(t *testing.T, msgs []Message) int {
	t.Helper()
	if len(msgs) == 0 {
		return 0
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		t.Fatalf("★ 落库形态编不出来 ⇒ ⇒ 那一轮写不进去：%v", err)
	}
	return len(b)
}

// persistedTotal 独立测量「整轮正文落库后的总字节」。
func persistedTotal(t *testing.T, rec *BodiesRecord) int {
	t.Helper()
	return persistedBytes(t, rec.RequestDelta) +
		persistedBytes(t, rec.ResponseDelta) +
		persistedBytes(t, rec.OutboundBody)
}

func TestBoundBodiesRecord_TruncatesAndMarks(t *testing.T) {
	// Use printable prose — pure "x"*N is redacted as a credential shape
	// by errorsx.RedactCredentialShapes before the size bound runs.
	huge := strings.Repeat("word ", MaxTurnBodyBytes/5)
	rec := BodiesRecord{
		RequestDelta:  []Message{{Role: "user", Content: huge}},
		ResponseDelta: []Message{{Role: "assistant", Content: "ok"}},
	}
	if !boundBodiesRecord(&rec) {
		t.Fatal("expected truncation")
	}
	markTruncation(&rec)
	if len(rec.RequestDelta) == 0 {
		t.Fatal("request delta emptied unexpectedly")
	}
	if messageWireSize(rec.RequestDelta[0]) > MaxTurnBodyBytes+64 {
		// ★ r1041 轮（2026-10-28）：★ 上限口径从「每方向 512 KiB」
		// ★ （★ 实际总量 1.5 MiB）改成「**每轮总量 1 MiB**」⇒
		// ★ 本断言随之改为【总量 ≤ MaxTurnBodyBytes】——
		// ★ ★ 旧断言（≤ MaxTurnBodyBytes/2+64）锁住的是**旧实现**，
		// ★ ★ 而旧实现与本文件头注释的「1 MiB per turn」**矛盾** ⇒
		// ★ ★ 改的是【实现】（注释与常量都说 1 MiB），测试跟着契约走。
		t.Fatalf("truncated message still too large: %d", messageWireSize(rec.RequestDelta[0]))
	}
	found := false
	for _, m := range rec.ResponseDelta {
		if strings.Contains(m.Content, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing truncation marker")
	}
}

func TestScrubMessage_RedactsBearer(t *testing.T) {
	m := scrubMessage(Message{Role: "user", Content: "Authorization: Bearer sk-secret-token-value-here"})
	if strings.Contains(m.Content, "sk-secret") {
		t.Fatalf("secret not scrubbed: %q", m.Content)
	}
}

func TestRecordingConsentAllowsWrite_Hook(t *testing.T) {
	enableRecordingConsentForTest(t)
	if !RecordingConsentAllowsWrite("t1", 0) {
		t.Fatal("hook should allow")
	}
}

func TestRecordingConsentAllowsWrite_DefaultOff(t *testing.T) {
	prev := allowBodiesWriteHook
	allowBodiesWriteHook = nil
	t.Cleanup(func() { allowBodiesWriteHook = prev })
	if RecordingConsentAllowsWrite("no-such-tenant", 0) {
		t.Fatal("default must be off (fail closed)")
	}
}

// TestBoundBodiesRecord_TotalIsCappedAtOneMiB：★ 「1 MiB」必须是【总量】。
//
// ★ 起因（r1041 轮）：★ 旧实现给**三个方向各** MaxTurnBodyBytes/2
// ★ ⇒ 实际总量上限 = 1.5 MiB ⇒ 与本文件头注释「1 MiB per turn JSON
// ★ after scrub」和常量注释「hard ceiling for one turn's persisted
// ★ payload budget」**矛盾** ⇒ ★★ 「文档声称 1 MiB，实现是 1.5 MiB」。
//
// ★ 判据：**把三个方向都灌满** ⇒ 落库总量必须 ≤ MaxTurnBodyBytes。
// ★ ★ 这条断言在旧实现下会红（1.5 MiB > 1 MiB）⇒ ⇒ 它就是这次修正的守卫。
//
// ★★ r1041 轮第二处修正：★ 断言必须打在**生产路径**上 ——
// ★ 即「bound + markTruncation」（★ 截断标记是在预算**之外**追加的）
// ★ ⇒ ⇒ 只断言 bound 的结果会漏掉标记带来的溢出。
func TestBoundBodiesRecord_TotalIsCappedAtOneMiB(t *testing.T) {
	// ★ 用可打印散文（纯 "x"*N 会被 errorsx 当成凭据形状先被 redact）。
	// ★ ★ 每段 0.5 MiB ⇒ 三段共 1.5 MiB ⇒ 必然触发截断；
	// ★ ★ 且【前两段各自装得下】⇒ ⇒ 数组框架的记账偏差会【传递】到
	// ★ ★ 后面的方向 ⇒ ⇒ 「数组框架漏算」这类偏差也能被看见。
	fill := strings.Repeat("word ", MaxTurnBodyBytes/10)
	rec := BodiesRecord{
		RequestDelta:  []Message{{Role: "user", Content: fill}},
		ResponseDelta: []Message{{Role: "assistant", Content: fill}},
		OutboundBody:  []Message{{Role: "assistant", Content: fill}},
	}
	// ★ 生产路径（★ bodies_writer.go:285 就是这么写的）。
	if truncated := boundBodiesRecord(&rec); truncated {
		markTruncation(&rec)
	}

	total := persistedTotal(t, &rec)
	// ★ ★ 判据是**精确**的 1 MiB（★ 不留「+64 余量」这种含糊尾巴）——
	// ★ ★ 因为测量用的是**落库形态**（JSON 数组）的字节数
	// ★ ★ ⇒ 三段之和就是落库行的正文体积。
	if total > MaxTurnBodyBytes {
		t.Fatalf("★ 三方向合计 %d 字节 > 上限 %d 字节\n"+
			"  ⇒ 「1 MiB per turn」没有兑现（★ 旧实现是三个方向各 512 KiB ⇒ 1.5 MiB）",
			total, MaxTurnBodyBytes)
	}
	// ★★ 反向守卫：★ 上限必须是【贴着】的 —— ★ 若上限收得过紧
	// ★（比如把内容全丢掉），断言会**空洞地**通过 ⇒ ★ 那不叫「上限」。
	// ★⇒ 灌了 3× 1.25 MiB 的内容进去，剩下来的必须仍接近 1 MiB。
	if total < MaxTurnBodyBytes-4096 {
		t.Fatalf("★ 上限收得过紧：只留下 %d 字节（离上限差 %d）⇒ ⇒ 疑似把内容整段丢了",
			total, MaxTurnBodyBytes-total)
	}
}

// TestBoundBodiesRecord_EscapingDoesNotBreakCap：★ 转义膨胀不得撑破上限。
//
// ★ 起因（r1041 轮实测）：★ 旧实现按【原始字节】裁（`n := budget - 24`），
// ★ 而额度衡量的是【JSON 编码后】的字节 ⇒ ★ Go 的 JSON 编码把 `<` 转成
// ★ \u003c（1 字节 → 6 字节）⇒ ★ 裁完仍可超额度 6 倍
// ★ ⇒ ⇒ 实测：额度 1 MiB 时落库 1,572,892 字节（**超 50%**）。
// ⇒ ⇒ 这条断言在旧实现下会红 ⇒ ⇒ 它就是「按编码后大小裁」的守卫。
func TestBoundBodiesRecord_EscapingDoesNotBreakCap(t *testing.T) {
	// ★ 全是 `<` ⇒ 每个字符编码后 6 字节（\u003c）⇒ 最容易撑破上限。
	fill := strings.Repeat("<", MaxTurnBodyBytes/4)
	rec := BodiesRecord{
		RequestDelta: []Message{{Role: "user", Content: fill}},
	}
	if truncated := boundBodiesRecord(&rec); truncated {
		markTruncation(&rec)
	}
	total := persistedTotal(t, &rec)
	if total > MaxTurnBodyBytes {
		t.Fatalf("★ 转义膨胀撑破了上限：%d > %d（★ 旧实现实测 1,572,892）",
			total, MaxTurnBodyBytes)
	}
	// ★★ 反向守卫：★ 上限必须是【贴着】的 ⇒ 内容被整段丢掉也会红。
	if total < MaxTurnBodyBytes-4096 {
		t.Fatalf("★ 上限收得过紧：只留下 %d 字节（离上限差 %d）⇒ ⇒ 疑似把内容整段丢了",
			total, MaxTurnBodyBytes-total)
	}
}

// TestBoundBodiesRecord_TruncationKeepsValidUTF8：★ 截断不得落非法 UTF-8。
//
// ★ 起因（r1041 轮）：★ Content 分支的截断做了 UTF-8 边界回退，
// ★ 而 ContentRaw 分支**没有** ⇒ ⇒ 截断可能落在多字节字符中间
// ⇒ ⇒ 落库的是非法 UTF-8 ⇒ ⇒ 读回来时 jsonb 会报错。
func TestBoundBodiesRecord_TruncationKeepsValidUTF8(t *testing.T) {
	// ★ 多字节字符（中文 3 字节）反复 ⇒ 任何奇数边界都会切在字符中间。
	fill := strings.Repeat("中", MaxTurnBodyBytes/8)
	rec := BodiesRecord{
		RequestDelta: []Message{{Role: "user", ContentRaw: []byte(fill)}},
	}
	boundBodiesRecord(&rec)
	if len(rec.RequestDelta) == 0 {
		t.Fatal("request delta emptied unexpectedly")
	}
	if !utf8.Valid(rec.RequestDelta[0].ContentRaw) {
		t.Fatalf("★ 截断后的 ContentRaw 不是合法 UTF-8 ⇒ ⇒ 落库即坏数据")
	}
}

// TestBoundBodiesRecord_TruncatedContentRawStaysValidJSON：★ 截断后必须仍是合法 JSON。
//
// ★ 起因（r1041 轮）：★ 旧实现对 ContentRaw 是**任意字节截断**
// ★ ⇒ 截在结构中间 ⇒ 产出**非法 JSON** ⇒ 落库时 safeJSONMarshal 报错
// ★ ⇒ ★★ 那一轮**根本写不进去**（不是写小了，是写失败）。
// ⇒ ⇒ 判据：★ 裁完的每一段都必须能被 json.Marshal 成功。
func TestBoundBodiesRecord_TruncatedContentRawStaysValidJSON(t *testing.T) {
	// ★ provider 原生内容数组（★ 结构体形态 ⇒ 走 ContentRaw 分支）。
	blocks := make([]map[string]any, 0, 64)
	for i := 0; i < 64; i++ {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": strings.Repeat("中", MaxTurnBodyBytes/64),
		})
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("build raw: %v", err)
	}
	rec := BodiesRecord{
		RequestDelta: []Message{{Role: "user", ContentRaw: raw}},
	}
	boundBodiesRecord(&rec)
	if len(rec.RequestDelta) == 0 {
		t.Fatal("request delta emptied unexpectedly")
	}
	for i, m := range rec.RequestDelta {
		if len(m.ContentRaw) == 0 {
			continue
		}
		if !json.Valid(m.ContentRaw) {
			t.Fatalf("★ 第 %d 条消息的 ContentRaw 裁成了非法 JSON ⇒ ⇒ 落库即失败", i)
		}
		// ★ ★ 整条消息（含 content 字段）也必须能编码 ⇒
		// ★ ★ 否则 safeJSONMarshal 在落库那一刻才炸 ⇒ ★ 写不进去。
		if _, err := json.Marshal(m); err != nil {
			t.Fatalf("★ 第 %d 条消息整条编码失败：%v", i, err)
		}
	}
}
