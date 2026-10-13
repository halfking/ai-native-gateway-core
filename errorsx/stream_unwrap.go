package errorsx

import (
	"encoding/json"
	"strconv"
	"strings"
)

// UnwrapStreamWrapped4xx 解出聚合中转在流式路径上包裹的「内层 4xx」。
//
// 2026-10-13（Vapeur gpt-5.6-sol temperature 事故链第 3 环）：Vapeur 这类
// 聚合中转的流式出站腿会把上游真实 4xx 缓冲后重发成 HTTP 502，原始状态码
// 与错误体嵌在 message 字段里：
//
//	{"error":{"code":"stream_error","type":"server_error",
//	 "message":"400|{\n  \"error\": {\n    \"message\": \"Unsupported
//	 parameter: 'temperature' is not supported with this model.\", ...}"} }}
//
// 后果（事故现场 785ca12e，2026-10-13 07:28）：外层 502 被 5xx 家族收编为
// KindUpstreamDown——请求侧参数错误被当成节点故障，盲重试三连 502，
// node_probe_state 记 3 连败 + 300s 退避，随后整个模型候选清空
// （candidate db_empty），健康节点被停 5 分钟。
//
// 解包后按内层状态码/错误体重新分类（client_bug / param_rejected 族），
// reqprobe 的 4xx 诊断门（executor 侧）也用解包结果，剥参重试才能在
// 流式路径触发。只认 4xx 内层：5xx|{...} 仍是真上游故障，不走此路。
//
// 判形保守：message 必须以 "NNN|"（4xx）开头且余下部分是完整 JSON 对象，
// 避免把恰好以数字开头的中转文案误当包裹体。
func UnwrapStreamWrapped4xx(body []byte) (int, []byte, bool) {
	st, embedded, ok := UnwrapStreamWrapped(body)
	if ok && st >= 400 && st <= 499 {
		return st, embedded, true
	}
	return 0, nil, false
}

// UnwrapStreamWrapped 是宽形态：内层 4xx 或 5xx 都解（2026-10-13 claude
// 轮实测：Vapeur 对 claude 系流式回 502 包 502——内层是「该供应商不支持
// Responses API 流式请求」的能力裁决，需要进分类器而不是当 transient
// 盲重试）。reqprobe 的剥参门继续用 4xx 形态（UnwrapStreamWrapped4xx）。
func UnwrapStreamWrapped(body []byte) (int, []byte, bool) {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Message == "" {
		return 0, nil, false
	}
	msg := envelope.Error.Message
	if len(msg) < 4 || msg[3] != '|' {
		return 0, nil, false
	}
	st, err := strconv.Atoi(msg[:3])
	if err != nil || st < 400 || st > 599 {
		return 0, nil, false
	}
	embedded := []byte(strings.TrimSpace(msg[4:]))
	if len(embedded) == 0 || embedded[0] != '{' || !json.Valid(embedded) {
		return 0, nil, false
	}
	return st, embedded, true
}
