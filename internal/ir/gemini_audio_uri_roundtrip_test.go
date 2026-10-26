package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

// ★★ 2026-10-27（12h 审计轮）★★
//
// 缺陷：★ Gemini 客户端发来的 **fileData 音频**（file_uri 形态）在
// ★★ **执行器强制的 Parse→Validate→Serialize 往返**里**整个消失**。
//
// ★★ 实测链（复现于本门之前）：
//
//	IR{type:"audio", FileURI:"gs://bucket/a.mp3"}
//	  → OpenAI 线 {"type":"input_audio","input_audio":{"url":"gs://bucket/a.mp3"}}
//	  → 往返再解析 ⇒ parseOpenAIInputAudioBlock 只读 data/format ⇒ 返回 nil
//	    ⇒ 落进 Type="raw" ⇒ RawContent=原文
//	  → Gemini 线 ⇒ serialize_gemini.go 的 switch **没有 raw 分支也没有 default**
//	    ⇒ parts 里只剩文本 ⇒ **音频整个消失**
//
// ⇒ ★★ 上游收到的是**没有音频**的请求，而客户端**收不到任何报错**
// ⇒ ★★ 这就是「静默丢载荷」：最难查的一类。
//
// ★★ 根因不是「少写了一个 case」，而是**承载方式选错了**：
// ★★ 只有 Anthropic 序列化器有 default 分支会还原 RawContent；
// ★★ OpenAI/Gemini/Responses/Ollama 四家都没有 ⇒ raw 兜底**根本不兜底**。
// ⇒ ★★ 正确修法是让 URI 引用形态**走有类型承载的那条路**（MediaSource），
// ★★ 而不是指望 raw 兜底。
//
// ★★ 本门锁住两件事：
// ① URI **不丢**（Gemini 线上仍有音频 part）；
// ② 形状**正确**（Gemini 线上是 fileData，不是 OpenAI 的 input_audio）。
func TestGeminiAudioURIReferenceSurvivesOpenAIWireRoundTrip(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[
		{"text":"what is in this audio?"},
		{"fileData":{"mimeType":"audio/mp3","fileUri":"gs://bucket/a.mp3"}}]}]}`)

	irReq, err := ParseGemini(body)
	if err != nil {
		t.Fatalf("ParseGemini: %v", err)
	}

	// 往返：Gemini 客户端 → IR → OpenAI 线（网关自己的转换）
	openaiWire, err := SerializeOpenAI(irReq)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	if !strings.Contains(string(openaiWire), "gs://bucket/a.mp3") {
		t.Fatalf("第一跳就丢了 URI：\n%s", openaiWire)
	}

	// 执行器强制的再解析（往返）
	ir2, err := ParseOpenAI(openaiWire)
	if err != nil {
		t.Fatalf("ParseOpenAI: %v", err)
	}

	// ★ 判据 1：往返后**必须**还有音频载荷 ⇒ 且**不能**是 raw
	// （raw 在 Gemini/Responses/Ollama 三家线上会被静默丢弃 ⇒ 不算修好）
	var audio *MediaSource
	for _, b := range ir2.Messages[0].Content {
		if b.Type == "audio" && b.Audio != nil {
			audio = b.Audio
		}
	}
	if audio == nil {
		t.Fatalf("往返后音频块不在有类型的载体上（落成了 raw ⇒ 在 Gemini 线上会被静默丢弃）\n"+
			"往返后的块：%+v", ir2.Messages[0].Content)
	}
	if audio.URL == "" && audio.FileURI == "" {
		t.Errorf("往返后的音频块没有 URI 载体：%+v", audio)
	}

	// 再回到 Gemini 线 ⇒ 必须仍有 fileData part，且**形状是厂商原生形状**
	gemWire, err := SerializeGemini(ir2)
	if err != nil {
		t.Fatalf("SerializeGemini: %v", err)
	}
	if !strings.Contains(string(gemWire), "gs://bucket/a.mp3") {
		t.Errorf("★ Gemini 线上音频 URI 丢失（静默丢载荷）：\n%s", gemWire)
	}
	if !strings.Contains(string(gemWire), "fileData") {
		t.Errorf("★ Gemini 线上的音频形状不是厂商原生形状 fileData：\n%s", gemWire)
	}
	if strings.Contains(string(gemWire), "input_audio") {
		t.Errorf("★ Gemini 线上混进了 OpenAI 形状的块（input_audio）⇒ 形状非法：\n%s", gemWire)
	}
	// ★ mimeType 必须**带着子类型**过来：★ Gemini 的 fileData.mimeType 是
	// ★ 必填且要合法 ⇒ ★ 丢成 "audio/"（畸形 MIME）⇒ ★ 上游按错类型解码。
	// ★ ★ 根因：★ Gemini→OpenAI 第一跳只把 `audio.Format` 写进 OpenAI 线的
	// ★ `format` 字段 ⇒ ★ Format 不带值 ⇒ ★ mimeType 的**子类型**当场蒸发。
	if strings.Contains(string(gemWire), `"mimeType":"audio/"`) {
		t.Errorf("★ Gemini 线上的 mimeType 是畸形 MIME（audio/，无子类型）：\n%s", gemWire)
	}
	if !strings.Contains(string(gemWire), "audio/mp3") {
		t.Errorf("★ Gemini 线上丢了 mimeType 的子类型（audio/mp3）：\n%s", gemWire)
	}
}

// TestSerializeOpenAIDropsEmptyPayloadInputAudio locks the sibling guard.
//
// ★★ 同族缺陷：★ `case "input_audio"` 此前**没有**「载荷为空就不发」的守卫
// ★★ （而紧邻的 `case "audio"` 有）⇒ ★★ 发出
// ★★ `{"input_audio":{"data":"","format":"mp3"}}` ⇒ ★★ **上游必 400**
// ★★ （OpenAI 的 input_audio 要 base64 数据 + wav|mp3 格式）⇒ ★★ 且**静默**。
func TestSerializeOpenAIDropsEmptyPayloadInputAudio(t *testing.T) {
	req := &InternalRequest{
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: "hi"},
				{Type: "input_audio", InputAudio: &InputAudioBlock{Data: "", Format: "mp3"}},
			},
		}},
	}
	out, err := SerializeOpenAI(req)
	if err != nil {
		t.Fatalf("SerializeOpenAI: %v", err)
	}
	var pretty map[string]any
	if err := json.Unmarshal(out, &pretty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := pretty["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages 形状异常：%s", out)
	}
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	for _, c := range content {
		blk, _ := c.(map[string]any)
		if blk["type"] == "input_audio" {
			t.Errorf("★ 空载荷的 input_audio 仍被发出（上游必 400）：%s", out)
		}
	}
}
