package main

// user_content 全形态提取器（2026-11-13 48h 审计轮 P1 修复，源自 4225add0e 的
// injection_bridge.go 提取器移植）。
//
// 缺口（本审计轮对 daf3fce5b 的裁定，主档 §三）：并入的 lastUserContentFromBody
// 只经 compressionMessagesFromBody 取 `messages[].content` **纯字符串**单形态，
// 其余三族端点的用户文本全部取不到（提取结果恒空 ⇒ user_content 键不写 ⇒
// promptinjection/security/intentanalysis 三个 hook 对这些端点继续空转）：
//  1. /v1/completions 的 prompt（string 或 [string]）——体无 messages 数组；
//  2. anthropic /v1/messages 的 content blocks（数组形态）——且 Content string
//     反序列化失败会让**连 chat 腿也整体丢失**（Unmarshal 报错 ⇒ 返回 nil）；
//  3. /v1/responses 的 input（string 或 message 数组，块类型 input_text）；
//  4. 多模态 parts（content:[{type:"text",text:...}]）——同 2。
// 另：content[:64KiB] 按字节截断会分裂多字节 UTF-8 rune ⇒ 非法尾字节进
// Metadata（P2）。本文件以 rune 安全截断 + 四形态覆盖同轮收口。

import (
	"encoding/json"
	"os"
	"strings"
	"unicode/utf8"
)

// userContentMaxBytes 与 promptinjection 策略的默认 MaxInputLength（50000）对齐：
// 写入侧先截断，detector 侧策略截断（head 形态）落在同一口径上，避免
// 「每请求在 Metadata 里塞 1MB 原文」的内存放大。截断按 rune 安全切分
// （旧值 64KiB 按字节切，见上方 P2）。
const userContentMaxBytes = 50000

// userContentEnabled 控制 v1 包装器是否把 user_content 写进 Metadata。
// 默认开（未设置/设为任意非否定值都开）；显式关闭是运行阀，不是灰度开关
// ——hook 消费面（promptinjection/security/intentanalysis）在 v2 pipeline
// 之内，pipeline 本身默认关。
func userContentEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_V2_USER_CONTENT")))
	return v != "0" && v != "false" && v != "no" && v != "off"
}

// userContentFromBody 从 v1 四端点的请求体里抽「用户侧文本」。
//
// 覆盖形态：
//   - OpenAI chat（/v1/chat/completions）：messages[].content（string 或
//     [{type,text}] 多模态形态），只取 role=="user" 的文本；
//   - OpenAI completions（/v1/completions）：prompt（string 或 [string]）；
//   - Anthropic（/v1/messages）：messages[].content（string 或 content blocks），
//     只取 role=="user"；system 不算用户内容（注入语义上它是运营商侧输入）；
//   - Responses（/v1/responses）：input（string，或 [{role,content...}] 形态，
//     content 兼容 string / [{type,text}]）。
//
// 多条用户消息按出现顺序以 "\n" 连接（detector 的 head 截断口径下，
// 前文保留语义 = 上下文注入同样可检）。解析失败/无用户文本返回 ""。
// 逐消息独立 Unmarshal：单条坏消息跳过而不是整体放弃（旧实现的整体
// Unmarshal 形态会把「一条 anthropic 数组 content」放大成全端点取空）。
func userContentFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
		Prompt   json.RawMessage   `json:"prompt"`
		Input    json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}

	var parts []string
	appendText := func(s string) {
		if t := strings.TrimSpace(s); t != "" {
			parts = append(parts, t)
		}
	}

	// —— messages[]（openai chat / anthropic 共形）——
	for _, raw := range probe.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if !strings.EqualFold(msg.Role, "user") {
			continue
		}
		for _, s := range textsFromContentJSON(msg.Content) {
			appendText(s)
		}
	}
	// —— prompt（openai completions）——
	if len(probe.Prompt) > 0 {
		for _, s := range textsFromContentJSON(probe.Prompt) {
			appendText(s)
		}
	}
	// —— input（responses：string 或 message 数组）——
	if len(probe.Input) > 0 {
		var inputStr string
		if err := json.Unmarshal(probe.Input, &inputStr); err == nil {
			appendText(inputStr)
		} else {
			var inputMsgs []json.RawMessage
			if err := json.Unmarshal(probe.Input, &inputMsgs); err == nil {
				for _, raw := range inputMsgs {
					var im struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					}
					if err := json.Unmarshal(raw, &im); err != nil {
						continue
					}
					if im.Role != "" && !strings.EqualFold(im.Role, "user") {
						continue
					}
					for _, s := range textsFromContentJSON(im.Content) {
						appendText(s)
					}
				}
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, "\n")
	return truncateRuneSafe(joined, userContentMaxBytes)
}

// textsFromContentJSON 宽容解出 content 字段里的文本：
// string ⇒ 单元素；[{type,text}...]（openai/anthropic content blocks）⇒ 取 text；
// [string] ⇒ 逐元素。其它形态（工具调用块等）忽略。
func textsFromContentJSON(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	// content blocks：[{type:"text",text:"..."}] / [string]
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		var str string
		if err := json.Unmarshal(b, &str); err == nil {
			out = append(out, str)
			continue
		}
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &blk); err == nil {
			switch blk.Type {
			case "", "text", "input_text", "output_text":
				// text = openai/anthropic 通用；input_text/output_text = responses。
				out = append(out, blk.Text)
			}
		}
	}
	return out
}

// truncateRuneSafe 按 rune 安全截断到 max 字节上限（不分裂多字节字符）。
func truncateRuneSafe(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
