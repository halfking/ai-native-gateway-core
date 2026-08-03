package catalog

import "encoding/json"

// CatalogEntry 与 sql/objects/tables/provider_catalog.sql 的列一一对应。
// 字段顺序与 DDL 一致；JSON tag 是 catalog export 的稳定字段名。
//
// SOURCE-VERIFIED: 列定义来自 provider_catalog.sql:5-36。
// 不含 created_at/updated_at（由 DB now() 默认）；
// 不含任何 secret 字段（OAuth secret / API key 由 provider/auth 解析，见 GW-02）。
type CatalogEntry struct {
	Code                 string          `json:"code"`                   // PK-ish，UNIQUE(provider_catalog_code_key)
	Tier                 string          `json:"tier"`                   // tier1|tier2|local|restricted
	DisplayName          string          `json:"display_name"`
	DisplayNameEN        string          `json:"display_name_en,omitempty"`
	Category             string          `json:"category"`               // official|official_proxy|third_party_relay|aggregator|self_host
	Kind                 string          `json:"kind"`                   // cloud|local
	Protocol             string          `json:"protocol"`               // 见 Protocol 常量
	BaseURLTemplate      string          `json:"base_url_template"`
	DocsURL              string          `json:"docs_url,omitempty"`
	DefaultEgressProfile string          `json:"default_egress_profile"` // 默认 direct
	Domestic             bool            `json:"domestic"`
	DiscountRateDefault  float64         `json:"discount_rate_default"`  // 默认 1.0
	ModelsManifestJSON   json.RawMessage `json:"models_manifest_json"`   // 默认 '[]'
	DiscoveryStrategy    string          `json:"discovery_strategy"`     // auto|manifest|hybrid
	ModelsEndpointTpl    string          `json:"models_endpoint_template,omitempty"`
	SeedPricingPlansJSON json.RawMessage `json:"seed_pricing_plans_json,omitempty"` // 默认 '[]'，当前未填充
	PriceSourcesJSON     json.RawMessage `json:"price_sources_json,omitempty"`     // 默认 '{}'
	Hidden               bool            `json:"hidden"`
	Notes                string          `json:"notes,omitempty"`
	CatalogVersion       int             `json:"catalog_version"` // 默认 1
	HeaderProfileCode    string          `json:"header_profile_code,omitempty"`
	Capabilities         json.RawMessage `json:"capabilities,omitempty"` // 默认 '{}'
	VendorName           string          `json:"vendor_name,omitempty"`
}

// NewEntry 返回填好 DB 默认值的 CatalogEntry，调用方只需覆盖业务字段。
// 默认值对齐 provider_catalog.sql 的 DEFAULT 子句。
func NewEntry(code string) CatalogEntry {
	return CatalogEntry{
		Code:                 code,
		Category:             CategoryOfficial,
		Kind:                 KindCloud,
		DefaultEgressProfile: "direct",
		Domestic:             true,
		DiscountRateDefault:  1.0,
		ModelsManifestJSON:   json.RawMessage(`[]`),
		DiscoveryStrategy:    DiscoveryAuto,
		SeedPricingPlansJSON: json.RawMessage(`[]`),
		PriceSourcesJSON:     json.RawMessage(`{}`),
		Capabilities:         json.RawMessage(`{}`),
		CatalogVersion:       1,
	}
}
