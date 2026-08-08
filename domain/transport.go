package domain

import (
	"encoding/json"
	"net/http"
)

// TransportContext 封装网络中转领域的所有上下文。
type TransportContext struct {
	W         http.ResponseWriter
	R         *http.Request
	BodyBytes []byte
	IsStream  bool

	ClientProtocol   string
	UpstreamProtocol string

	ClientModel   string
	OutboundModel string

	// ClientCatalogCode is the catalog code of the client's entry provider,
	// if known. Used by IR transport to determine same-provider extension
	// restoration. Empty when the client entry point is unknown or when
	// the executor has not yet resolved a candidate.
	ClientCatalogCode string

	// UpstreamCatalogCode is the catalog code of the chosen upstream
	// candidate. Set by the executor after candidate selection.
	UpstreamCatalogCode string

	// ProviderID is the provider_id of the chosen upstream candidate.
	// Added 2026-08-09 to support per-provider circuit breaker isolation
	// in TransportIRConverter (previously a single process-wide breaker
	// caused one unstable provider to take down all IR conversions).
	// Set by the executor after candidate selection, alongside
	// UpstreamCatalogCode.
	ProviderID int

	Transform      *TransformResult
	ToolsRequested bool

	Extensions ExtensionsBag
}

// ExtensionsBag 存储客户端非标参数，用于协议转换时无损往返。
//
// IR 和 Legacy 共用此结构。
type ExtensionsBag struct {
	ClientRaw map[string]json.RawMessage `json:"client_raw,omitempty"`
	Headers   map[string]string          `json:"headers,omitempty"`
	Custom    map[string]any             `json:"custom,omitempty"`
}

// IsZero 报告 ExtensionsBag 是否为空。
func (b ExtensionsBag) IsZero() bool {
	return len(b.ClientRaw) == 0 && len(b.Headers) == 0 && len(b.Custom) == 0
}

// TransformResult 是协议转换的简化版结果。
type TransformResult struct {
	EgressPreference  []string
	StripHeaders      []string
	InjectHeaders     map[string]string
	DisguiseProfileID string
}

// TransportResponseContext 响应上下文。
type TransportResponseContext struct {
	StatusCode      int
	Headers         http.Header
	BodyBytes       []byte
	StreamCompleted bool
}
