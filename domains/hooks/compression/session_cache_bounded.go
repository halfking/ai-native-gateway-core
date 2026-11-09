package compression

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
)

const (
	strictCacheBodyBytes = 1 << 20
	strictCacheMetaBytes = 256 << 10
	boundedReadScript    = `-- session-cache-bounded-read
local kind=redis.call('TYPE',KEYS[1]).ok
if kind == 'none' then return {} end
if kind ~= 'hash' then return redis.error_reply('WRONGTYPE bounded session cache requires hash') end
local body_limit=tonumber(ARGV[1])
local meta_limit=tonumber(ARGV[2])
local fields={}
local meta_size=0
for i=4,#ARGV do
 local field=ARGV[i]
 local size=redis.call('HSTRLEN',KEYS[1],field)
 if ARGV[3] == 'pair' and field == 'body' then
  if size > body_limit then return redis.error_reply('bounded session cache body exceeds capacity') end
 else
  meta_size=meta_size+size
  if size > meta_limit or meta_size > meta_limit then return redis.error_reply('bounded session cache metadata exceeds capacity') end
 end
 fields[#fields+1]=field
end
if ARGV[3] == 'pair' then
 if redis.call('HSTRLEN',KEYS[1],'body_size') > 32 then return redis.error_reply('invalid bounded session cache body size') end
 local declared=redis.call('HGET',KEYS[1],'body_size')
 if declared then
  local size=tonumber(declared)
  if not size or size < 0 or size > body_limit or size ~= math.floor(size) then return redis.error_reply('bounded session cache decoded body exceeds capacity') end
 end
end
return redis.call('HMGET',KEYS[1],unpack(fields))`
)

var boundedPairFields = []string{"pair_v", "revision", "state", "state_hash", "body", "body_codec", "body_size", "body_hash"}

var boundedLegacyFields = func() []string {
	// Fixed writer-defined fields, never HKEYS/HGETALL over an old huge hash.
	encoded := encodeSessionStateFields(&SessionState{})
	fields := []string{"state", "th", "sys", "fsh", "lsat", "sa", "cm", "ct", "mas", "tas"}
	for i := 0; i < len(encoded); i += 2 {
		fields = append(fields, encoded[i].(string))
	}
	return fields
}()

func cacheBudgetStrict(ctx context.Context) bool {
	_, strict := contextbudget.SpecFromContext(ctx)
	return strict
}

func boundedCachePair(state *SessionState, body []byte) error {
	if len(body) > strictCacheBodyBytes || !contextbudget.MetadataFits(state, strictCacheMetaBytes) {
		return errors.New("session cache pair exceeds bounded recovery capacity")
	}
	return nil
}

func (c *SessionCache) readCacheFields(ctx context.Context, key string, legacy bool) (map[string]string, error) {
	if !cacheBudgetStrict(ctx) {
		return c.redis.HGetAll(ctx, key)
	}
	backend, err := c.scriptBackend()
	if err != nil {
		return nil, err
	}
	fields, mode := boundedPairFields, "pair"
	if legacy {
		fields, mode = boundedLegacyFields, "legacy"
	}
	args := []any{strictCacheBodyBytes, strictCacheMetaBytes, mode}
	for _, field := range fields {
		args = append(args, field)
	}
	result, err := backend.Eval(ctx, boundedReadScript, []string{key}, args...)
	if err != nil {
		return nil, fmt.Errorf("bounded session cache read: %w", err)
	}
	values, ok := result.([]any)
	if !ok || (len(values) != 0 && len(values) != len(fields)) {
		return nil, errors.New("invalid bounded session cache result")
	}
	out := make(map[string]string, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid bounded session cache field")
		}
		out[fields[i]] = text
	}
	return out, nil
}

// A legacy DB backend must explicitly support bounding values before transfer.
// Checking the length after LastOutboundForSession has allocated is too late.
type boundedSessionCacheDB interface {
	LastOutboundForSessionBounded(context.Context, string, string, int, int) (*LastOutboundRow, error)
}

func (c *SessionCache) loadStrictLegacyBody(ctx context.Context, tenantID, sessionID string) (*SessionState, []byte, error) {
	backend, ok := c.db.(boundedSessionCacheDB)
	if !ok || ShouldSkipLegacyLoad(ctx, sessionID) || ShouldSkipProbeLoad(sessionID) {
		return nil, nil, nil
	}
	row, err := backend.LastOutboundForSessionBounded(ctx, tenantID, sessionID, strictCacheBodyBytes, strictCacheMetaBytes)
	if err != nil || row == nil {
		return nil, nil, err
	}
	if len(row.OutboundBody) > strictCacheBodyBytes || len(row.CompressionMeta) > strictCacheMetaBytes || len(row.OutboundMsgHashes) > strictCacheMetaBytes {
		return nil, nil, errors.New("bounded session cache DB contract exceeded")
	}
	marker := ""
	var meta struct {
		SummaryMarker string `json:"summary_marker"`
	}
	if json.Unmarshal(row.CompressionMeta, &meta) == nil {
		marker = meta.SummaryMarker
	}
	state := &SessionState{SchemaVersion: schemaVersion, LastOutboundHash: sha256Hex(row.OutboundBody), MsgCount: row.OutboundMsgCount, TokenEstimate: row.OutboundTokenEst, SummaryMarker: marker}
	return state, row.OutboundBody, nil
}

func (c *SessionCache) loadStrictFromDB(ctx context.Context, tenantID, sessionID string) (*SessionState, []byte, error) {
	if ShouldSkipLegacyLoad(ctx, sessionID) {
		return nil, nil, nil
	}
	if c.turnReader == nil {
		return c.loadStrictLegacyBody(ctx, tenantID, sessionID)
	}
	messages, err := c.turnReader.LoadLatestOutbound(ctx, tenantID, sessionID)
	if err != nil {
		return nil, nil, err // Never reinterpret oversized latest as an old snapshot.
	}
	if len(messages) == 0 {
		messages, err = c.turnReader.LoadChain(ctx, tenantID, sessionID, 10)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(messages) == 0 {
		return c.loadStrictLegacyBody(ctx, tenantID, sessionID)
	}
	body, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		return nil, nil, err
	}
	if len(body) > strictCacheBodyBytes {
		return nil, nil, errors.New("rebuilt session cache body exceeds bounded capacity")
	}
	return &SessionState{SchemaVersion: schemaVersion, LastOutboundHash: sha256Hex(body), MsgCount: len(messages), TokenEstimate: estimateBodyTokens(body)}, body, nil
}
