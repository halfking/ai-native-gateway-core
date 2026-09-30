// Package sanitize - stats_builder.go
//
// Phase 2 Task 2.2: Build SanitizeStats from SanitizeResult
package sanitize

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// BuildSanitizeStats constructs compression.SanitizeStats from a SanitizeResult.
//
// Deprecated: the production path does not use this. The HTTP middleware merges
// the placeholder map across messages and only has the map, so the live
// implementation is compression.BuildSanitizeInfo (see
// smart_sani_guard.go buildSanitizeInfoForSession). Keeping a second,
// never-called copy of the same bucketing logic here is how the two drifted
// apart. Kept for API compatibility; new callers should build SanitizeInfo.
func BuildSanitizeStats(result *SanitizeResult) compression.SanitizeStats {
	if result == nil || len(result.Fragments) == 0 {
		return compression.SanitizeStats{}
	}

	stats := compression.SanitizeStats{
		PlaceholderCount: len(result.SanitizeMap),
		SanitizedAt:      time.Now().Unix(),
	}

	// Count by type
	for _, fragment := range result.Fragments {
		switch fragment.Type {
		case TypePhone:
			stats.PhoneCount++
		case TypeEmail:
			stats.EmailCount++
		case TypeIDCard:
			stats.IDCardCount++
		case TypeCreditCard:
			stats.CreditCardCount++
		case TypeSecret:
			stats.SecretCount++
		default:
			stats.OtherCount++
		}
	}

	return stats
}
