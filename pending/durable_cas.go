package pending

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// DurableCASResult describes the atomic durable projection decision.
type DurableCASResult int

const (
	DurableCASRejectedTask DurableCASResult = iota
	DurableCASApplied
	DurableCASStale
)

var durableSaveScript = redis.NewScript(`
local exists = redis.call('EXISTS', KEYS[1])
if exists == 1 then
  local current_task = redis.call('HGET', KEYS[1], 'task_id') or ''
  if current_task ~= '' and current_task ~= ARGV[1] then
    return 0
  end
  local current_version = tonumber(redis.call('HGET', KEYS[1], 'result_version') or '-1')
  if tonumber(ARGV[3]) < current_version then
    return 2
  end
end

redis.call('HSET', KEYS[1],
  'task_id', ARGV[1],
  'fencing_token', ARGV[2],
  'result_version', ARGV[3],
  'result_hash', ARGV[4],
  'session_id', ARGV[5],
  'tenant_id', ARGV[6],
  'request_id', ARGV[7],
  'status', ARGV[8],
  'body', ARGV[9],
  'content_type', ARGV[10],
  'request_hash', ARGV[11],
  'created_at', ARGV[12],
  'completed_at', ARGV[13],
  'bytes_buffered', ARGV[14],
  'is_stream', ARGV[15],
  'error_message', ARGV[16])
redis.call('PEXPIRE', KEYS[1], ARGV[18])
redis.call('ZADD', KEYS[2], ARGV[17], ARGV[7])
redis.call('PEXPIRE', KEYS[2], ARGV[18])
return 1
`)

// SaveDurableCAS projects a PostgreSQL terminal version into Redis. Existing
// entries can only be overwritten by the same task and a non-decreasing
// result version. The entry TTL is derived from the task result-read window.
func (s *Store) SaveDurableCAS(ctx context.Context, r *Response, expiresAt time.Time) (DurableCASResult, error) {
	if r == nil {
		return DurableCASRejectedTask, errors.New("pending: nil durable response")
	}
	if r.SessionID == "" || r.RequestID == "" || r.TaskID == "" || r.ResultVersion <= 0 {
		return DurableCASRejectedTask, errors.New("pending: durable projection requires session, request, task and positive result version")
	}
	if s == nil || s.rdb == nil {
		return DurableCASRejectedTask, ErrUnavailable
	}
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return DurableCASStale, nil
	}

	projected := *r
	if projected.CreatedAt == 0 {
		projected.CreatedAt = time.Now().Unix()
	}
	if projected.CompletedAt == 0 {
		projected.CompletedAt = time.Now().Unix()
	}
	if len(projected.Body) > MaxBodyBytes {
		projected.BytesBuffered = len(projected.Body)
		projected.Body = ""
		projected.ErrorMessage = "response_too_large: see PostgreSQL durable result"
	} else {
		projected.BytesBuffered = len(projected.Body)
	}

	result, err := durableSaveScript.Run(ctx, s.rdb,
		[]string{entryKey(projected.SessionID, projected.RequestID), indexKey(projected.SessionID)},
		projected.TaskID,
		projected.FencingToken,
		projected.ResultVersion,
		projected.ResultHash,
		projected.SessionID,
		projected.TenantID,
		projected.RequestID,
		string(projected.Status),
		projected.Body,
		projected.ContentType,
		projected.RequestHash,
		projected.CreatedAt,
		projected.CompletedAt,
		projected.BytesBuffered,
		projected.IsStream,
		projected.ErrorMessage,
		projected.CompletedAt,
		ttl.Milliseconds(),
	).Int()
	if err != nil {
		return DurableCASRejectedTask, err
	}
	return DurableCASResult(result), nil
}
