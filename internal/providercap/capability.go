package providercap

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

type Descriptor struct {
	Protocol              string
	CatalogCode           string
	SupportsModelsEndpoint bool
	ModelListSource       string
	ChatProbeEndpoint     upstreamurl.Endpoint
	AuthStyle             string
	SupportsBalanceProbe  bool
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
		d.SupportsModelsEndpoint = false
		d.ModelListSource = "manifest"
		d.ChatProbeEndpoint = upstreamurl.EpMessages
		d.AuthStyle = "anthropic"
	}

	switch catalogCode {
	case "openrouter":
		// OpenRouter exposes account credit APIs, but current gateway does not
		// manage provider-specific billing credentials for safe probing yet.
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
