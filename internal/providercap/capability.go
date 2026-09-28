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
	if strings.Contains(b, "not support") || strings.Contains(b, "不支持") {
		return true
	}
	// Bare "unsupported" is ambiguous: it most often names a rejected
	// parameter ("Unsupported parameter: 'messages'. In the Responses API,
	// this parameter has moved to 'input'.") — a probe-shape bug, not a
	// capability gap. Only treat it as the capability verdict when it is not
	// attached to a parameter-ish noun.
	if strings.Contains(b, "unsupported") &&
		!strings.Contains(b, "unsupported parameter") &&
		!strings.Contains(b, "unsupported argument") &&
		!strings.Contains(b, "unsupported value") &&
		!strings.Contains(b, "unsupported field") &&
		!strings.Contains(b, "unsupported request") {
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
