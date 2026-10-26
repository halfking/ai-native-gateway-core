package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/emptyoutcome"
)

const credentialProbeBodyLimit = 64 << 10

// credentialProbeModelsBodyLimit 是 /v1/models 目录专用的读上限。
//
// 2026-10-20 12h 审计轮修复：单次完成/单条消息的响应体天然很小，64KiB 够用；
// 但 /v1/models 返回的是**整个模型目录**，条目形如
// {"id":"...","object":"model","owned_by":"..."} ≈ 50 字节 ⇒ 64KiB 只装得下
// 约 1.3k 个模型，聚合型供应商很容易越线。
// 越线的后果不是「截断后继续判健康」，而是 readCredentialProbeBody 直接返回
// 错误 → classifyProbeHTTPResponse 把 2xx 判成 probe_response_inconclusive
// → 该模型**永远到不了 healthy_confirmed**，且每轮周期都被重新选中
// （旧写法是截断后按 2xx 判 ok，所以这是一处**行为回退**）。
// ⇒ 这里给目录型端点单独放宽到 1MiB（约 2 万个模型，仍是有界读）。
const credentialProbeModelsBodyLimit = 1 << 20

// Retain bounded classification evidence before shortening operator previews.
// Quota codes can follow long vendor metadata, beyond the old 1KiB read cap.
func credentialProbeHTTPFailure(protocol string, status int, body []byte) string {
	return fmt.Sprintf("%s status %d: %s", protocol, status, errorsx.SanitizeErrorText(body, credentialProbeBodyLimit))
}

func credentialProbeReadFailure(protocol string, status int, err error) string {
	if status >= 400 {
		// Receiving 401 is auth evidence even when its body cannot be read.
		// Ambiguous 403 keeps the existing fail-closed auth classification.
		return credentialProbeHTTPFailure(protocol, status, []byte("response body unavailable: "+err.Error()))
	}
	return protocol + " probe response invalid: " + err.Error()
}

// A low-budget reasoning response can establish neither success nor failure.
// Record only cadence; keep auth/quota evidence, bindings and health untouched.
//
// ★★ 2026-10-27（R152 轮 · D9c）：「只记节奏」以前真的**什么都不记**
// ⇒ ★★★ 探针每 5 s 重新武装一次、状态一行不改 ⇒ **既不解封、也不退避**
// ⇒ ⇒ ★★ 模型被钉死在死状态（R151 实测：`node_probe_state` 一行不改，
// `consecutive_failures` 卡在 3 ⇒ `broken_confirmed` ⇒ 路由器硬排除）。
//
// ⇒ ⇒ **D9c：inconclusive 必须【参与退避阶梯】** ⇒ 体现在
// `probe_consecutive_failures` 上：
//
//	① 它**只**驱动【探针节奏】（auth 退避阶梯 `recover_at = base << n`、
//	   quota 探针的 due 门）—— ★★ **不**驱动健康判定
//	   （健康结论走 `health_status` / `availability_state` / `state_reason_code`，
//	   而 ObservationOnly 路径**整条跳过**那个写入）⇒ ⇒ ★★ **inconclusive
//	   不会把一个健康对判死**（★ 这是 R151 确立的关键区分，不能破坏）；
//	② 它让「什么都没发生」变成「**发生了一次，并按阶梯退避**」
//	   ⇒ ⇒ 探针不再每 5 s hammer ⇒ ★★ 而阶梯封顶 6 h，探针**仍在跑**
//	   ⇒ ⇒ 一旦内容预算被修好（D9b），下一次成功探测就能把它解封；
//	③ 它被一次成功探测归零（`probe_consecutive_failures = 0`，见
//	   credential_probe_v2.go 的 healthy 分支）⇒ ⇒ **自愈路径完好**。
//
// ⚠ 顺带说明 `last_probe_success = FALSE`：**它不是健康判定**，
// 只是「上一次探测没有得到成功结论」的记账（`last_probe_at` 喂给
// balance/periodic quota 探针的 due 过滤）。
const credentialProbeObservationSQL = `UPDATE credentials
SET last_probe_at = NOW(), last_probe_success = FALSE,
    probe_consecutive_failures = COALESCE(probe_consecutive_failures, 0) + 1
WHERE id = $1 AND COALESCE(manual_disabled, FALSE) = FALSE
  AND (lifecycle_status = 'active' OR
       (lifecycle_status = 'disabled' AND auto_disabled_at IS NOT NULL))`

func writeCredentialProbeObservation(ctx context.Context, db bindingProbeExecer, credID int) (pgconn.CommandTag, error) {
	return db.Exec(ctx, credentialProbeObservationSQL, credID)
}

func credentialProbeInvalidResponse(message string) bool {
	if strings.HasPrefix(message, "chat unreachable (after ") {
		if i := strings.Index(message, "): "); i >= 0 {
			message = message[i+3:]
		}
	}
	for _, protocol := range []string{"chat", "messages", "responses"} {
		if strings.HasPrefix(message, protocol+" probe response invalid: ") {
			return true
		}
	}
	return false
}

// Read the complete bounded response: a truncated JSON prefix cannot prove
// health, and a body read error must not turn a received 200 into success.
func readCredentialProbeBody(r io.Reader) ([]byte, error) {
	return readCredentialProbeBodyLimit(r, credentialProbeBodyLimit)
}

// readCredentialProbeBodyLimit 是带显式上限的读；上限必须由调用方按
// **响应体形态**选择：单次生成用默认 64KiB，模型目录用
// credentialProbeModelsBodyLimit（见该常量处的说明）。
// 超过上限一律返回错误而不是截断 —— 截断出来的 JSON 前缀不是证据。
func readCredentialProbeBodyLimit(r io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = credentialProbeBodyLimit
	}
	body, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}

type credentialProbeContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
	Name    string `json:"name"`
}

func credentialProbeText(parts []credentialProbeContent) bool {
	for _, p := range parts {
		switch p.Type {
		case "", "text", "output_text", "refusal":
			if p.Text != "" || p.Refusal != "" {
				return true
			}
		}
	}
	return false
}

// emptyoutcome's unknown/malformed fallback protects business payloads from
// being discarded. A probe needs positive evidence, so additionally require
// the expected envelope and recognized assistant text or a named tool call.
//
// 2026-10-26 多模态标准接口轮（本地 ollama 接入实测）：thinking 默认开启的
// 模型（qwen3/qwen3.8 系）在探针的固定载荷（max_tokens=32、不带 think:false）
// 下把全部预算烧进 reasoning、content 为空 ⇒ ClassifyBody 按 r1029 的业务
// 语义判「空」⇒ 探针 inconclusive ⇒ 节点永远翻不了绿（211 实测与冷加载
// 超时叠加成 node_probe_failed 活锁）。探针问的是「**节点活着吗**」，与
// r1029 管的「这条回答能不能交付给用户」是两个问题：reasoning 有输出同样
// 是模型存活的一手证据。故 chat 形态在「业务判空」之后、放弃之前，补一道
// reasoning 检查（ollama 的 message.reasoning 与 deepseek/openrouter 风格的
// message.reasoning_content）。业务面（emptyoutcome/中继 failover）一字不动。
// 这正是《嵌入与会话总结优化方案-2026-10-26》§14.4 建议的网关补丁一。
func credentialProbeHasOutput(protocol string, body []byte) bool {
	format, empty := emptyoutcome.ClassifyBody(body)
	want := map[string]emptyoutcome.Format{"chat": emptyoutcome.FormatChat,
		"messages": emptyoutcome.FormatAnthropic, "responses": emptyoutcome.FormatResponses}[protocol]
	if format == emptyoutcome.FormatUnknown || format != want {
		return false
	}
	var envelope struct {
		Error   json.RawMessage          `json:"error"`
		Type    string                   `json:"type"`
		Status  string                   `json:"status"`
		Content []credentialProbeContent `json:"content"`
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				Reasoning        json.RawMessage `json:"reasoning"`         // ollama qwen3 系
				ReasoningContent json.RawMessage `json:"reasoning_content"` // deepseek/openrouter 风格
				ToolCalls        []struct {
					Function *struct{ Name string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Output []struct {
			Type    string                   `json:"type"`
			Name    string                   `json:"name"`
			Content []credentialProbeContent `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &envelope) != nil || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return false
	}
	hasReasoningEvidence := false
	for _, choice := range envelope.Choices {
		if rawNonEmptyJSONString(choice.Message.Reasoning) || rawNonEmptyJSONString(choice.Message.ReasoningContent) {
			hasReasoningEvidence = true
			break
		}
	}
	if empty && !(protocol == "chat" && hasReasoningEvidence) {
		return false
	}
	switch protocol {
	case "chat":
		for _, choice := range envelope.Choices {
			var text string
			if json.Unmarshal(choice.Message.Content, &text) == nil && text != "" {
				return true
			}
			// 探针的存活语义：reasoning 有输出 = 模型活着（见函数头注释）。
			if rawNonEmptyJSONString(choice.Message.Reasoning) || rawNonEmptyJSONString(choice.Message.ReasoningContent) {
				return true
			}
			var parts []credentialProbeContent
			if json.Unmarshal(choice.Message.Content, &parts) == nil && credentialProbeText(parts) {
				return true
			}
			for _, tc := range choice.Message.ToolCalls {
				if tc.Function != nil && tc.Function.Name != "" {
					return true
				}
			}
		}
	case "messages":
		if envelope.Type != "" && envelope.Type != "message" {
			return false
		}
		if credentialProbeText(envelope.Content) {
			return true
		}
		for _, p := range envelope.Content {
			if p.Type == "tool_use" && p.Name != "" {
				return true
			}
		}
	case "responses":
		// This checks reachability and assistant output, not a complete answer:
		// a terminal token-budget stop with real text is sufficient. Reasoning
		// alone is not; failed, cancelled and unfinished executions never are.
		if envelope.Status != "" && envelope.Status != "completed" && envelope.Status != "incomplete" {
			return false
		}
		for _, item := range envelope.Output {
			if (item.Type == "message" && credentialProbeText(item.Content)) ||
				(item.Type == "function_call" && item.Name != "") {
				return true
			}
		}
	}
	return false
}

// rawNonEmptyJSONString 判定一个 JSON 原始字段是否为非空字符串
// （去掉首尾空白后非空）。用于 reasoning/reasoning_content 的存活证据检查。
func rawNonEmptyJSONString(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	return strings.TrimSpace(s) != ""
}
