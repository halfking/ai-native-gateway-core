package stats

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// EventType is the stable vocabulary used by the statistics projections.
// Keep request and attempt outcomes separate: one request may have many
// attempts, but only one terminal request outcome.
type EventType string

const (
	EventRequestSucceeded EventType = "request_succeeded"
	EventRequestFailed    EventType = "request_failed"
	EventRequestRateLimit EventType = "request_rate_limited"
	EventUsageCorrected   EventType = "usage_corrected"
)

type TrafficClass string

const (
	TrafficBusiness     TrafficClass = "business"
	TrafficProbe        TrafficClass = "probe"
	TrafficSelfCheck    TrafficClass = "self_check"
	TrafficSystemHealth TrafficClass = "system_health"
	TrafficAdminTest    TrafficClass = "admin_test"
	TrafficUnknown      TrafficClass = "unknown"
)

// Event is deliberately narrower than telemetry.RequestLogEntry. It is safe
// to persist in an analytics inbox because it contains no prompt or response.
type Event struct {
	EventID    string       `json:"event_id"`
	OccurredAt time.Time    `json:"occurred_at"`
	RequestID  string       `json:"request_id"`
	EventType  EventType    `json:"event_type"`
	Traffic    TrafficClass `json:"traffic_class"`
	AttemptNo  int          `json:"attempt_no,omitempty"`

	TenantID      string `json:"tenant_id"`
	ProviderID    *int   `json:"provider_id,omitempty"`
	CredentialID  *int   `json:"credential_id,omitempty"`
	CanonicalID   *int   `json:"canonical_id,omitempty"`
	RawModelName  string `json:"raw_model_name,omitempty"`
	APIKeyID      *int   `json:"api_key_id,omitempty"`
	ApplicationID *int   `json:"application_id,omitempty"`
	EndUserID     string `json:"end_user_id,omitempty"`
	PersonHash    string `json:"person_hash,omitempty"`
	ClientProfile string `json:"client_profile,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	VirtualClient string `json:"virtual_client_id,omitempty"`
	IdentityHash  string `json:"identity_hash,omitempty"`

	Status           string `json:"status"`
	ErrorKind        string `json:"error_kind,omitempty"`
	ErrorClass       string `json:"error_class,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	FailureStage     string `json:"failure_stage,omitempty"`
	AttributionOwner string `json:"attribution_owner,omitempty"`
	HTTPStatus       *int   `json:"http_status,omitempty"`
	Retryable        bool   `json:"retryable,omitempty"`

	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	ImageTokens      int64   `json:"image_tokens"`
	AudioTokens      int64   `json:"audio_tokens"`
	VideoTokens      int64   `json:"video_tokens"`
	ProviderTokens   int64   `json:"provider_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	CostCurrency     string  `json:"cost_currency,omitempty"`
	CreditsCharged   int64   `json:"credits_charged"`
	UsageSource      string  `json:"usage_source,omitempty"`
	PricingVersion   string  `json:"pricing_version,omitempty"`
	LatencyMs        int64   `json:"latency_ms"`
	TTFTMs           int64   `json:"ttft_ms"`

	Source         string `json:"source"`
	PayloadVersion int    `json:"payload_version"`
}

func (e Event) Valid() error {
	if e.EventID == "" || e.RequestID == "" {
		return fmt.Errorf("stats: event_id and request_id are required")
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("stats: occurred_at is required")
	}
	if e.TenantID == "" {
		return fmt.Errorf("stats: tenant_id is required")
	}
	if e.EventType == "" {
		return fmt.Errorf("stats: event_type is required")
	}
	return nil
}

// EventFromTelemetry converts only terminal request updates. Inserts and
// in-progress updates are intentionally not counted as billable outcomes.
func EventFromTelemetry(entry *telemetry.RequestLogEntry, now time.Time) (Event, bool) {
	if entry == nil || entry.RequestID == "" || entry.Op == telemetry.RequestLogInsert {
		return Event{}, false
	}
	status := valueString(entry.RequestStatus)
	if status != telemetry.RequestStatusSuccess && status != telemetry.RequestStatusFailure && status != telemetry.RequestStatusRateLimited {
		return Event{}, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	occurred := now.UTC()
	if entry.EventAt != nil && !entry.EventAt.IsZero() {
		occurred = entry.EventAt.UTC()
	}
	tenant := entry.TenantID
	if tenant == "" {
		tenant = "default"
	}
	typeOf := EventRequestFailed
	if status == telemetry.RequestStatusSuccess {
		typeOf = EventRequestSucceeded
	} else if status == telemetry.RequestStatusRateLimited {
		typeOf = EventRequestRateLimit
	}
	rawModel := valueString(entry.OutboundModel)
	if rawModel == "" {
		rawModel = valueString(entry.ClientModel)
	}
	e := Event{
		EventID: eventID(entry.RequestID, string(typeOf), 0), OccurredAt: occurred,
		RequestID: entry.RequestID, EventType: typeOf, Traffic: trafficClass(entry),
		TenantID: tenant, ProviderID: entry.ProviderID, CredentialID: entry.CredentialID,
		CanonicalID: entry.CanonicalID, RawModelName: rawModel, APIKeyID: entry.APIKeyID,
		ApplicationID: entry.ApplicationID, EndUserID: valueString(entry.EndUserID),
		PersonHash:    personHash(valueString(entry.APIKeyOwnerUser), valueString(entry.EndUserID)),
		ClientProfile: valueString(entry.ClientProfile), AgentName: valueString(entry.AgentName),
		VirtualClient: valueString(entry.VirtualClientID), IdentityHash: valueString(entry.IdentityHash),
		Status: status, ErrorKind: valueString(entry.ErrorKind), FailureStage: valueString(entry.FailureStage),
		HTTPStatus: entry.UpstreamStatusCode, PromptTokens: int64(valueInt(entry.PromptTokens)),
		CompletionTokens: int64(valueInt(entry.CompletionTokens)), CacheReadTokens: int64(valueInt(entry.CacheReadTokens)),
		CacheWriteTokens: int64(valueInt(entry.CacheWriteTokens)), ReasoningTokens: int64(valueInt(entry.ReasoningTokens)),
		ImageTokens: int64(valueInt(entry.ImageTokens)), AudioTokens: int64(valueInt(entry.AudioTokens)),
		VideoTokens: int64(valueInt(entry.VideoTokens)), ProviderTokens: int64(valueInt(entry.ProviderTokens)),
		CostUSD: valueFloat(entry.CostUSD), CostCurrency: valueString(entry.CostCurrency),
		CreditsCharged: valueInt64(entry.CreditsCharged), UsageSource: valueString(entry.UsageSource),
		LatencyMs: int64(valueInt(entry.LatencyMs)), TTFTMs: int64(valueInt(entry.StreamFirstChunkMs)),
		Source: "telemetry", PayloadVersion: 1,
	}
	e.TotalTokens = e.PromptTokens + e.CompletionTokens + e.CacheReadTokens + e.CacheWriteTokens + e.ReasoningTokens + e.ImageTokens + e.AudioTokens + e.VideoTokens
	e.ErrorClass, e.AttributionOwner = classifyError(e.ErrorKind, e.HTTPStatus, status)
	return e, true
}

func eventID(requestID, eventType string, attemptNo int) string {
	return fmt.Sprintf("%s:%s:%d", requestID, eventType, attemptNo)
}

func valueString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func valueInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func valueInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func valueFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func personHash(owner, endUser string) string {
	value := strings.TrimSpace(endUser)
	if value == "" {
		value = strings.TrimSpace(owner)
	}
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func trafficClass(entry *telemetry.RequestLogEntry) TrafficClass {
	switch strings.ToLower(strings.TrimSpace(valueString(entry.OriginStage))) {
	case "probe", "probe_direct", "probe_v2", "model_probe", "passive_probe", "node_probe":
		return TrafficProbe
	case "self_check":
		return TrafficSelfCheck
	case "system_health":
		return TrafficSystemHealth
	case "manual", "admin_test":
		return TrafficAdminTest
	case "", "business":
		return TrafficBusiness
	default:
		return TrafficUnknown
	}
}

func classifyError(kind string, status *int, requestStatus string) (string, string) {
	if requestStatus == telemetry.RequestStatusRateLimited {
		return "rate_limit", "rate_limit"
	}
	k := strings.ToLower(strings.TrimSpace(kind))
	switch {
	case strings.Contains(k, "auth"), status != nil && (*status == 401 || *status == 403):
		return "auth", "auth"
	case strings.Contains(k, "timeout"), status != nil && (*status == 408 || *status == 504):
		return "timeout", "upstream"
	case strings.Contains(k, "quota"):
		return "quota", "quota"
	case strings.Contains(k, "rate"):
		return "rate_limit", "rate_limit"
	case strings.Contains(k, "network"), strings.Contains(k, "connect"):
		return "network", "network"
	case status != nil && *status >= 500:
		return "upstream", "upstream"
	case status != nil && *status >= 400:
		return "client", "client"
	case requestStatus == telemetry.RequestStatusFailure:
		return "unknown", "unknown"
	default:
		return "", ""
	}
}
