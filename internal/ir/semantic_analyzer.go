package ir

import (
	"encoding/json"
	"strings"
)

// SemanticAnalyzer 分析响应的语义完整性，检测潜在的工具调用丢失问题
type SemanticAnalyzer struct {
	// 配置项
	EnableAnalysis bool
}

// NewSemanticAnalyzer 创建语义分析器
func NewSemanticAnalyzer(enabled bool) *SemanticAnalyzer {
	return &SemanticAnalyzer{
		EnableAnalysis: enabled,
	}
}

// AnalysisResult 语义分析结果
type AnalysisResult struct {
	// IsIncomplete 表示响应语义上未完成（但没有tool_calls）
	IsIncomplete bool
	// Reason 不完整的原因
	Reason string
	// Confidence 置信度 (0.0-1.0)
	Confidence float64
	// SuspectedMissingTools 疑似缺失的工具调用标志
	SuspectedMissingTools bool
	// Indicators 触发的指标列表
	Indicators []string
}

// AnalyzeResponse 分析响应是否语义完整
//
// 检测场景：
//  1. 响应文本包含"正在调用"、"let me"、"I'll use"等工具调用意图词汇，但没有tool_calls
//  2. 响应文本突然截断（不完整句子、未闭合的引号/括号）
//  3. finish_reason 是 "stop" 但内容暗示未完成
//  4. 响应为空或仅包含极短内容（<10字符）且finish_reason不是"length"或"content_filter"
func (sa *SemanticAnalyzer) AnalyzeResponse(resp *InternalResponse) *AnalysisResult {
	if !sa.EnableAnalysis || resp == nil {
		return &AnalysisResult{IsIncomplete: false}
	}

	result := &AnalysisResult{
		IsIncomplete:          false,
		Confidence:            0.0,
		SuspectedMissingTools: false,
		Indicators:            []string{},
	}

	// 提取文本内容
	textContent := extractTextContent(resp)
	hasToolCalls := len(resp.ToolCalls) > 0

	// 检查1: 工具调用意图词汇（中英文）
	if !hasToolCalls {
		toolIntentScore := sa.detectToolIntentPhrases(textContent)
		if toolIntentScore > 0.5 {
			result.IsIncomplete = true
			result.SuspectedMissingTools = true
			result.Confidence = toolIntentScore
			result.Reason = "Response contains tool invocation intent phrases but no tool_calls present"
			result.Indicators = append(result.Indicators, "tool_intent_phrases")
		}
	}

	// 检查2: 文本突然截断
	if sa.detectTruncation(textContent) {
		result.IsIncomplete = true
		result.Confidence = maxFloat(result.Confidence, 0.7)
		result.Reason = appendReason(result.Reason, "Response text appears truncated (incomplete sentence or unclosed punctuation)")
		result.Indicators = append(result.Indicators, "text_truncation")
	}

	// 检查3: finish_reason 与内容不一致
	if resp.FinishReason == "stop" && !hasToolCalls {
		if sa.detectInconsistentStop(textContent) {
			result.IsIncomplete = true
			result.Confidence = maxFloat(result.Confidence, 0.6)
			result.Reason = appendReason(result.Reason, "finish_reason is 'stop' but content suggests continuation")
			result.Indicators = append(result.Indicators, "inconsistent_stop")
		}
	}

	// 检查4: 空响应或极短响应（排除合法的短响应）
	if len(strings.TrimSpace(textContent)) < 10 && !hasToolCalls {
		if resp.FinishReason != "length" && resp.FinishReason != "content_filter" {
			result.IsIncomplete = true
			result.Confidence = maxFloat(result.Confidence, 0.8)
			result.Reason = appendReason(result.Reason, "Response is unusually short without valid finish_reason")
			result.Indicators = append(result.Indicators, "empty_or_short")
		}
	}

	// 检查5: 工具结果消息后的空响应（疑似工具调用响应丢失）
	if sa.detectMissingToolResponse(resp) {
		result.IsIncomplete = true
		result.SuspectedMissingTools = true
		result.Confidence = maxFloat(result.Confidence, 0.9)
		result.Reason = appendReason(result.Reason, "Response appears to be missing tool call after tool result message")
		result.Indicators = append(result.Indicators, "missing_tool_after_result")
	}

	return result
}

// detectToolIntentPhrases 检测工具调用意图短语
func (sa *SemanticAnalyzer) detectToolIntentPhrases(text string) float64 {
	text = strings.ToLower(text)

	// 英文工具调用意图短语
	englishPhrases := []string{
		"let me use",
		"i'll use",
		"i will use",
		"calling",
		"using the",
		"tool to",
		"function to",
		"i'll call",
		"i will call",
		"let me call",
		"i need to call",
		"i'm calling",
		"i am calling",
	}

	// 中文工具调用意图短语
	chinesePhrases := []string{
		"正在调用",
		"我将调用",
		"我会调用",
		"让我调用",
		"使用工具",
		"调用工具",
		"执行工具",
		"需要调用",
		"我需要使用",
	}

	score := 0.0
	matchCount := 0

	for _, phrase := range englishPhrases {
		if strings.Contains(text, phrase) {
			matchCount++
			score += 0.3
		}
	}

	for _, phrase := range chinesePhrases {
		if strings.Contains(text, phrase) {
			matchCount++
			score += 0.3
		}
	}

	// 限制最高分数
	if score > 1.0 {
		score = 1.0
	}

	return score
}

// detectTruncation 检测文本截断
func (sa *SemanticAnalyzer) detectTruncation(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		return false
	}

	// 检查未闭合的引号
	quoteCount := strings.Count(text, "\"")
	if quoteCount%2 != 0 {
		return true
	}

	// 检查未闭合的括号
	openParens := strings.Count(text, "(")
	closeParens := strings.Count(text, ")")
	if openParens != closeParens {
		return true
	}

	openBrackets := strings.Count(text, "[")
	closeBrackets := strings.Count(text, "]")
	if openBrackets != closeBrackets {
		return true
	}

	openBraces := strings.Count(text, "{")
	closeBraces := strings.Count(text, "}")
	if openBraces != closeBraces {
		return true
	}

	// 检查是否以不完整的句子结尾（没有句号、问号、感叹号）
	lastChar := text[len(text)-1]
	// 使用字符串比较而非 rune 比较
	if lastChar != '.' && lastChar != '?' && lastChar != '!' && lastChar != '\n' &&
		!strings.HasSuffix(text, "。") && !strings.HasSuffix(text, "？") && !strings.HasSuffix(text, "！") {
		// 进一步检查：如果最后是逗号或者连词，更可能是截断
		if lastChar == ',' ||
			strings.HasSuffix(text, "，") ||
			strings.HasSuffix(text, " and") ||
			strings.HasSuffix(text, " or") ||
			strings.HasSuffix(text, " but") ||
			strings.HasSuffix(text, "并且") ||
			strings.HasSuffix(text, "或者") {
			return true
		}
	}

	return false
}

// detectInconsistentStop 检测finish_reason与内容不一致
func (sa *SemanticAnalyzer) detectInconsistentStop(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))

	// 检查结尾是否包含延续性词汇
	continuationPhrases := []string{
		"next,",
		"then,",
		"after that,",
		"following this,",
		"subsequently,",
		"接下来",
		"然后",
		"之后",
		"随后",
	}

	for _, phrase := range continuationPhrases {
		if strings.HasSuffix(text, phrase) {
			return true
		}
	}

	return false
}

// detectMissingToolResponse 检测工具结果消息后缺失响应的情况
func (sa *SemanticAnalyzer) detectMissingToolResponse(resp *InternalResponse) bool {
	// 这个检查需要访问请求上下文中的消息历史
	// 当前InternalResponse不包含请求历史，所以这个检查需要在调用层实现
	// 这里仅作为占位符
	return false
}

// extractTextContent 从响应中提取文本内容
func extractTextContent(resp *InternalResponse) string {
	if resp == nil {
		return ""
	}

	var texts []string

	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}

	return strings.Join(texts, " ")
}

// appendReason 追加原因（避免覆盖）
func appendReason(existing, newReason string) string {
	if existing == "" {
		return newReason
	}
	return existing + "; " + newReason
}

// maxFloat 返回两个float64中的最大值
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// CompareWithRawLog 对比原始日志，检测转换过程中的数据丢失
//
// 该函数用于在检测到疑似工具调用丢失时，对比原始上游响应数据
// 判断是否在IR转换过程中丢失了tool_calls
func CompareWithRawLog(rawUpstreamBody []byte, irResponse *InternalResponse) (bool, string) {
	if len(rawUpstreamBody) == 0 {
		return false, "no raw data available"
	}

	// 尝试解析原始响应为通用结构
	var rawData map[string]interface{}
	if err := json.Unmarshal(rawUpstreamBody, &rawData); err != nil {
		return false, "failed to parse raw data"
	}

	// 检查原始数据中是否包含tool_calls或tools（不同协议字段不同）
	hasToolCallsInRaw := false
	var toolCallsField interface{}

	// OpenAI: choices[0].message.tool_calls
	if choices, ok := rawData["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if toolCalls, ok := message["tool_calls"]; ok && toolCalls != nil {
					hasToolCallsInRaw = true
					toolCallsField = toolCalls
				}
			}
		}
	}

	// Anthropic: content[].type == "tool_use"
	if content, ok := rawData["content"].([]interface{}); ok {
		for _, block := range content {
			if blockMap, ok := block.(map[string]interface{}); ok {
				if blockType, ok := blockMap["type"].(string); ok && blockType == "tool_use" {
					hasToolCallsInRaw = true
					toolCallsField = block
					break
				}
			}
		}
	}

	// 对比IR响应
	hasToolCallsInIR := irResponse != nil && len(irResponse.ToolCalls) > 0

	if hasToolCallsInRaw && !hasToolCallsInIR {
		// 发现数据丢失！
		toolCallsJSON, _ := json.Marshal(toolCallsField)
		return true, string(toolCallsJSON)
	}

	return false, ""
}
