package providercap

import (
	"net/http"
	"strings"

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
