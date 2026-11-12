// injection_bridge.go —— PR-V4-11 的 detector 桥接（全面审计v3 53 增量移交轮，
// F-41「注入检测 v1 主链路双重空转」收口的第一步）。
//
// # 背景（F-41，cmd/gateway/main.go 原 7405-7407）
//
//	SetV2DispatchAnalysisResources(..., nil, nil, nil) —— detector / checker /
//	summarizer 三个形参恒为 nil，注释自认「需要 *sql.DB（不是 pgxpool），
//	后续 PR 再桥接」⇒ deps.PromptInjectionDetector == nil ⇒
//	main_pipeline.go 的 `governance_prompt_injection` stage 不注册；
//	同时 v1 包装器的 env.Metadata 从不写 user_content ⇒ 即使 detector 桥上，
//	hook.Enabled()（要求 Metadata["user_content"] 非空）也恒 false。
//	⇒ 注入检测 v1 主链路双重空转，实际生效的只有 armor observe-only。
//
// # 本文件补齐两件事
//
//	① detector 桥：db.DB.Stdlib()（共享 *sql.DB 桥接，见 db/db.go Stdlib 的
//	   所有权注释——调用方不得 Close）→ promptinjection.NewDetector；
//	   再经 adapter 把 *promptinjection.DetectionResult 映射成
//	   promptinjectionhooks.DetectionResult（两包刻意互不 import，
//	   hook 侧是最小镜像接口 ⇒ 这里是唯一合法的粘合点）。
//	② user_content 提取：v1 包装器（chat/completions/messages/responses 四个
//	   端点形态）的请求体里抽「用户侧文本」放进 env.Metadata["user_content"]，
//	   喂给 promptinjection hook / security hook / intentanalysis hook 三处
//	   既有消费方（它们都在，只是从来没被喂过）。
//
// # dump 面（rule 71 审查结论，见 domains/hooks/audit/hook.go 的 sanitize）
//
//	user_content 进 Metadata 后，任何「全量 dump Metadata」的面都会把原文带进
//	审计/日志。本轮逐面审查结论：pipeline 各 stage 无 slog 全量 dump；
//	唯一全量 dump 面 = audit hook（把 env.Metadata 原样塞进审计事件）⇒
//	审计侧改为**剥离大体积/敏感键**后再记录（见 audit hook 的 sanitizeAuditMetadata）。
//
// # 环境开关与失败语义
//
//	LLM_GATEWAY_V2_USER_CONTENT：user_content 写入开关，默认开（"0"/"false" 关）。
//	detector 构造失败：fail-open（不注册 stage、boot 响亮 ERROR）——
//	注入检测是治理面，不因它阻断请求主链路。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"unicode/utf8"

	promptinjectionhooks "github.com/kaixuan/llm-gateway-go/domains/hooks/promptinjection"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

// userContentMaxBytes 与 promptinjection 策略的默认 MaxInputLength（50000）对齐：
// 写入侧先截断，detector 侧策略截断（head 形态）落在同一口径上，避免
// 「每请求在 Metadata 里塞 1MB 原文」的内存放大。截断按 rune 安全切分。
const userContentMaxBytes = 50000

// userContentEnvToggle 控制 v1 包装器是否把 user_content 写进 Metadata。
func userContentEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_V2_USER_CONTENT")))
	return v != "0" && v != "false" && v != "no" && v != "off"
}

// promptInjectionDetectorBridge 把 *promptinjection.Detector 适配成
// promptinjectionhooks.Detector（hook 侧最小镜像）。
type promptInjectionDetectorBridge struct {
	inner *promptinjection.Detector
}

// newPromptInjectionDetector 构造桥接 detector。stdlibDB 为 nil 或构造失败时
// 返回 nil（stage 不注册；调用方记 boot 日志）。
func newPromptInjectionDetector(stdlibDB *sql.DB) promptinjectionhooks.Detector {
	if stdlibDB == nil {
		return nil
	}
	d, err := promptinjection.NewDetector(stdlibDB)
	if err != nil {
		return nil
	}
	return &promptInjectionDetectorBridge{inner: d}
}

// Detect 实现 promptinjectionhooks.Detector。
func (b *promptInjectionDetectorBridge) Detect(ctx context.Context, tenantID, input string) (*promptinjectionhooks.DetectionResult, error) {
	r, err := b.inner.Detect(ctx, tenantID, input)
	if err != nil || r == nil {
		return nil, err
	}
	return mapInjectionResult(r), nil
}

// mapInjectionResult 把 domains/promptinjection.DetectionResult 映射成 hook 侧镜像。
// 独立成纯函数：字段对位（14 个）是桥接的全部语义面，单测直接对拍——
// 字段错位/漏搬都会在这里被抓住，不必起真库。
func mapInjectionResult(r *promptinjection.DetectionResult) *promptinjectionhooks.DetectionResult {
	if r == nil {
		return nil
	}
	return &promptinjectionhooks.DetectionResult{
		Score:              r.Score,
		RiskLevel:          r.RiskLevel,
		Categories:         r.Categories,
		ActionTaken:        r.ActionTaken,
		Blocked:            r.Blocked,
		RequireApproval:    r.RequireApproval,
		ApprovalTimeoutMin: r.ApprovalTimeoutMin,
		Evidence:           r.Evidence,
		ReplacedContent:    r.ReplacedContent,
		CanaryTokenLeaked:  r.CanaryTokenLeaked,
		LLMConfidence:      r.LLMConfidence,
		LLMReason:          r.LLMReason,
		SessionHealthDelta: r.SessionHealthDelta,
		TerminateSession:   r.TerminateSession,
	}
}

// injectionUserContentFromBody 从 v1 四端点的请求体里抽「用户侧文本」。
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
func injectionUserContentFromBody(body []byte) string {
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

// truncateRuneSafe 按 rune 安全截断到 max 字节上限。
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
