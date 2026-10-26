// bg/probe_responses_fallback.go — responses-unsupported 探针降级（2026-09-28 vapeur 轮）。
//
// 背景：vapeur 是多厂商聚合中转，provider.protocol=openai-responses 让全部
// 探针链路（node_probe 直连轮 / model_probe Layer4 / active_probe_executor /
// credential_probe_v2 step2）只按 /v1/responses 形态探测。实测（2026-09-28）
// 该中转的 /v1/responses 仅对 GPT 系开放：claude 全系 400「该供应商不支持
// Responses API」、qwen/doubao 系 502「X provider does not support the
// Responses API」，而这些模型的 /v1/chat/completions 全部 200。结果：非 GPT
// 模型被探红 → URSM v2 节点视图 available=0 → 路由整体排除 vapeur →
// "经常出错不通"。
//
// 本文件提供唯一的共享降级原语：当 responses 探针被上游判定为「不支持
// Responses API」时（providercap.ResponsesUnsupportedError），向同一
// base_url 的 /v1/chat/completions 补一发 chat 探针。chat 绿 → 该
// (credential, model) 经 chat 可用，探针按成功回报；chat 也挂 → 维持原
// responses 失败结论并附注 fallback 也失败。预算复用 ProbePingMaxTokens；
// 推理耗尽预算而没有真实输出时只能记录不确定，不能判健康或摘流。
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// responsesFallbackChatMaxTokens 是 chat 降级探针的 max_tokens。推理模型
// （claude-opus-5 等）1 个 token 连 reasoning 都放不下 → 上游 400；
// 所有文本探针使用同一预算下限，避免中转 Responses 的 >=16 限制。
const responsesFallbackChatMaxTokens = providercap.ProbePingMaxTokens

// responsesChatFallbackResult retains uncertainty independently of HTTP errors.
type responsesChatFallbackResult struct {
	HTTPStatus   int
	Body         string // complete bounded, sanitized response for classification
	LatencyMs    int
	OK           bool
	Inconclusive bool
}

// Keep the legacy tuple for existing callers; consumers that update health
// must use the typed result to distinguish a failed probe from no evidence.
func responsesChatFallbackPing(ctx context.Context, client *http.Client, apiKey, baseURL, model string) (int, string, int, bool) {
	r := responsesChatFallbackRound(ctx, client, apiKey, baseURL, model)
	return r.HTTPStatus, r.Body, r.LatencyMs, r.OK
}

func responsesChatFallbackRound(ctx context.Context, client *http.Client, apiKey, baseURL, model string) responsesChatFallbackResult {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := upstreamurl.Build(strings.TrimRight(baseURL, "/"), upstreamurl.EpChatCompletions)
	body, _ := json.Marshal(map[string]any{
		"model": model, "messages": []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": responsesFallbackChatMaxTokens,
	})
	start := time.Now()
	r := responsesChatFallbackResult{}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		r.Body = fmt.Sprintf("build chat fallback request: %s", err.Error())
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	r.LatencyMs = int(time.Since(start).Milliseconds())
	if err != nil {
		r.Body = fmt.Sprintf("chat fallback unreachable: %s", err.Error())
		return r
	}
	defer resp.Body.Close()
	r.HTTPStatus = resp.StatusCode
	response, readErr := readCredentialProbeBody(resp.Body)
	r.Body = string(errorsx.SanitizeErrorText(response, credentialProbeBodyLimit))
	if readErr != nil {
		r.Body = credentialProbeReadFailure("chat", resp.StatusCode, readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.OK = readErr == nil && probeResponseHasOutput("chat", response)
		r.Inconclusive = !r.OK
	}
	return r
}

// Text/tool probes share the credential validator. Audio probes additionally
// need positive payload evidence; an audio id or empty object is insufficient.
func probeResponseHasOutput(protocol string, body []byte) bool {
	if credentialProbeHasOutput(protocol, body) {
		return true
	}
	if protocol != "chat" {
		return false
	}
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Type    string          `json:"type"`
		Object  string          `json:"object"`
		Choices []struct {
			Message struct {
				Audio *struct {
					Data       string
					Transcript string
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Type == "error" ||
		(len(envelope.Error) > 0 && string(envelope.Error) != "null") ||
		(envelope.Object != "" && envelope.Object != "chat.completion") {
		return false
	}
	for _, choice := range envelope.Choices {
		if audio := choice.Message.Audio; audio != nil && (strings.TrimSpace(audio.Data) != "" || strings.TrimSpace(audio.Transcript) != "") {
			return true
		}
	}
	return false
}

// responsesUnsupportedDetail assembles the human-readable annotation shared
// by the three probe stacks, so node_probe_runs / model_probe_runs /
// _request_logs all tell the same story for one incident.
//
// 2026-09-29 (审计二十一轮): chatBody 是上游原始响应体片段，会进入
// model_probe_runs.error_message / node_probe_runs 等 admin 可见面（可能
// 回显凭据）——在此单一收口处过 truncateProbeBody + SanitizeErrorText，
// 与 probe_http.go 既有 errMsg 纪律（:347 truncateProbeBody）对齐。
func responsesUnsupportedDetail(origStatus int, chatStatus int, chatBody string, chatLatencyMs int, chatOK bool) string {
	verb := "failed"
	if chatOK {
		verb = "OK"
	}
	return fmt.Sprintf("responses probe rejected (HTTP %d: Responses API unsupported for this model); chat fallback probe %s (HTTP %d, %dms): %s",
		origStatus, verb, chatStatus, chatLatencyMs, truncateProbeBody(firstLine(chatBody), 300))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	return s
}
