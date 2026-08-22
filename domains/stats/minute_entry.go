package stats

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/maas"
)

const unknownDim = "__unknown__"

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

	tenantID := entry.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	bucket := ts.UTC().Truncate(time.Minute)

	prompt := int64(derefInt(entry.PromptTokens))
	completion := int64(derefInt(entry.CompletionTokens))
	cacheRead := int64(derefInt(entry.CacheReadTokens))
	cacheWrite := int64(derefInt(entry.CacheWriteTokens))
	totalTokens := prompt + completion + cacheRead + cacheWrite
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
