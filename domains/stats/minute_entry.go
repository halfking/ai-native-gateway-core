package stats

import (
	"slices"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/maas"
)

const unknownDim = "__unknown__"

// probeGatewayActors mirrors bg.ProbeTrafficExclusionPredicateView's
// origin_actor arm. It lives here rather than importing bg because bg imports
// this package (bg/stats_minute_rollup.go) — a shared const in bg would be an
// import cycle. TestProbeActorSetMatchesSQLPredicate in this package fails if
// the two lists ever drift.
var probeGatewayActors = []string{
	"node-probe-worker",
	"active-probe-worker",
	"probe-service",
	"credential-selfcheck-worker",
}

// IsProbeTraffic reports whether a completed request_log entry is probe traffic
// and must therefore be kept out of the dashboard's request/success counters.
//
// This is the live (Go) twin of bg.ProbeTrafficExclusionPredicateView and must
// stay semantically identical to it. The SQL arm works on the 113-column view,
// which has no origin_stage column, so it identifies probes via the three
// remaining markers; the entry here carries origin_stage too, but we deliberately
// do NOT add an origin_stage arm — that would make the live and rollup writers
// disagree about which rows are business traffic, and the two writers feed the
// same ON CONFLICT key in request_stats_minute.
func IsProbeTraffic(entry *telemetry.RequestLogEntry) bool {
	if entry == nil {
		return false
	}
	if slices.Contains(entry.QualityFlags, "probe") {
		return true
	}
	if task := strings.TrimSpace(derefString(entry.TaskType)); task == "probe_triggered" {
		return true
	}
	if actor := strings.TrimSpace(derefString(entry.OriginActor)); actor != "" &&
		slices.Contains(probeGatewayActors, actor) {
		return true
	}
	return false
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// MinuteRow is a per-minute aggregate keyed by tenant + provider + model.
type MinuteRow struct {
	Bucket           time.Time
	TenantID         string
	ProviderID       int64
	CanonicalID      int64
	Requests         int64
	SuccessCount     int64
	FailureCount     int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CreditsCharged   int64
	CostUSD          float64
	LatencyMsSum     int64
}

// DimRow is a per-minute breakdown for pie charts.
type DimRow struct {
	Bucket         time.Time
	TenantID       string
	DimType        string
	DimKey         string
	Requests       int64
	SuccessCount   int64
	FailureCount   int64
	TotalTokens    int64
	CreditsCharged int64
	CostUSD        float64
}

// ErrorDrillRow supports error pie chart drill-down.
type ErrorDrillRow struct {
	Bucket        time.Time
	TenantID      string
	ErrorKind     string
	ModelName     string
	ProviderID    int64
	ClientProfile string
	Requests      int64
}

// FromTelemetryEntry builds rollup rows from a completed request log entry.
func FromTelemetryEntry(entry *telemetry.RequestLogEntry, ts time.Time) (
	main MinuteRow,
	dims []DimRow,
	drills []ErrorDrillRow,
	ok bool,
) {
	if entry == nil {
		return main, nil, nil, false
	}
	if entry.Op == telemetry.RequestLogInsert {
		return main, nil, nil, false
	}
	status := ""
	if entry.RequestStatus != nil {
		status = *entry.RequestStatus
	}
	if status == telemetry.RequestStatusInProgress {
		return main, nil, nil, false
	}
	if status != telemetry.RequestStatusSuccess &&
		status != telemetry.RequestStatusFailure &&
		status != telemetry.RequestStatusRateLimited {
		return main, nil, nil, false
	}
	// Probe traffic is not user traffic. Counting it here is what made the
	// dashboard's 总请求数 include health checks and the 成功率 read as a
	// meaningless number, because probes fail far more often than real calls.
	if IsProbeTraffic(entry) {
		return main, nil, nil, false
	}

	tenantID := entry.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	bucket := ts.UTC().Truncate(time.Minute)

	prompt := int64(derefInt(entry.PromptTokens))
	completion := int64(derefInt(entry.CompletionTokens))
	// total_tokens 口径与 request_logs / usage_facts（event.go）对齐：
	// prompt + completion，cache 等细分项是子集，**不另加**。
	// R48-F1（2026-10-06）：此前这里 +cacheRead+cacheWrite，是 event.go
	// 同族的双重计——usage_facts 回填 cache 数据后分钟表/看板缓存会虚高。
	totalTokens := prompt + completion
	credits := creditsFromEntry(entry)
	cost := derefFloat(entry.CostUSD)
	latency := int64(derefInt(entry.LatencyMs))

	providerID := int64(derefInt(entry.ProviderID))
	canonicalID := int64(derefInt(entry.CanonicalID))

	main = MinuteRow{
		Bucket:           bucket,
		TenantID:         tenantID,
		ProviderID:       providerID,
		CanonicalID:      canonicalID,
		Requests:         1,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      totalTokens,
		CreditsCharged:   credits,
		CostUSD:          cost,
		LatencyMsSum:     latency,
	}
	if status == telemetry.RequestStatusSuccess {
		main.SuccessCount = 1
	} else if status == telemetry.RequestStatusFailure {
		main.FailureCount = 1
	}

	addDim := func(dimType, key string) {
		dims = append(dims, DimRow{
			Bucket:         bucket,
			TenantID:       tenantID,
			DimType:        dimType,
			DimKey:         key,
			Requests:       1,
			SuccessCount:   main.SuccessCount,
			FailureCount:   main.FailureCount,
			TotalTokens:    totalTokens,
			CreditsCharged: credits,
			CostUSD:        cost,
		})
	}

	clientProfile := unknownDim
	if entry.ClientProfile != nil && *entry.ClientProfile != "" {
		clientProfile = *entry.ClientProfile
	}
	addDim("client_profile", clientProfile)

	// R57 B7: real resolved client IP (origin middleware trust-list chain).
	// 背景合成流量（is_auto_request 等内部路径）无 origin 中间件 →
	// ClientIP nil → 哨兵。与 bg rollupDims 的 HOST(client_ip) 维度同一
	// 真源，看板 client_ips 饼图据此做内网直显/GeoIP 归类。
	clientIP := unknownDim
	if entry.ClientIP != nil && *entry.ClientIP != "" {
		clientIP = *entry.ClientIP
	}
	addDim("client_ip", clientIP)

	identityHash := unknownDim
	if entry.IdentityHash != nil && *entry.IdentityHash != "" {
		identityHash = *entry.IdentityHash
	}
	addDim("identity_hash", identityHash)

	modelName := unknownDim
	if entry.OutboundModel != nil && *entry.OutboundModel != "" {
		modelName = *entry.OutboundModel
	} else if entry.ClientModel != nil && *entry.ClientModel != "" {
		modelName = *entry.ClientModel
	}
	addDim("model", modelName)

	addDim("tenant", tenantID)
	addDim("provider", itoa(providerID))

	if status == telemetry.RequestStatusFailure {
		errKind := unknownDim
		if entry.ErrorKind != nil && *entry.ErrorKind != "" {
			errKind = *entry.ErrorKind
		}
		addDim("error_kind", errKind)
		drills = append(drills, ErrorDrillRow{
			Bucket:        bucket,
			TenantID:      tenantID,
			ErrorKind:     errKind,
			ModelName:     modelName,
			ProviderID:    providerID,
			ClientProfile: clientProfile,
			Requests:      1,
		})
	}

	return main, dims, drills, true
}

func creditsFromEntry(entry *telemetry.RequestLogEntry) int64 {
	if entry.CreditsCharged != nil {
		return *entry.CreditsCharged
	}
	rates := maas.ModelRateValues{In: 10000, Out: 10000, CacheIn: 10000, CacheOut: 10000}
	return maas.CalcCreditsMultimodal(maas.TokenUsage{
		PromptTokens:     derefInt(entry.PromptTokens),
		CompletionTokens: derefInt(entry.CompletionTokens),
		CacheReadTokens:  derefInt(entry.CacheReadTokens),
		CacheWriteTokens: derefInt(entry.CacheWriteTokens),
		ImageTokens:      derefInt(entry.ImageTokens),
		AudioTokens:      derefInt(entry.AudioTokens),
		VideoTokens:      derefInt(entry.VideoTokens),
	}, rates)
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
