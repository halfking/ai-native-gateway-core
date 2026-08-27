package requestjourney

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"
)

type redisJourneyClient interface {
	redis.Scripter
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	LRange(ctx context.Context, key string, start, stop int64) *redis.StringSliceCmd
	ZRange(ctx context.Context, key string, start, stop int64) *redis.StringSliceCmd
	SMembers(ctx context.Context, key string) *redis.StringSliceCmd
}

// RedisStore is the shared hot projection used by all gateway instances.
type RedisStore struct {
	client redisJourneyClient
	config Config
}

func NewRedisStore(client redisJourneyClient, config Config) *RedisStore {
	defaults := DefaultConfig()
	config.TotalRequestCapacity = positiveOrDefault(config.TotalRequestCapacity, defaults.TotalRequestCapacity)
	config.PerModelCapacity = positiveOrDefault(config.PerModelCapacity, defaults.PerModelCapacity)
	config.PerNodeCapacity = positiveOrDefault(config.PerNodeCapacity, defaults.PerNodeCapacity)
	if config.DetailTTL <= 0 {
		config.DetailTTL = defaults.DetailTTL
	}
	return &RedisStore{client: client, config: config}
}

var applyRedisIngressScript = redis.NewScript(`
local exists = redis.call('HEXISTS', KEYS[2], ARGV[1]) == 1
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
if not exists then
  redis.call('ZADD', KEYS[1], ARGV[3], ARGV[1])
  while redis.call('ZCARD', KEYS[1]) > tonumber(ARGV[4]) do
    local evicted = redis.call('ZRANGE', KEYS[1], 0, 0)[1]
    if evicted then
      redis.call('ZREM', KEYS[1], evicted)
      redis.call('HDEL', KEYS[2], evicted)
    end
  end
end
redis.call('EXPIRE', KEYS[1], ARGV[5])
redis.call('EXPIRE', KEYS[2], ARGV[5])
return exists and 0 or 1
`)

// ApplyIngress stores the tenant-free global FIFO under its own Redis hash tag.
// It is intentionally separate from the tenant journey Lua operation.
func (s *RedisStore) ApplyIngress(ctx context.Context, event IngressEvent) error {
	if s == nil || s.client == nil {
		return errors.New("request journey Redis is unavailable")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = applyRedisIngressScript.Run(ctx, s.client,
		[]string{redisIngressOrderKey(), redisIngressItemsKey()},
		redisIngressIdentity(event.ArrivedAt, event.GatewayInstanceID, event.RequestID), string(body),
		strconv.FormatInt(event.ArrivedAt.UnixNano(), 10),
		s.config.TotalRequestCapacity,
		maxInt64(1, int64(s.config.DetailTTL/time.Second)),
	).Result()
	return err
}

// RecentIngress reads the cross-instance global FIFO in arrival order.
func (s *RedisStore) RecentIngress(ctx context.Context) ([]IngressSnapshot, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("request journey Redis is unavailable")
	}
	order, err := s.client.ZRange(ctx, redisIngressOrderKey(), 0, int64(s.config.TotalRequestCapacity-1)).Result()
	if err != nil {
		return nil, err
	}
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	// Cast to redis.Cmdable since redisJourneyClient embeds the necessary methods
	items, err := redissafe.SafeHGetAll(ctx, s.client.(redis.Cmdable), redisIngressItemsKey())
	if err != nil {
		return nil, err
	}
	result := make([]IngressSnapshot, 0, len(order))
	for _, id := range order {
		body, ok := items[id]
		if !ok {
			continue
		}
		var snapshot IngressSnapshot
		if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
			return nil, fmt.Errorf("decode Redis ingress snapshot: %w", err)
		}
		if err := snapshot.Validate(); err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, nil
}

var applyRedisEventScript = redis.NewScript(`
local existing = redis.call('HGET', KEYS[1], ARGV[1])
if existing then
  if existing == ARGV[2] then return 0 end
  return redis.error_reply('REQUEST_JOURNEY_SEQUENCE_CONFLICT')
end

local detail_was_empty = redis.call('HLEN', KEYS[1]) == 0
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('EXPIRE', KEYS[1], ARGV[3])

local function append_once(key, value, capacity, enabled, reset)
  if enabled ~= '1' then return end
  if reset then redis.call('LREM', key, 0, value) end
  if not redis.call('LPOS', key, value) then redis.call('RPUSH', key, value) end
  redis.call('LTRIM', key, -capacity, -1)
  redis.call('EXPIRE', key, ARGV[3])
end

append_once(KEYS[2], ARGV[4], tonumber(ARGV[8]), '1', detail_was_empty)
append_once(KEYS[3], ARGV[4], tonumber(ARGV[9]), ARGV[6], false)
append_once(KEYS[4], ARGV[5], tonumber(ARGV[10]), ARGV[7], false)
if ARGV[6] == '1' then redis.call('SADD', KEYS[5], ARGV[11]); redis.call('EXPIRE', KEYS[5], ARGV[3]) end
if ARGV[7] == '1' then redis.call('SADD', KEYS[6], ARGV[12]); redis.call('EXPIRE', KEYS[6], ARGV[3]) end
return 1
`)

// Apply atomically stores one idempotent event, refreshes detail retention, and
// appends/trims all applicable FIFO indexes.
func (s *RedisStore) Apply(ctx context.Context, event JourneyEvent) error {
	if s == nil || s.client == nil {
		return errors.New("request journey Redis is unavailable")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	keys := s.keysForEvent(event)
	_, err = applyRedisEventScript.Run(ctx, s.client, keys,
		strconv.FormatInt(event.Seq, 10), string(body),
		maxInt64(1, int64(s.config.DetailTTL/time.Second)),
		event.RequestID, nodeEntry(event), modelForEvent(event) != "", event.Attempt != nil,
		s.config.TotalRequestCapacity, s.config.PerModelCapacity, s.config.PerNodeCapacity,
		encodeKeyPart(modelForEvent(event)), encodeNodeCatalogEntry(nodeKeyForOptionalEvent(event)),
	).Result()
	if err != nil && strings.Contains(err.Error(), "REQUEST_JOURNEY_SEQUENCE_CONFLICT") {
		return fmt.Errorf("%w: seq %d", ErrSequenceConflict, event.Seq)
	}
	return err
}

func (s *RedisStore) Detail(ctx context.Context, tenantID, requestID string) (*RequestJourney, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("request journey Redis is unavailable")
	}
	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	values, err := redissafe.SafeHGetAll(ctx, s.client.(redis.Cmdable), redisDetailKey(tenantID, requestID))
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrJourneyNotFound
	}
	events := make([]JourneyEvent, 0, len(values))
	for _, body := range values {
		var event JourneyEvent
		if err := json.Unmarshal([]byte(body), &event); err != nil {
			return nil, fmt.Errorf("decode Redis journey event: %w", err)
		}
		if event.TenantID != tenantID || event.RequestID != requestID {
			return nil, errors.New("redis journey identity mismatch")
		}
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	return journeyFromEvents(events)
}

func (s *RedisStore) RecentTotal(ctx context.Context, tenantID string) ([]RequestSnapshot, error) {
	return s.recent(ctx, tenantID, redisTotalKey(tenantID), s.config.TotalRequestCapacity, false)
}

func (s *RedisStore) RecentModel(ctx context.Context, tenantID, model string) ([]RequestSnapshot, error) {
	return s.recent(ctx, tenantID, redisModelKey(tenantID, model), s.config.PerModelCapacity, false)
}

func (s *RedisStore) RecentNode(ctx context.Context, tenantID string, key NodeKey) ([]RequestSnapshot, error) {
	return s.recent(ctx, tenantID, redisNodeKey(tenantID, key), s.config.PerNodeCapacity, true)
}

func (s *RedisStore) RecentModels(ctx context.Context, tenantID string) ([]ModelFIFOSnapshot, error) {
	members, err := s.client.SMembers(ctx, redisModelCatalogKey(tenantID)).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(members)
	result := make([]ModelFIFOSnapshot, 0, len(members))
	for _, member := range members {
		model, err := decodeKeyPart(member)
		if err != nil {
			return nil, err
		}
		requests, err := s.RecentModel(ctx, tenantID, model)
		if err != nil {
			return nil, err
		}
		result = append(result, ModelFIFOSnapshot{Model: model, Capacity: s.config.PerModelCapacity, Requests: requests})
	}
	return result, nil
}

func (s *RedisStore) RecentNodes(ctx context.Context, tenantID string) ([]NodeFIFOSnapshot, error) {
	members, err := s.client.SMembers(ctx, redisNodeCatalogKey(tenantID)).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(members)
	result := make([]NodeFIFOSnapshot, 0, len(members))
	for _, member := range members {
		key, err := decodeNodeCatalogEntry(member)
		if err != nil {
			return nil, err
		}
		requests, err := s.RecentNode(ctx, tenantID, key)
		if err != nil {
			return nil, err
		}
		result = append(result, NodeFIFOSnapshot{Model: key.Model, ProviderID: key.ProviderID, CredentialID: key.CredentialID, Capacity: s.config.PerNodeCapacity, Requests: requests})
	}
	return result, nil
}

func (s *RedisStore) recent(ctx context.Context, tenantID, key string, capacity int, node bool) ([]RequestSnapshot, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("request journey Redis is unavailable")
	}
	entries, err := s.client.LRange(ctx, key, 0, int64(capacity-1)).Result()
	if err != nil {
		return nil, err
	}
	result := make([]RequestSnapshot, 0, len(entries))
	for _, entry := range entries {
		requestID, attemptID, err := parseIndexEntry(entry, node)
		if err != nil {
			return nil, err
		}
		journey, err := s.Detail(ctx, tenantID, requestID)
		if errors.Is(err, ErrJourneyNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		snapshot := snapshotFromJourney(journey)
		if node {
			var found bool
			snapshot, found = snapshotForAttempt(journey, attemptID)
			if !found {
				continue
			}
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func (s *RedisStore) keysForEvent(event JourneyEvent) []string {
	model := modelForEvent(event)
	modelKey := redisTenantTag(event.TenantID) + "unused:model"
	if model != "" {
		modelKey = redisModelKey(event.TenantID, model)
	}
	nodeKey := redisTenantTag(event.TenantID) + "unused:node"
	if event.Attempt != nil {
		nodeKey = redisNodeKey(event.TenantID, nodeKeyForEvent(event))
	}
	return []string{
		redisDetailKey(event.TenantID, event.RequestID),
		redisTotalKey(event.TenantID), modelKey, nodeKey,
		redisModelCatalogKey(event.TenantID), redisNodeCatalogKey(event.TenantID),
	}
}

func journeyFromEvents(events []JourneyEvent) (*RequestJourney, error) {
	if len(events) == 0 {
		return nil, ErrJourneyNotFound
	}
	projection := NewProjection(DefaultConfig())
	for _, event := range events {
		if err := projection.Apply(event); err != nil {
			return nil, err
		}
	}
	return projection.Detail(events[0].TenantID, events[0].RequestID)
}

func snapshotFromJourney(journey *RequestJourney) RequestSnapshot {
	var snapshot RequestSnapshot
	for _, event := range journey.Events {
		snapshot = projectSnapshot(snapshot, event, journey.StartedAt, journey.UpdatedAt, journey.ObservationStatus)
	}
	return snapshot
}

func snapshotForAttempt(journey *RequestJourney, attemptID string) (RequestSnapshot, bool) {
	var snapshot RequestSnapshot
	found := false
	for _, event := range journey.Events {
		if event.Attempt == nil || event.Attempt.AttemptID != attemptID {
			continue
		}
		snapshot = projectSnapshot(snapshot, event, journey.StartedAt, event.OccurredAt, journey.ObservationStatus)
		found = true
	}
	return snapshot, found
}

func modelForEvent(event JourneyEvent) string {
	model := event.ResolvedModel
	if event.Model != "" {
		model = event.Model
	}
	if event.ToModel != "" {
		model = event.ToModel
	}
	if event.Attempt != nil && event.Attempt.Model != "" {
		model = event.Attempt.Model
	}
	return model
}

func nodeKeyForOptionalEvent(event JourneyEvent) NodeKey {
	if event.Attempt == nil {
		return NodeKey{}
	}
	return nodeKeyForEvent(event)
}

func encodeNodeCatalogEntry(key NodeKey) string {
	if key.Model == "" || key.ProviderID <= 0 || key.CredentialID <= 0 {
		return ""
	}
	return fmt.Sprintf("%s.%d.%d", encodeKeyPart(key.Model), key.ProviderID, key.CredentialID)
}

func decodeNodeCatalogEntry(value string) (NodeKey, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return NodeKey{}, errors.New("invalid Redis node catalog entry")
	}
	model, err := decodeKeyPart(parts[0])
	if err != nil {
		return NodeKey{}, err
	}
	providerID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return NodeKey{}, err
	}
	credentialID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return NodeKey{}, err
	}
	return NodeKey{Model: model, ProviderID: providerID, CredentialID: credentialID}, nil
}

func nodeEntry(event JourneyEvent) string {
	if event.Attempt == nil {
		return ""
	}
	return encodeKeyPart(event.RequestID) + "." + encodeKeyPart(event.Attempt.AttemptID)
}

func parseIndexEntry(entry string, node bool) (string, string, error) {
	if !node {
		return entry, "", nil
	}
	parts := strings.SplitN(entry, ".", 2)
	if len(parts) != 2 {
		return "", "", errors.New("invalid Redis node FIFO entry")
	}
	requestID, err := decodeKeyPart(parts[0])
	if err != nil {
		return "", "", err
	}
	attemptID, err := decodeKeyPart(parts[1])
	return requestID, attemptID, err
}

func redisIngressTag() string {
	return "requestjourney:{global-ingress}:"
}

func redisIngressOrderKey() string {
	return redisIngressTag() + "order"
}

func redisIngressItemsKey() string {
	return redisIngressTag() + "items"
}

func redisIngressIdentity(arrivedAt time.Time, gatewayInstanceID, requestID string) string {
	return fmt.Sprintf("%020d.%s.%s", arrivedAt.UnixNano(), encodeKeyPart(gatewayInstanceID), encodeKeyPart(requestID))
}

func redisTenantTag(tenantID string) string {
	return "requestjourney:{" + encodeKeyPart(tenantID) + "}:"
}

func redisDetailKey(tenantID, requestID string) string {
	return redisTenantTag(tenantID) + "detail:" + encodeKeyPart(requestID)
}

func redisTotalKey(tenantID string) string {
	return redisTenantTag(tenantID) + "total"
}

func redisModelCatalogKey(tenantID string) string {
	return redisTenantTag(tenantID) + "catalog:models"
}

func redisNodeCatalogKey(tenantID string) string {
	return redisTenantTag(tenantID) + "catalog:nodes"
}

func redisModelKey(tenantID, model string) string {
	return redisTenantTag(tenantID) + "model:" + encodeKeyPart(model)
}

func redisNodeKey(tenantID string, key NodeKey) string {
	return fmt.Sprintf("%snode:%s:%d:%d", redisTenantTag(tenantID), encodeKeyPart(key.Model), key.ProviderID, key.CredentialID)
}

func encodeKeyPart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeKeyPart(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return string(decoded), err
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
