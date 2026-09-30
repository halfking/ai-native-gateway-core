// Package compression — responses 专属压缩链的 AlignmentMap provenance
// （R34-A1，承接 R34 批判复审 A-G3 定论）。
//
// messages 车道的压缩结果本就带 AlignmentMap（PrepareResult.AlignmentMap，
// 进 result memo）；native Responses 车道不接 session compressor 是显式
// 设计（防污染 session-cache），其专属压缩链
//（transformation.CompressResponsesInput*，candidate-window 前置 +
// 4xx recovery 聚合中转）此前只输出裁剪后的 body，无任何"哪些 input
// item 去了哪里"的审计证据。本文件给 responses input 形态补齐同一套
// original→compressed 位置映射，供 executor 写入
// request_logs.compression_meta。
package compression

import (
	"encoding/json"
	"errors"
)

// errResponsesInputMissing 表示 body 不是 native Responses 请求形态
// （无 input 字段），对齐构建无对象。
var errResponsesInputMissing = errors.New("compression: responses input field missing")

// extractResponsesInputMessages 把 native Responses 请求的 input 形态
// 归一成 rawMsg 数组，供 buildAlignmentMapWithExtractor 复用 messages
// 车道的 hash-match 算法：
//   - input 为数组：逐 item 原样返回（item 即对齐坐标）；
//   - input 为字符串：包装成单条 {"role":"user","content":...}（整串
//     是一个对齐单元——专属链对 string 形态做 rune 级前缀截断，item
//     粒度对齐无意义，靠 meta 的 rune 计数留证）。
//
// protocol 车道只有 openai-responses；TopLevelSystem 摘要特判不存在
// （专属链无 LLM 摘要），故传 "openai-responses" 走默认分支。
func extractResponsesInputMessages(body []byte) ([]rawMsg, error) {
	var probe struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	if len(probe.Input) == 0 || string(probe.Input) == "null" {
		return nil, errResponsesInputMissing
	}
	var items []rawMsg
	if err := json.Unmarshal(probe.Input, &items); err == nil && items != nil {
		return items, nil
	}
	var inputString string
	if err := json.Unmarshal(probe.Input, &inputString); err == nil {
		return []rawMsg{mustRawMessage(map[string]any{
			"role":    "user",
			"content": inputString,
		})}, nil
	}
	return nil, errors.New("compression: responses input neither array nor string")
}

func mustRawMessage(v any) rawMsg {
	b, err := json.Marshal(v)
	if err != nil {
		return rawMsg("null")
	}
	return rawMsg(b)
}

// TargetSpaceResponsesInput 标识保留目标的坐标空间是 Responses 请求的
// 顶层 input 数组（而非 chat messages 数组）。
const TargetSpaceResponsesInput = "responses_input"

// BuildResponsesAlignmentMap 计算 responses 专属压缩链的
// original→compressed input-item 位置映射。专属链只做最老 item 删除
// （无摘要折叠），summaryIdx 恒为 -1；被删 item 落 TargetKindDropped，
// 保留 item 落 TargetKindRetained + TargetSpaceResponsesInput。
// before/after 任一不是合法 responses 形态时返回 nil（fail-open，
// 与 messages 车道一致）。
func BuildResponsesAlignmentMap(before, after []byte) []AlignmentInfo {
	return buildAlignmentMapWithExtractor(before, after, -1, "openai-responses", TargetSpaceResponsesInput, extractResponsesInputMessages)
}
