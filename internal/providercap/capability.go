package providercap

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

type Descriptor struct {
	Protocol               string
	CatalogCode            string
	SupportsModelsEndpoint bool
	ModelListSource        string
	ChatProbeEndpoint      upstreamurl.Endpoint
	AuthStyle              string
	SupportsBalanceProbe   bool
	// BalanceEndpoint is the path appended to base_url to fetch account balance.
	// Empty string means this vendor is not supported.
	BalanceEndpoint string
	// BalanceJSONPath is a dot-separated key path to the USD balance value in
	// the response JSON.  e.g. "total_available" or "balance_infos.0.total_balance"
	BalanceJSONPath string
}

// ResponsesProbeMaxOutputTokens is the max_output_tokens carried by
// Responses-API probes (admin session-ping, provider health check, bg chat
// ping). The Responses API enforces a floor of 16 — vapeur answered
// "Invalid 'max_output_tokens': integer below minimum value. Expected >= 16"
// to a 5-token probe (measured 2026-09-25) — and a reasoning model can burn
// the entire budget invisibly, so 32 keeps the ping cheap while leaving a
// compliant provider room to actually emit a minimal message.
const ResponsesProbeMaxOutputTokens = 32

// ProbePingMaxTokens is the max_tokens floor for ALL text probe ping bodies,
// chat leg included. 2026-10-13（Vapeur deepseek-v4-pro 探针三连败事故）：
// 聚合中转把 chat ping 在内部转成 Responses 形态转发，chat 腿的
// max_tokens=10 撞上同一个 ≥16 下限（"Invalid 'max_output_tokens': must
// be greater than or equal to 16, got 10"）→ 探针 3 连败 → 节点停 5 分钟
// → 用户流量 db_empty 连坐。两条腿同一个中转、同一个下限，统一取 32。
//
// ★★ 2026-10-27（R152 轮 · D9b）：**32 对推理模型必然不足** ⇒ 探针拿到的
// 响应里**没有 assistant 正文** ⇒ 判定 `probe_response_inconclusive`
// ⇒ ★★ **故意不写回** ⇒ 状态永不改变 ⇒ ★★★ **永久 broken_confirmed**
// ⇒ ⇒ ★★ **「超时判死 = 入口、内容预算不足 = 出不来」⇒ 两者组合 = 不可逆封禁**。
//
// ★★ R151 轮的 2×2 正交消融（本地实测）：
//
//	臂 A（只放宽超时 30 s → 180 s）⇒ **无效**：仍 inconclusive、仍不写回；
//	臂 C（超时 180 s **+** 内容预算 32 → **1024**）⇒ ★★ **判定翻转**
//	    （last_direct_ok=true / consecutive_failures=0 / state=healthy_confirmed）
//	    ⇒ Goal 点名的 glm-5.3-flash 随即 10/10 全成功。
//
// ⇒ ⇒ ★★ **两个门槛必须一起放宽**（只放宽一个无效；只放宽内容预算救不了已被判死的对）。
//
// ⚠ 为什么是 **1024** 而不是更大：
// ① 探针**必须**拿到 assistant 正文才可能判定成功 ⇒ 预算得容得下模型的
//
//	推理开销**加上**一段极短的回答；
//
// ② R151 实测：臂 C 的 1024 让探针判定翻转 ⇒ **1024 对探针这一档够用**；
// ③ ★★ 但**用户请求**那一档仍可能不够 —— 实测 `max_tokens=1024` 的用户请求
//
//	仍被推理吃光（completion_tokens == reasoning_tokens == 1024、finish_reason=length）
//	⇒ ⇒ ★★ **同一个数字在「探针」与「用户请求」两处的含义不同**：
//	探针要的是「模型有没有正常应答」，用户请求要的是「正文长度」；
//	⇒ ⇒ ★★ 用户请求那一档属于**另一件事**（R150 §6.3 P2/D10），不要混为一谈。
//
// ⚠ 成本：探针每轮最多 1024 输出 token（探针频率 = 每对每 30 s 上限）
// ⇒ ★★ 这是「用一点 token 换一个不再被永久误杀」的交换。
const ProbePingMaxTokens = 1024

// responsesUnsupportedStatuses are the HTTP statuses under which a
// /v1/responses rejection can carry the relay's "this model/vendor does not
// support the Responses API" verdict. Observed in the wild (2026-09-28
// vapeur round): 400 (claude family, "该供应商不支持 Responses API" +
// code=unsupported_operation) and 502 (QWEN/DOUBAO vendor errors relayed by
// the aggregator, "X provider does not support the Responses API
// (/responses). Please use /v1/chat/completions instead."). The remaining
// statuses are the nearby contract-mismatch shapes (wrong path/method/media
// type/not implemented) that carry the same verdict on other relays.
var responsesUnsupportedStatuses = map[int]bool{
	400: true, 404: true, 405: true, 415: true, 422: true, 501: true, 502: true,
}

// ResponsesUnsupportedError reports whether a failed /v1/responses response
// is the upstream's "Responses API not supported here" verdict — a per-model
// protocol capability gap on multi-vendor relays (vapeur), NOT node death and
// NOT model death. text is the response body (or any extracted message text);
// matching is a case-insensitive substring pass:
//
//   - the text must mention the Responses API itself, AND
//   - carry a negation ("does not support" / "unsupported" / CJK 不支持), OR
//   - explicitly redirect to /v1/chat/completions.
//
// A compliant Responses API rejecting parameters ("Invalid
// 'max_output_tokens'", "Unsupported parameter: 'messages'") mentions the API
// name but carries no negation of the API itself, so it is NOT matched —
// those are probe-shape bugs, not capability gaps. Callers use this to fall
// back to a chat-completions probe instead of poisoning availability.
func ResponsesUnsupportedError(httpStatus int, text string) bool {
	if !responsesUnsupportedStatuses[httpStatus] {
		return false
	}
	b := strings.ToLower(strings.TrimSpace(text))
	if b == "" {
		return false
	}
	if !strings.Contains(b, "responses api") {
		return false
	}
	// Parameter-shaped rejections ("Unsupported parameter: 'messages'", "The
	// Responses API does not support the 'messages' parameter", CJK "不支持该
	// 参数") are probe-shape bugs, not capability gaps — a green fallback chat
	// ping would silently mask the shape bug. The guard applies to the
	// "not support"/"不支持" branch too (2026-09-28 十九轮审计收口：此前只拦
	// 裸 "unsupported"，"does not support the 'messages' parameter" 会被误判
	// 为能力缺口，多打一发 chat 探针并写下误导性注记)。The chat-completions
	// redirect below stays as the final override: genuine relay verdicts
	// overwhelmingly carry it, with or without the word "parameter".
	paramShaped := strings.Contains(b, "parameter") || strings.Contains(b, "参数") ||
		strings.Contains(b, "argument") ||
		strings.Contains(b, "unsupported value") ||
		strings.Contains(b, "unsupported field") ||
		strings.Contains(b, "unsupported request")
	if !paramShaped &&
		(strings.Contains(b, "not support") || strings.Contains(b, "不支持")) {
		return true
	}
	if strings.Contains(b, "unsupported") && !paramShaped {
		return true
	}
	return strings.Contains(b, "/v1/chat/completions") || strings.Contains(b, "chat/completions")
}

func Resolve(protocol, catalogCode string) Descriptor {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	catalogCode = strings.ToLower(strings.TrimSpace(catalogCode))

	d := Descriptor{
		Protocol:               protocol,
		CatalogCode:            catalogCode,
		SupportsModelsEndpoint: true,
		ModelListSource:        "api",
		ChatProbeEndpoint:      upstreamurl.EpChatCompletions,
		AuthStyle:              "bearer",
	}

	switch protocol {
	case "anthropic-messages":
		// v5 (2026-06-20): Anthropic /v1/models is available since 2024 and
		// returns 200 + model list free of charge. Enable model-list probing
		// alongside chat probing so Layer 1 can validate without burning tokens.
		d.SupportsModelsEndpoint = true
		d.ModelListSource = "api"
		d.ChatProbeEndpoint = upstreamurl.EpMessages
		d.AuthStyle = "anthropic"
	case "openai-responses":
		// 2026-09-25 vapeur/hxt-local gpt-5.6-terra 事故：openai-responses
		// 供应商的探针必须走其原生 /v1/responses——这类中转往往对 chat
		// max_tokens=1 直接 400（"Could not finish the message because
		// max_tokens or model output limit was reached"），且 responses 本来
		// 就是该协议供应商的默认出站形态（协议归一见
		// providercatalog.NormalizeProviderProtocol）。
		d.ChatProbeEndpoint = upstreamurl.EpResponses
	}

	// P3 (2026-06-19): per-vendor balance probe configuration.
	switch catalogCode {
	case "openai":
		d.SupportsBalanceProbe = true
		d.BalanceEndpoint = "/dashboard/billing/credit_grants"
		d.BalanceJSONPath = "total_available"
	case "deepseek":
		d.SupportsBalanceProbe = true
		d.BalanceEndpoint = "/user/balance"
		d.BalanceJSONPath = "balance_infos.0.total_balance"
	case "siliconflow":
		d.SupportsBalanceProbe = true
		d.BalanceEndpoint = "/user/info"
		d.BalanceJSONPath = "data.balance"
	case "openrouter":
		d.SupportsBalanceProbe = false
	}

	return d
}

func ApplyAuthHeaders(req *http.Request, desc Descriptor, apiKey string) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return
	}
	switch desc.AuthStyle {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

func ModelsURLCandidates(baseURL string, template *string, desc Descriptor) []string {
	if !desc.SupportsModelsEndpoint {
		return nil
	}
	if template != nil {
		tpl := strings.TrimSpace(*template)
		if tpl == "" {
			return nil
		}
		if strings.HasPrefix(tpl, "http://") || strings.HasPrefix(tpl, "https://") {
			return []string{tpl}
		}
		base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		return []string{base + tpl}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	add(upstreamurl.ModelsURL(baseURL))
	for _, u := range upstreamurl.ModelsURLCandidates(baseURL) {
		add(u)
	}
	return out
}

func ProbeEndpointURL(baseURL string, desc Descriptor) string {
	return upstreamurl.Build(baseURL, desc.ChatProbeEndpoint)
}

// BalanceURL builds the full URL for fetching account balance.
// Returns "" if SupportsBalanceProbe is false or BalanceEndpoint is empty.
func BalanceURL(baseURL string, desc Descriptor) string {
	if !desc.SupportsBalanceProbe || desc.BalanceEndpoint == "" {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return base + desc.BalanceEndpoint
}

// ExtractJSONPath walks a dot-separated path ("total_available",
// "balance_infos.0.total_balance") through already-decoded JSON and returns
// the terminal value as a float64. Numeric JSON strings ("10.73") are
// accepted — DeepSeek returns string amounts, and rejecting them silently
// zeroes the balance display. Returns ok=false for missing/malformed paths.
func ExtractJSONPath(parsed any, path string) (float64, bool) {
	cur := parsed
	for _, p := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[p]
		case []any:
			idx := 0
			//nolint:errcheck // best-effort parse, non-critical
			fmt.Sscanf(p, "%d", &idx)
			if idx >= len(v) {
				return 0, false
			}
			cur = v[idx]
		default:
			return 0, false
		}
		if cur == nil {
			return 0, false
		}
	}
	switch v := cur.(type) {
	case float64:
		return v, true
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// FetchBalanceUSD GETs a vendor balance endpoint and extracts the USD value
// via desc.BalanceJSONPath. Shared by credential_probe_v2 (routine display
// refresh) and bg/balance_floor_guard (floor re-check for pulled credentials).
// Network/parse failures return (0, false) — callers treat balance as unknown
// and never act on stale numbers.
func FetchBalanceUSD(ctx context.Context, client *http.Client, url, apiKey string, desc Descriptor) (float64, bool) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	ApplyAuthHeaders(req, desc, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return 0, false
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false
	}
	return ExtractJSONPath(parsed, desc.BalanceJSONPath)
}
