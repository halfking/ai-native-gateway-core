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
// responses 失败结论并附注 fallback 也失败。max_tokens=10：max_tokens=1
// 会在推理模型上 400（"Could not finish the message ..."，2026-09-25 已测），
// 10 已实测 5 个目标模型全绿。
package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// responsesFallbackChatMaxTokens 是 chat 降级探针的 max_tokens。推理模型
// （claude-opus-5 等）1 个 token 连 reasoning 都放不下 → 上游 400；
// 10 已实测全绿（2026-09-28）。与 node_probe.directProbeBody 的 chat 形态
// 同值。
const responsesFallbackChatMaxTokens = 10

// responsesChatFallbackPing fires ONE chat-completions probe against
// base+"/chat/completions" — the fallback leg used by every probe path when
// the /v1/responses probe comes back with the relay's "Responses API not
// supported" verdict. Returns (httpStatus, bodyPreview, latencyMs, ok).
// A network failure yields (0, reason, latency, false).
func responsesChatFallbackPing(ctx context.Context, client *http.Client, apiKey, baseURL, model string) (int, string, int, bool) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := upstreamurl.Build(strings.TrimRight(baseURL, "/"), upstreamurl.EpChatCompletions)
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": responsesFallbackChatMaxTokens,
	})
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return 0, fmt.Sprintf("build chat fallback request: %s", err.Error()), int(time.Since(start).Milliseconds()), false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return 0, fmt.Sprintf("chat fallback unreachable: %s", err.Error()), latency, false
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return resp.StatusCode, string(respBody), latency, ok
}

// responsesUnsupportedDetail assembles the human-readable annotation shared
// by the three probe stacks, so node_probe_runs / model_probe_runs /
// request_logs all tell the same story for one incident.
func responsesUnsupportedDetail(origStatus int, chatStatus int, chatBody string, chatLatencyMs int, chatOK bool) string {
	verb := "failed"
	if chatOK {
		verb = "OK"
	}
	return fmt.Sprintf("responses probe rejected (HTTP %d: Responses API unsupported for this model); chat fallback probe %s (HTTP %d, %dms): %s",
		origStatus, verb, chatStatus, chatLatencyMs, firstLine(chatBody))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	return s
}
