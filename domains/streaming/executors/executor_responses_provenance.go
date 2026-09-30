package executors

// R34-A1: responses 专属压缩链的 provenance 组装。此前该链（candidate
// window 前置裁剪 + 4xx recovery aggressive 裁剪）只输出裁剪后的 body，
// request_logs 上无"哪些 input item 被删"的审计证据；messages 车道
// 本就带 AlignmentMap（R34 批判复审 A-G3 定论），本文件把对齐证据补齐
// 到 responses 车道，最终经 ExecResult.CompressionMeta 落
// request_logs.compression_meta（v7 §3.2 JSONB，运维 SQL 可查）。

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/transformation"
)

// recordResponsesInputTrimMeta 把一次 responses input 裁剪的证据写入
// dst（provenance JSON）。meta 为 nil（未触发压缩）时是 no-op——
// 调用方无需自行判断。4xx recovery 路径以同函数覆盖写入，后写 win。
func recordResponsesInputTrimMeta(dst *[]byte, before, after []byte, meta *transformation.ResponsesInputTrimMeta) {
	if dst == nil || meta == nil {
		return
	}
	payload := map[string]any{
		"strategy":     meta.Strategy,
		"trim_phase":   "pre_request",
		"bytes_before": len(before),
		"bytes_after":  len(after),
	}
	if meta.OriginalItems > 0 {
		payload["original_items"] = meta.OriginalItems
		payload["kept_items"] = meta.KeptItems
		payload["dropped_input_indexes"] = meta.DroppedIndexes
	}
	if meta.InputString {
		payload["input_string"] = true
		payload["original_runes"] = meta.OriginalRunes
		payload["kept_runes"] = meta.KeptRunes
	}
	// AlignmentMap：original→compressed 的 input-item 位置映射
	//（TargetSpaceResponsesInput 坐标系）。string 形态无 item 粒度，
	// 抽取器返回单条包装消息，映射退化为 1:1，此时省略以免误导。
	if !meta.InputString {
		if alignment := compression.BuildResponsesAlignmentMap(before, after); len(alignment) > 0 {
			payload["alignment_map"] = alignment
		}
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return
	}
	*dst = out
}
