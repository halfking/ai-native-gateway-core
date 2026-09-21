// Package recentmodels provides the tenant-scoped seven-day model ranking
// shared by routing model selection and scheduled credential self-checks.
package recentmodels

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyPrefix = "llmgw:routing:recently_used_models:"
	TTL       = 7 * 24 * time.Hour
)

type Entry struct {
	Model string
	Count int
}

func Key(tenantID string) string {
	if tenantID = strings.TrimSpace(tenantID); tenantID == "" {
		return ""
	}
	return keyPrefix + tenantID
}

func Normalize(model string) string {
	return strings.ToLower(strings.Join(strings.Fields(model), ""))
}

// Record ignores probes and Redis errors so the business request path remains
// best-effort and health traffic cannot affect the shared ranking.
func Record(ctx context.Context, client *redis.Client, tenantID, model string, isProbe bool) {
	if client == nil || isProbe {
		return
	}
	key, model := Key(tenantID), Normalize(model)
	if key == "" || model == "" || model == "unknown" {
		return
	}
	pipe := client.Pipeline()
	pipe.ZIncrBy(ctx, key, 1, model)
	pipe.Expire(ctx, key, TTL)
	_, _ = pipe.Exec(ctx)
}

// Read returns nil when Redis is unavailable; callers then use their database
// fallback without making Redis a correctness dependency.
func Read(ctx context.Context, client *redis.Client, tenantID string, limit int) []Entry {
	if client == nil || limit <= 0 {
		return nil
	}
	key := Key(tenantID)
	if key == "" {
		return nil
	}
	pairs, err := client.ZRevRangeWithScores(ctx, key, 0, int64(limit-1)).Result()
	if err != nil {
		return nil
	}
	entries := make([]Entry, 0, len(pairs))
	for _, pair := range pairs {
		model, _ := pair.Member.(string)
		if model = strings.TrimSpace(model); model != "" {
			entries = append(entries, Entry{Model: model, Count: int(pair.Score)})
		}
	}
	return entries
}
