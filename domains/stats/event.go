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
		// A request has one terminal fact. Keep the id independent of the
		// outcome so a late correction cannot create a second terminal fact.
		EventID: eventID(entry.RequestID, "request_terminal", 0), OccurredAt: occurred,
		RequestID: entry.RequestID, EventType: typeOf, Traffic: trafficClass(entry),
		TenantID: tenant, ProviderID: entry.ProviderID, CredentialID: entry.CredentialID,
		CanonicalID: entry.CanonicalID, RawModelName: rawModel, APIKeyID: entry.APIKeyID,
		ApplicationID: entry.ApplicationID, EndUserID: valueString(entry.EndUserID),
		PersonHash:    personHash(tenant, valueString(entry.APIKeyOwnerUser), valueString(entry.EndUserID)),
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
	// total_tokens 的口径：prompt + completion，**不加** cache / reasoning /
	// image / audio / video 那些「细分项」。
	//
	// ★ 2026-10-06 修正（实测，不是推理）。原来这里是把七个 token 列全加起来：
	//
	//	TotalTokens = Prompt+Completion+CacheRead+CacheWrite+Reasoning+Image+Audio+Video
	//
	// 那是**重复计**，而且会一路流进看板。三个环节，每一环都放大后果：
	//
	//  1) 语义上它们是**子集**。上游把 usage 拆成两个层级：
	//     `prompt_tokens`（已含缓存与图像）与 `prompt_tokens_details.{cached,
	//      image,audio,video}_tokens`（它的**细分**）；`completion_tokens` 与
	//      `completion_tokens_details.reasoning_tokens` 同理。成本公式
	//      calcCostWithConvention 也是这么用的：它把 cache_read 当作 prompt 的
	//      子集（先按原价 `promptCost -= cacheRead*priceIn` 减掉，再按缓存价
	//      `+= cacheRead*cachePrice` 入账）⇒ cache 绝不能再加一次。
	//  2) 与另两张表口径不符（2026-10-06 真库读数，30 天只读）：
	//       - `request_logs.total_tokens`（取自上游 usage.total_tokens）：
	//         近 30 天 **1,492,562 行全部**严格等于 prompt+completion，
	//         有无 cache 读都一样；而 cache_read 平均占总 token 的 **42.5%**
	//         （87,888 行有 cache 读，avg 12,218.9）⇒ 它确实没被另加。
	//       - `stats_usage_daily.total_tokens`：`daily_monthly_rollup.go:207`
	//         是 `SUM(total_tokens)`，逐日核对同样等于 prompt+completion。
	//     ⇒ 全仓既有口径就是「子集不另加」，只有这一行是异类。
	//  3) 后果面。`daily_monthly_rollup.go:197` 的日聚合 **FROM usage_facts f**，
	//     月聚合（`:114`）再从 stats_usage_daily 取 ⇒ 这一行的口径直接决定
	//     **运营在看板和月报上看到的总量**，不是只脏一张明细表。
	//
	// 为什么今天没炸：usage_facts 目前的 cache/reasoning/image… 全为 0，
	// 两种公式结果相同（真库 246 行验证：total 一律 = prompt+completion）。
	// ⇒ 这是一次**前向修正**，对存量读数零变化，改动当下也**测不出差异**。
	// 别因此以为它不重要：回填 48 天 usage_facts 之后（那批有大量 cache 读，
	// 87,888 行 / 30 天），看板总量会相对 request_logs 虚高 42%。
	//
	// ⚠ 口径边界：上游 usage 若真的报了一个与 prompt+completion 不同的
	// total_tokens，这里**不会**跟随（Event 里没有承载它的列；`provider_tokens`
	// 不是它 —— 那是 Doubao Seed 的 `seed_token_usage`，见 usage.go:152-159，
	// 真库 246 行恒为 0，**不可当参照系**）。要跟随上游总量需要加一列，那是
	// 另一件事；本行的契约是「与 request_logs 同口径」。
	e.TotalTokens = e.PromptTokens + e.CompletionTokens
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

func personHash(tenant, owner, endUser string) string {
	tenant = strings.TrimSpace(tenant)
	value := strings.TrimSpace(endUser)
	if value == "" {
		value = strings.TrimSpace(owner)
	}
	if value == "" || tenant == "" {
		return ""
	}

	// Length-prefix the fields so tenant="a", value="b:c" cannot collide
	// with tenant="a:b", value="c" before the SHA-256 is computed.
	input := fmt.Sprintf("v2:%d:%s%d:%s", len(tenant), tenant, len(value), value)
	sum := sha256.Sum256([]byte(input))
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
