package dispatch

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const minuteStatsTTL = 2 * time.Hour

const minuteStatsLua = `
local bucket = KEYS[1]
local index = KEYS[2]
local requests = tonumber(ARGV[1])
local successes = tonumber(ARGV[2])
local failures = tonumber(ARGV[3])
local tokens = tonumber(ARGV[4])
local ttl_ms = tonumber(ARGV[5])
redis.call('HINCRBY', bucket, 'requests', requests)
redis.call('HINCRBY', bucket, 'successes', successes)
redis.call('HINCRBY', bucket, 'failures', failures)
redis.call('HINCRBY', bucket, 'estimated_tokens', tokens)
redis.call('ZADD', index, 0, bucket)
redis.call('PEXPIRE', bucket, ttl_ms)
redis.call('PEXPIRE', index, ttl_ms)
return 1
`

// MinuteStatsAggregator keeps a short-lived, atomic operational projection.
// It is not an accounting source of truth; persistent reporting remains in
// the existing PostgreSQL rollups.
type MinuteStatsAggregator struct {
	client redis.UniversalClient
	script *redis.Script
}

// MinuteStatsSink is the bounded operational projection written at request
// terminal state. Implementations must not change delivery semantics.
type MinuteStatsSink interface {
	Record(ctx context.Context, credential CredentialRef, model string, estimatedTokens int, outcome ForwardOutcome) error
}

type MinuteStat struct {
	Bucket          time.Time `json:"bucket"`
	ProviderID      int       `json:"provider_id"`
	Model           string    `json:"model"`
	CredentialID    int       `json:"credential_id"`
	Outcome         string    `json:"outcome"`
	Requests        int64     `json:"requests"`
	Successes       int64     `json:"successes"`
	Failures        int64     `json:"failures"`
	EstimatedTokens int64     `json:"estimated_tokens"`
}

func NewMinuteStatsAggregator(client redis.UniversalClient) *MinuteStatsAggregator {
	if client == nil {
		return nil
	}
	return &MinuteStatsAggregator{client: client, script: redis.NewScript(minuteStatsLua)}
}

func (a *MinuteStatsAggregator) Record(ctx context.Context, credential CredentialRef, model string, estimatedTokens int, outcome ForwardOutcome) error {
	if a == nil || a.client == nil {
		return nil
	}
	if model == "" {
		model = "unknown"
	}
	bucket := time.Now().UTC().Truncate(time.Minute)
	key := minuteStatKey(bucket, credential.ProviderID, model, credential.CredentialID, minuteOutcome(outcome))
	index := minuteStatIndexKey(bucket)
	successes, failures := 0, 1
	if outcome.Err == nil {
		successes, failures = 1, 0
	}
	_, err := a.script.Run(ctx, a.client, []string{key, index}, 1, successes, failures, maxInt(estimatedTokens, 0), minuteStatsTTL.Milliseconds()).Result()
	return err
}

func (a *MinuteStatsAggregator) List(ctx context.Context, bucket time.Time) ([]MinuteStat, error) {
	if a == nil || a.client == nil {
		return []MinuteStat{}, nil
	}
	bucket = bucket.UTC().Truncate(time.Minute)
	keys, err := a.client.ZRange(ctx, minuteStatIndexKey(bucket), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list minute stats failed: %w (context: bucket=%s)", err, bucket.Format(time.RFC3339))
	}
	stats := make([]MinuteStat, 0, len(keys))
	for _, key := range keys {
		values, err := a.client.HGetAll(ctx, key).Result()
		if err != nil || len(values) == 0 {
			continue
		}
		stat, ok := parseMinuteStat(bucket, key, values)
		if ok {
			stats = append(stats, stat)
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Requests > stats[j].Requests })
	return stats, nil
}

func minuteOutcome(outcome ForwardOutcome) string {
	if outcome.Err == nil {
		return "success"
	}
	if outcome.BytesSent {
		return "fail_postfirstbyte"
	}
	return "fail_prefirstbyte"
}

func minuteStatIndexKey(bucket time.Time) string {
	return "llmgw:dispatch:minute:{" + bucket.Format("200601021504") + "}:index"
}

func minuteStatKey(bucket time.Time, providerID int, model string, credentialID int, outcome string) string {
	return "llmgw:dispatch:minute:{" + bucket.Format("200601021504") + "}:p:" + strconv.Itoa(providerID) + ":m:" + url.QueryEscape(model) + ":c:" + strconv.Itoa(credentialID) + ":o:" + outcome
}

func parseMinuteStat(bucket time.Time, key string, values map[string]string) (MinuteStat, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 12 || parts[4] != "p" || parts[6] != "m" || parts[8] != "c" || parts[10] != "o" {
		return MinuteStat{}, false
	}
	providerID, providerErr := strconv.Atoi(parts[5])
	credentialID, credentialErr := strconv.Atoi(parts[9])
	if providerErr != nil || credentialErr != nil {
		return MinuteStat{}, false
	}
	model, err := url.QueryUnescape(parts[7])
	if err != nil {
		return MinuteStat{}, false
	}
	outcome := parts[11]
	return MinuteStat{Bucket: bucket, ProviderID: providerID, Model: model, CredentialID: credentialID, Outcome: outcome,
		Requests: parseMinuteInt(values["requests"]), Successes: parseMinuteInt(values["successes"]), Failures: parseMinuteInt(values["failures"]), EstimatedTokens: parseMinuteInt(values["estimated_tokens"])}, true
}

func parseMinuteInt(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}
