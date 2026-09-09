package executors

import (
	"encoding/json"
	"strings"
)

// input_token_estimate.go (审计 R3 #2, 2026-09-09)
//
// Q2 桥(Anthropic 客户端 ← OpenAI 上游)的 message_start.usage.input_tokens
// 此前恒为 0:OpenAI 上游只在流尾(或根本不)携带 prompt_tokens,而 Anthropic
// 协议要求 message_start 在流首声明 input_tokens。Claude Code 等客户端按该值
// 管理上下文窗口,恒 0 会系统性低估输入 token,导致自动压缩(compaction)过晚
// 触发直至上下文溢出。
//
// SSE 是只写向前的流,message_start 一经写出便无法回填真实值(Anthropic 协议
// 的 message_delta.usage 仅承载 output_tokens)。因此在流首用请求体估算值填充:
// 略偏高是安全方向(提前压缩优于上下文溢出),真实 usage 到达后仅用于日志比对
// 与审计 capture,不做线上修正。
//
// 估算沿用仓内既有启发式(transformation.EstimateTokens 的 chars/3.5),并针对
// Anthropic 请求体做两处修正:图片/文档块按固定 token 成本计(避免 base64 字节
// 被 chars/3.5 放大约两个数量级),无空白长串(base64/hex blob)按更接近解码后
// 字节成本的 len/5 计。

const (
	// estimateCharsPerToken 与 transformation.EstimateTokens 的 charsPerToken
	// 一致(约 3.5 字符/Token,英文偏保守高估,CJK 按 UTF-8 字节计接近 1:1)。
	estimateCharsPerToken = 3.5
	// estimateBlobCharsPerToken 无空白长串的保守下界:base64 解码后约为自身
	// 长度的 3/4,再按 4 字符/Token。
	estimateBlobCharsPerToken = 5.0
	// estimateImageTokens 单个 image/document 块的固定成本。Anthropic 图片
	// token ≈ (宽×高)/750,常见截图/照片在 800-2000 区间,取中位偏上值。
	estimateImageTokens = 1600
	// estimatePerMessageTokens 每条消息的角色/框架开销近似值。
	estimatePerMessageTokens = 8
	// estimateBlobMinBytes 判定"无空白长串"的最小长度,避免误伤正常短文本。
	estimateBlobMinBytes = 4096
)

// estimateAnthropicInputTokens 基于 Anthropic 形状的请求体估算上游输入 token 数。
//
// 覆盖 system、tools 与 messages;解析失败或非 Anthropic 形状(无 messages 且无
// system)时回退为整体 len/3.5,保证任何输入都有非零估计(与旧行为 0 相比仍是
// 改进)。body 为空返回 0。
func estimateAnthropicInputTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}

	var req struct {
		System   json.RawMessage   `json:"system"`
		Tools    []json.RawMessage `json:"tools"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return int(float64(len(body)) / estimateCharsPerToken)
	}

	if len(req.Messages) == 0 && len(req.System) == 0 {
		// 非 Anthropic 形状(如已是转换后的 OpenAI 体):退回整体估算。
		return int(float64(len(body)) / estimateCharsPerToken)
	}

	total := 0
	for _, tool := range req.Tools {
		total += int(float64(len(tool)) / estimateCharsPerToken)
	}
	total += estimateContentTokens(req.System)
	for _, msg := range req.Messages {
		total += estimateContentTokens(msg) + estimatePerMessageTokens
	}
	if total <= 0 {
		total = int(float64(len(body)) / estimateCharsPerToken)
	}
	return total
}

// estimateContentTokens 递归统计一段请求 JSON 的 token 估计:字符串按
// chars/3.5(blob 按 chars/5),image/document 块按固定成本,其余结构递归。
func estimateContentTokens(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return int(float64(len(raw)) / estimateCharsPerToken)
	}
	return estimateJSONTokens(v)
}

func estimateJSONTokens(v any) int {
	switch t := v.(type) {
	case string:
		return estimateStringTokens(t)
	case []any:
		n := 0
		for _, e := range t {
			n += estimateJSONTokens(e)
		}
		return n
	case map[string]any:
		switch t["type"] {
		case "image", "document":
			// {"type":"image","source":{"type":"base64","data":"..."}} 等,
			// 数据本体按固定成本计,不按 base64 字节放大。
			return estimateImageTokens
		}
		n := 0
		for _, e := range t {
			n += estimateJSONTokens(e)
		}
		return n
	default:
		// 数字/布尔/null 的 JSON 表示开销忽略不计。
		return 0
	}
}

// estimateStringTokens 估算单个字符串的 token:正常文本按 chars/3.5;超长且
// 无空白的前缀视为 base64/hex blob(图片数据、内嵌文件),按 chars/5 计以
// 贴近解码后字节成本,避免 chars/3.5 放大两个数量级。
func estimateStringTokens(s string) int {
	if s == "" {
		return 0
	}
	prefix := s
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}
	if len(s) >= estimateBlobMinBytes && !strings.ContainsAny(prefix, " \t\n\r") {
		return int(float64(len(s)) / estimateBlobCharsPerToken)
	}
	return int(float64(len(s)) / estimateCharsPerToken)
}
