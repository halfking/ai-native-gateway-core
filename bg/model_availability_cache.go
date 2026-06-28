package bg

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const modelAvailabilityCacheTTL = 4 * time.Hour

// ModelAvailabilityCache stores per-(credential, model) probe state in Redis
// so operators and future routing paths can query a single fast public source.
type ModelAvailabilityCache struct {
	redis *redis.Client
	ttl   time.Duration
}

func NewModelAvailabilityCache(redisClient *redis.Client, ttl time.Duration) *ModelAvailabilityCache {
	if redisClient == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = modelAvailabilityCacheTTL
	}
	return &ModelAvailabilityCache{redis: redisClient, ttl: ttl}
}

func (c *ModelAvailabilityCache) Enabled() bool {
	return c != nil && c.redis != nil
}

func (c *ModelAvailabilityCache) Set(ctx context.Context, credentialID int, rawModel string, fields map[string]any) error {
	if !c.Enabled() {
		return nil
	}
	key := c.key(credentialID, rawModel)
	pipe := c.redis.Pipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, c.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *ModelAvailabilityCache) key(credentialID int, rawModel string) string {
	return fmt.Sprintf("llmgw:avail:%d:%s", credentialID, rawModel)
}

func modelAvailabilityFields(
	credentialID int,
	rawModel string,
	state string,
	available bool,
	lastStatus string,
	consecutiveSuccesses int,
	consecutiveFailures int,
	nextRetryAt *time.Time,
	source string,
) map[string]any {
	fields := map[string]any{
		"credential_id":         strconv.Itoa(credentialID),
		"raw_model_name":        rawModel,
		"state":                 state,
		"available":             strconv.FormatBool(available),
		"last_status":           lastStatus,
		"consecutive_successes": strconv.Itoa(consecutiveSuccesses),
		"consecutive_failures":  strconv.Itoa(consecutiveFailures),
		"updated_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"source":                source,
	}
	if nextRetryAt != nil {
		fields["next_retry_at"] = nextRetryAt.UTC().Format(time.RFC3339Nano)
	}
	return fields
}
