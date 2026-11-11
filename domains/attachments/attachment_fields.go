package attachments

// attachment_fields.go —— 附件在 **wire 上**长什么样（2026-11-11 附件域审计轮）
//
// ── 为什么要有这个文件 ────────────────────────────────────────────────────
// ★ 附件载荷在出站 body 里落在**哪些字段**，此前是**两处各写一份**：
//   · OutboundURLRewriter 只认 `image_url.url`
//   · URLFetchFallback 也只认 `image_url.url`
// ⇒ ⇒ 两处**都**漏掉了文档（file.file_data）与视频（video_url.url）
// ⇒ ⇒ 而且两处的遍历方式还不同（一个按坐标、一个按前缀全 body 扫描）
// ⇒ ⇒ 同一族缺陷要在**两个地方**各修一次 ⇒ ⇒ 修完还会漂。
//
// ⇒ 本文件把「附件载荷在 wire 上的字段位置」收敛成**一处**，
//   两边都走同一个遍历器（★ 单一职责 + 开闭原则：★ 以后新增附件形态 ⇒
//   ★ 只改这一张表 ⇒ ★ 两边同时看见 ★）。
//
// ── 覆盖的 wire 形态 ──────────────────────────────────────────────────────
//   · OpenAI Chat Completions：messages[].content[] 里的块
//       image_url → image_url.url
//       file      → file.file_data      ← 文档/PDF（★ 此前漏 ★）
//       video_url → video_url.url       ← 视频（★ 此前漏 ★）
//   · Responses 形态（input[]）**本轮未纳入** ⇒ ★ 理由见下方「已知缺口」★。
//
// ── ★ 已知缺口（如实登记，不假装覆盖）★ ──────────────────────────────────
// ★ Responses（`input[]`）形态**没有**纳入本表 ⇒ 理由：
//   ① 生产调用点上 native Responses 候选走的是 `params.ResponsesBodyBytes`
//      （executor_dispatch.go:1015-1018）⇒ 而 rewriter/fallback 都只认
//      `messages[]` ⇒ ⇒ 对 Responses 形态**本来就是 no-op**；
//   ② native Responses 能力表**当前是空表、默认不启用**
//      （executor_chat.go 关于 SupportsNativeResponses 的注释：「该能力表是
//      opt-in 且当前默认为空 ⇒ 默认所有 openai-responses provider 都不可用」）
//      ⇒ ⇒ 这条路径**当前不可达** ⇒ ⇒ 本轮改它属于「改不可达路径」；
//   ③ Responses 的坐标语义与 chat 不同（extract 记的是 itemIdx/blockIdx）
//      ⇒ ⇒ 混进同一张表会引入**未验证**的语义。
// ⇒ ⇒ 处置：**登记为已知缺口**，等 native Responses 能力表启用时单开一轮。

import (
	"bytes"
	"encoding/json"
)

// attachmentFieldPath 描述块内承载附件载荷的字段路径（逐层下钻到字符串）。
//
// ★ 例：{"image_url","url"} ⇒ 块["image_url"]["url"]；
//   {"file","file_data"}   ⇒ 块["file"]["file_data"]。
type attachmentFieldPath []string

// chatAttachmentFields 是 **OpenAI Chat Completions** 形态下，
// 「块类型 → 该块承载附件载荷的字段路径」的**唯一事实源**。
//
// ★★ 为什么是 map 而不是 if 链：★★
// ★ 新增一种附件形态（例如 Responses 的 input_file）⇒ ⇒ **只加一行** ⇒
// ★ 两个消费者（改写 / 内联）**同时**看见 ⇒ ⇒ 不会漏（★ 这正是本轮
// ★ 两个消费者各漏了 file 与 video 的成因 ★）。
var chatAttachmentFields = map[string][]attachmentFieldPath{
	"image_url": {{"image_url", "url"}},
	"file":      {{"file", "file_data"}},
	"video_url": {{"video_url", "url"}},
}

// chatBlockAttachmentType 把 chat wire 的**块类型**映射到 Extractor 记录的
// **附件类型**（image / file / video）。
//
// ★ 用途：★ 坐标对上但**类型**对不上 ⇒ 说明 body 在落盘之后被改过 ⇒ ⇒
//   跳过（不盲改）★ ⇒ 与既有「坐标漂移 ⇒ 跳过」同款，但多一道类型闸。
// ★ 空类型（老记录）⇒ 不做类型闸（★ 保持既有契约：只钉坐标 ★）。
//
// ★ 与 chatAttachmentFields **同源**（★ 同一张表描述「wire 上附件长什么样」★）★★
var chatBlockAttachmentType = map[string]string{
	"image_url": "image",
	"file":      "file",
	"video_url": "video",
}

// attachmentSlotRef 是一次「附件字段」的位置引用。
//
// ★ Container/Block 是**坐标**（messages 下标 / content 下标）⇒ ⇒ 与
//   Extractor 记录的 AttachmentMetadata.MessageIndex/BlockIndex **同口径**
//   ⇒ ⇒ 改写器可以按坐标核对「这一块确实是那次落盘的附件」⇒ ⇒ 不盲改。
type attachmentSlotRef struct {
	Container int
	Block     int
	Path      attachmentFieldPath
}

// attachmentSlotRef 是一次「附件字段」的**当前值 + 位置**。
type attachmentSlot struct {
	attachmentSlotRef
	Value string
}

// attachmentFieldValue 按路径从块里取字符串值（任一层缺失 ⇒ ok=false）。
func attachmentFieldValue(block map[string]any, path attachmentFieldPath) (string, bool) {
	var cur any = block
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[key]
		if !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

// setAttachmentFieldValue 按路径在块里写字符串值（返回是否写成功）。
func setAttachmentFieldValue(block map[string]any, path attachmentFieldPath, value string) bool {
	if len(path) == 0 {
		return false
	}
	cur := block
	for _, key := range path[:len(path)-1] {
		next, ok := cur[key].(map[string]any)
		if !ok {
			return false
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
	return true
}

// rewriteChatAttachmentFields 遍历 chat body 里**每一个承载附件的字段**，
// 对每个字段调用 fn；fn 返回非空且与原值不同的字符串 ⇒ 该字段被替换。
//
// ★ 这是本文件对外的**唯一主入口** ⇒ ★ 改写器与回退器共用 ⇒ ★ 字段表改一处、
//   两边同时生效 ⇒ ⇒ 不会再出现「一边修了另一边没修」。
//
// ★ 返回：(新 body, 被替换的字段数, 错误)
//   · **无替换时原样返回入参**（★ 逐字节不变 ★）⇒ 保持「什么都没发生就
//     一个字节都不动」的既有契约。
//   · 有替换时整体重新序列化 ⇒ ★★ 诚实边界：★★ 键序会被规范化（Go 的
//     map 序列化按键排序）、空白被去掉 ⇒ ⇒ **不是**逐字节保持 ⇒ ⇒
//     但**字段语义**精确：★ 只有被 fn 明确改写的字段变了 ⇒ ★ 其余字段
//     **原值不变**（★ 数字用 json.Number 承载 ⇒ 不会被 float64 改写 ★）。
func rewriteChatAttachmentFields(
	body []byte,
	fn func(ref attachmentSlotRef, value string) string,
) ([]byte, int, error) {
	if len(body) == 0 || fn == nil {
		return body, 0, nil
	}

	// ★ UseNumber：★ 数字用 json.Number 承载 ⇒ 重新序列化时**原样输出**
	//   （★ 否则 1e3 / 大 int64 会被 float64 改写 ⇒ ★ 那是另一种「误伤」★）。
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return body, 0, nil
	}

	msgs, ok := root["messages"].([]any)
	if !ok {
		return body, 0, nil
	}

	changed := 0
	for mi := range msgs {
		msg, ok := msgs[mi].(map[string]any)
		if !ok {
			continue
		}
		arr, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for bi := range arr {
			block, ok := arr[bi].(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			paths, ok := chatAttachmentFields[blockType]
			if !ok {
				continue
			}
			for _, path := range paths {
				value, ok := attachmentFieldValue(block, path)
				if !ok || value == "" {
					continue
				}
				ref := attachmentSlotRef{Container: mi, Block: bi, Path: path}
				newValue := fn(ref, value)
				if newValue == "" || newValue == value {
					continue
				}
				if !setAttachmentFieldValue(block, path, newValue) {
					continue
				}
				changed++
			}
		}
	}

	if changed == 0 {
		return body, 0, nil
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// ★ 不转义 HTML 字符（< > &）⇒ ⇒ 文本内容里的这些字符**原样保留**
	//   （★ 默认的 SetEscapeHTML(true) 会把它们变成 \u003c 之类 ⇒ ★
	//   ★ 语义不变，但会让「未改动的字段」在字节层面也变了 ★）。
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return body, 0, nil
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	return out, changed, nil
}
