package ir

// ★★ 2026-10-27（12h 审计轮）★★
//
// 缺陷族：**上一轮（04:15）修的 `input_audio` 只落在 OpenAI 线上，
// Responses 线留着同款判据错误** ⇒ Responses 客户端的音频在往返里静默消失。
//
// ★★ 三条同源判据（都已在 parse_openai.go 上修过，parse_responses.go 没跟上）：
//   ① 判据必须是「**有没有载荷**」（Data 是否为空），不是「parse 返回 nil 与否」
//      ⇒ 只带 format 不带 data 时 parse 返回非 nil ⇒ 旧判据走错分支。
//   ② URI 引用形态必须走**有类型的载体**（MediaSource）⇒ 各线按各自正确形状还原。
//   ③ **载荷为空的块不可走 raw 兜底**：raw 会被序列化器的 `default:` 分支
//      **原样回吐** ⇒ 又变回 {"data":"","format":"mp3"} ⇒ 上游必 400；
//      ⇒ 而且上一轮补在 `case "input_audio"` 上的守卫**根本走不到**（形同虚设）。
//
// ★★★ 本门与上一轮那条门的**关键差别**（也是上一轮那条门「伪绿」的根因）：
// ★★★ 上一轮的门**直接构造 IR**（`ContentBlock{Type:"input_audio", …}`），
// ★★★ **绕过了解析器** ⇒ 真实的解析→序列化链路上仍然是坏的，门却是绿的。
// ★★★ ⇒ 本门**一律从线格式字节出发**（Parse → Serialize），
// ★★★ 覆盖「解析器判据」与「序列化器守卫」两端。
//
// ★★★ 判据可证伪：每条用例都配**必须判绿**的对照臂（见各用例的对照段）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// responsesWireInputAudio 构造一条 Responses 线请求（客户端线 = OpenAI Responses）。
func responsesWireInputAudio(inner string) []byte {
	return []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[` +
		inner + `]}]}`)
}

// wireHasEmptyDataInputAudio 判读数：线上是否出现了**载荷为空**的 input_audio。
//
// ★ 不用字符串匹配（`"data":""` 也可能出现在别的上下文里）⇒ 解析 JSON 后按结构判。
func wireHasEmptyDataInputAudio(t *testing.T, wire []byte) bool {
	t.Helper()
	var doc any
	if err := json.Unmarshal(wire, &doc); err != nil {
		t.Fatalf("wire is not valid JSON: %v\n%s", err, wire)
	}
	return findEmptyDataInputAudio(doc)
}

func findEmptyDataInputAudio(node any) bool {
	switch v := node.(type) {
	case map[string]any:
		if ia, ok := v["input_audio"].(map[string]any); ok {
			// ★ 「载荷为空」= **既没有非空 data，也没有非空 url**。
			// ★ 只看 data 会**误判**：OpenAI 线的 URI 形态是
			// ★ `input_audio{url}` —— 压根没有 data 键，而 **url 就是载荷**。
			data, _ := ia["data"].(string)
			url, _ := ia["url"].(string)
			if data == "" && url == "" {
				return true
			}
		}
		for _, child := range v {
			if findEmptyDataInputAudio(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if findEmptyDataInputAudio(child) {
				return true
			}
		}
	}
	return false
}

// wireContains 判读数：线上（按字符串）是否包含某片段。
func wireContains(wire []byte, needle string) bool {
	return strings.Contains(string(wire), needle)
}

// TestOpenAIWireEmptyPayloadInputAudioIsDropped 锁住 OpenAI 线上那条
// 「守卫形同虚设」的缺陷（上一轮修的那条线）。
//
// ★ 缺陷形状：`{"type":"input_audio","input_audio":{"data":"","format":"mp3"}}`
//
//	走 OpenAI 线 ⇒ 旧实现落进 `raw` 兜底（判据是「parse 返回 nil 与否」，
//	而只带 format 时返回**非 nil** ⇒ 但 Data 为空 ⇒ 判据是 `ia.Data == ""`
//	⇒ 进 raw 分支）⇒ 序列化器 `default:` 分支**原样回吐** ⇒
//	⇒ 又变回那个「上游必 400」的形状 ⇒
//	⇒ 而上一轮补在 `case "input_audio"` 上的守卫**根本走不到**
//	（因为块已经是 raw 了）。
//
// ★★ 本门与上一轮那条门的关键差别：上一轮那条门**直接构造 IR** ⇒ 绕过解析器
// ★★ ⇒ 真实链路上仍然是坏的而门是绿的（**伪绿**）。本门**从线格式出发**。
func TestOpenAIWireEmptyPayloadInputAudioIsDropped(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"input_audio","input_audio":{"data":"","format":"mp3"}}` +
		`]}]}`)

	irReq, err := ParseOpenAI(body)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}
	openaiWire, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if wireHasEmptyDataInputAudio(t, openaiWire) {
		t.Fatalf("OpenAI 线发出了载荷为空的 input_audio（上游必 400）：\n%s", openaiWire)
	}

	// ★★ 对照臂：带真载荷的 input_audio 必须照发。
	real := []byte(`{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"input_audio","input_audio":{"data":"QUJD","format":"mp3"}}` +
		`]}]}`)
	irReal, err := ParseOpenAI(real)
	if err != nil {
		t.Fatalf("ParseOpenAI(real): %v", err)
	}
	openaiReal, err := SerializeOpenAI(irReal)
	if err != nil {
		t.Fatalf("SerializeOpenAI(real): %v", err)
	}
	if !wireContains(openaiReal, "QUJD") {
		t.Fatalf("带真载荷的 input_audio 被丢掉了（门把合法载荷也判红了）：\n%s", openaiReal)
	}
}

// URI 音频形态在往返里不丢」——**从线格式出发**（不构造 IR）。
//
// ★ 缺陷形状（实测）：`{"type":"input_audio","input_audio":{"url":"…","format":"mp3"}}`
//
//	走 Responses 线 ⇒ 旧判据（parse 返回非 nil）⇒ 块留在 Type="input_audio"
//	而载荷为空 ⇒ 两条线各自的「载荷为空就不发」守卫把它丢掉 ⇒
//	**音频静默消失**；再叠加「Gemini 的 switch 没有 raw 分支」⇒
//	⇒ 若它是唯一的块，**整个请求体变成空的**（`{}`）⇒ 上游收到一个没有内容的请求。
func TestResponsesWireInputAudioURISurvivesToGeminiWire(t *testing.T) {
	body := responsesWireInputAudio(`{"type":"input_audio","input_audio":{"url":"https://x/a.mp3","format":"mp3"}}`)

	irReq, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	geminiWire, err := SerializeGemini(irReq)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}

	// ① 载荷不许丢：Gemini 线上必须还看得到那个 URL。
	if !wireContains(geminiWire, "https://x/a.mp3") {
		t.Fatalf("Responses→Gemini 丢了音频 URI：\n%s", geminiWire)
	}
	// ② 形状要正确：Gemini 线是 fileData{fileUri}，**不是** OpenAI 的 input_audio。
	if wireContains(geminiWire, "input_audio") {
		t.Fatalf("Gemini 线上出现了 OpenAI 形状的 input_audio（形状非法）：\n%s", geminiWire)
	}
	if !wireContains(geminiWire, "fileData") {
		t.Fatalf("Gemini 线上没有 fileData（形状不对）：\n%s", geminiWire)
	}
	// ③ ★ 整个请求体不许变空（缺陷最严重的形态：唯一块消失 ⇒ 请求体空）。
	if strings.TrimSpace(string(geminiWire)) == "{}" || !wireContains(geminiWire, "contents") {
		t.Fatalf("Responses→Gemini 把整个请求体变成空的：\n%s", geminiWire)
	}
}

// TestResponsesWireInputAudioURISurvivesToOpenAIWire 锁住同一条缺陷在
// OpenAI 线上的形态：URL 形态**不许**变成空载荷的 input_audio。
//
// ★ 这是上一轮**已经修好**的那条线 —— 但只修了 `parse_openai.go`，
// ★ Responses 线没跟上 ⇒ 同一个缺陷在另一条入口上原样存在。
func TestResponsesWireInputAudioURISurvivesToOpenAIWire(t *testing.T) {
	body := responsesWireInputAudio(`{"type":"input_audio","input_audio":{"url":"https://x/a.mp3","format":"mp3"}}`)

	irReq, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}

	openaiWire, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}

	if !wireContains(openaiWire, "https://x/a.mp3") {
		t.Fatalf("Responses→OpenAI 丢了音频 URI：\n%s", openaiWire)
	}
	// ★ 载荷为空 ⇒ 就是「上游必 400」的那条路。
	if wireHasEmptyDataInputAudio(t, openaiWire) {
		t.Fatalf("Responses→OpenAI 发出了载荷为空的 input_audio（上游必 400）：\n%s", openaiWire)
	}
}

// TestEmptyPayloadInputAudioIsDroppedOnEveryWire 锁住「载荷为空的
// input_audio 在**每一条线**上都不许被发出去」。
//
// ★ 缺陷形状：`{"type":"input_audio","input_audio":{"data":"","format":"mp3"}}`
//
//	走 OpenAI 线 ⇒ 旧实现落进 `raw` 兜底 ⇒ 序列化器 `default:` 分支
//	**原样回吐** ⇒ 又变回那个必 400 的形状 ⇒
//	⇒ 上一轮补的守卫（`case "input_audio"` 的 `Data != ""`）**根本走不到**。
//	★ 而 Responses 线上**连守卫都没有**。
//
// ★ 对照臂（必须判绿）：带**真载荷**的 input_audio 必须照发 ——
//
//	证明这条门判的是「载荷为空」而不是「input_audio 本身」。
func TestEmptyPayloadInputAudioIsDroppedOnEveryWire(t *testing.T) {
	empty := responsesWireInputAudio(`{"type":"input_audio","input_audio":{"data":"","format":"mp3"}}`)
	irReq, err := ParseResponses(empty)
	if err != nil {
		t.Fatalf("ParseResponses(empty): %v", err)
	}

	openaiWire, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if wireHasEmptyDataInputAudio(t, openaiWire) {
		t.Fatalf("OpenAI 线发出了载荷为空的 input_audio（上游必 400）：\n%s", openaiWire)
	}

	responsesWire, err := SerializeResponsesRequest(irReq)
	if err != nil {
		t.Fatalf("SerializeResponsesRequest: %v", err)
	}
	if wireHasEmptyDataInputAudio(t, responsesWire) {
		t.Fatalf("Responses 线发出了载荷为空的 input_audio（上游必 400）：\n%s", responsesWire)
	}

	geminiWire, err := SerializeGemini(irReq)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}
	if wireHasEmptyDataInputAudio(t, geminiWire) {
		t.Fatalf("Gemini 线发出了载荷为空的 input_audio：\n%s", geminiWire)
	}

	// ★★ 对照臂：**带真载荷**的 input_audio 必须照发（判据不是「input_audio 本身」）。
	real := responsesWireInputAudio(`{"type":"input_audio","input_audio":{"data":"QUJD","format":"mp3"}}`)
	irReal, err := ParseResponses(real)
	if err != nil {
		t.Fatalf("ParseResponses(real): %v", err)
	}
	openaiReal, err := SerializeOpenAI(irReal)
	if err != nil {
		t.Fatalf("SerializeOpenAI(real): %v", err)
	}
	if !wireContains(openaiReal, "QUJD") {
		t.Fatalf("带真载荷的 input_audio 被丢掉了（门把合法载荷也判红了）：\n%s", openaiReal)
	}
}
