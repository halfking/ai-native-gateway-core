package bg

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type ModelAvailabilitySnapshot struct {
	State                string
	Available            bool
	LastStatus           string
	ConsecutiveSuccesses int
	ConsecutiveFailures  int
	UpdatedAt            *time.Time
	NextRetryAt          *time.Time
	Source               string
	LoadedFromCache      bool
}

type ModelAvailabilityReader struct {
	redis *redis.Client
}

func NewModelAvailabilityReader(redisClient *redis.Client) *ModelAvailabilityReader {
	if redisClient == nil {
		return nil
	}
	return &ModelAvailabilityReader{redis: redisClient}
}

func (r *ModelAvailabilityReader) Enabled() bool {
	return r != nil && r.redis != nil
}

func (r *ModelAvailabilityReader) Read(ctx context.Context, credentialID int, rawModel string) (*ModelAvailabilitySnapshot, error) {
	if !r.Enabled() {
		return nil, nil
	}
	data, err := r.redis.HGetAll(ctx, (&ModelAvailabilityCache{}).key(credentialID, rawModel)).Result()
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	s := &ModelAvailabilitySnapshot{
		State:           data["state"],
		Available:       data["available"] == "true",
		LastStatus:      data["last_status"],
		Source:          data["source"],
		LoadedFromCache: true,
	}
	if v, err := strconv.Atoi(data["consecutive_successes"]); err == nil {
		s.ConsecutiveSuccesses = v
	}
	if v, err := strconv.Atoi(data["consecutive_failures"]); err == nil {
		s.ConsecutiveFailures = v
	}
	if data["updated_at"] != "" {
		if ts, err := time.Parse(time.RFC3339Nano, data["updated_at"]); err == nil {
			s.UpdatedAt = &ts
		}
	}
	if data["next_retry_at"] != "" {
		if ts, err := time.Parse(time.RFC3339Nano, data["next_retry_at"]); err == nil {
			s.NextRetryAt = &ts
		}
	}
	return s, nil
}
