package bg

import (
	"context"
	"fmt"
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
	readStart := time.Now()
	data, err := r.redis.HGetAll(ctx, modelAvailabilityKey(credentialID, rawModel)).Result()
	recordAvailabilityReadDuration(time.Since(readStart).Seconds())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		recordAvailabilityCacheRead("availability_reader", "miss")
		return nil, nil
	}
	recordAvailabilityCacheRead("availability_reader", "hit")

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

func (r *ModelAvailabilityReader) ReadByModel(ctx context.Context, rawModel string) ([]ModelAvailabilitySnapshotWithCredential, error) {
	if !r.Enabled() {
		return nil, nil
	}
	keys, err := r.redis.Keys(ctx, fmt.Sprintf("llmgw:avail:*:%s", rawModel)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]ModelAvailabilitySnapshotWithCredential, 0, len(keys))
	for _, key := range keys {
		data, err := r.redis.HGetAll(ctx, key).Result()
		if err != nil || len(data) == 0 {
			continue
		}
		credID, err := strconv.Atoi(data["credential_id"])
		if err != nil || credID == 0 {
			continue
		}
		snapshot, err := r.Read(ctx, credID, rawModel)
		if err != nil || snapshot == nil {
			continue
		}
		out = append(out, ModelAvailabilitySnapshotWithCredential{
			CredentialID: credID,
			Snapshot:     *snapshot,
		})
	}
	return out, nil
}

// ScanKeys returns the raw availability cache keys that match a credential
// scope filter.  When credentialID is 0 the scan is unrestricted.  Used by
// the admin /api/admin/probe/cache-state endpoint to enumerate the cache
// without going through the per-key Redis SCAN overhead for the common
// credential-scoped queries.
func (r *ModelAvailabilityReader) ScanKeys(ctx context.Context, credentialID int) ([]string, error) {
	if !r.Enabled() {
		return nil, nil
	}
	pattern := "llmgw:avail:*"
	if credentialID > 0 {
		pattern = fmt.Sprintf("llmgw:avail:%d:*", credentialID)
	}
	iter := r.redis.Scan(ctx, 0, pattern, 256).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
		if len(keys) >= 4096 {
			// Cap admin enumeration to keep the endpoint cheap.
			break
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// ReadCredentials returns every cached availability entry for a single raw
// model.  Distinct from ReadByModel only in its error semantics: callers can
// treat the returned slice as an authoritative view (no nil entries).
func (r *ModelAvailabilityReader) ReadCredentials(ctx context.Context, credentialIDs []int, rawModel string) ([]ModelAvailabilitySnapshotWithCredential, error) {
	if !r.Enabled() {
		return nil, nil
	}
	out := make([]ModelAvailabilitySnapshotWithCredential, 0, len(credentialIDs))
	for _, cid := range credentialIDs {
		snap, err := r.Read(ctx, cid, rawModel)
		if err != nil {
			return nil, err
		}
		if snap == nil {
			continue
		}
		out = append(out, ModelAvailabilitySnapshotWithCredential{
			CredentialID: cid,
			Snapshot:     *snap,
		})
	}
	return out, nil
}

type ModelAvailabilitySnapshotWithCredential struct {
	CredentialID int
	Snapshot     ModelAvailabilitySnapshot
}

func modelAvailabilityKey(credentialID int, rawModel string) string {
	return fmt.Sprintf("llmgw:avail:%d:%s", credentialID, rawModel)
}
