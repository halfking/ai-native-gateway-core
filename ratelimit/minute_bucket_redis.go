package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var minuteBucketAdmissionScript = redis.NewScript(`
local bucket = KEYS[1]
local queue = KEYS[2]
local sequence = KEYS[3]
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local token = ARGV[3]

local count = tonumber(redis.call('GET', bucket) or '0')
local queued = redis.call('ZCARD', queue)
if queued == 0 and count < limit then
  redis.call('INCR', bucket)
  redis.call('EXPIRE', bucket, 120)
  return {1, 0, limit - count - 1}
end
if queued >= limit then
  return {0, queued + 1, limit - count}
end

local seq = redis.call('INCR', sequence)
redis.call('ZADD', queue, seq, token)
redis.call('EXPIRE', queue, 120)
redis.call('EXPIRE', sequence, 120)
return {2, queued + 1, limit - count}
`)

var minuteBucketClaimScript = redis.NewScript(`
local bucket = KEYS[1]
local queue = KEYS[2]
local token = ARGV[1]
local limit = tonumber(ARGV[2])
local count = tonumber(redis.call('GET', bucket) or '0')
local rank = redis.call('ZRANK', queue, token)
if rank == false then
  return {-1, 0}
end
if rank == 0 and count < limit then
  redis.call('ZREM', queue, token)
  redis.call('INCR', bucket)
  redis.call('EXPIRE', bucket, 120)
  return {1, limit - count - 1}
end
return {0, rank + 1}
`)

var minuteBucketCancelScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`)

func (l *RedisLimiter) admitRPMRedis(ctx context.Context, keyID, limit int, notify func(AdmissionResult)) (AdmissionResult, error) {
	if limit <= 0 {
		return AdmissionResult{Admitted: true}, nil
	}
	token := fmt.Sprintf("%d:%d", time.Now().UnixNano(), keyID)
	bucketKey := fmt.Sprintf("rl:minute:%d:%d", keyID, time.Now().Unix()/60)
	queueKey := fmt.Sprintf("rl:minute:queue:%d", keyID)
	sequenceKey := fmt.Sprintf("rl:minute:sequence:%d", keyID)
	values, err := minuteBucketAdmissionScript.Run(ctx, l.client,
		[]string{bucketKey, queueKey, sequenceKey}, time.Now().Unix(), limit, token).Int64Slice()
	if err != nil {
		return AdmissionResult{}, err
	}
	if len(values) != 3 {
		return AdmissionResult{}, fmt.Errorf("minute bucket admission returned %d values", len(values))
	}
	result := AdmissionResult{Limit: limit, Position: int(values[1]), Remaining: int(values[2])}
	result.QueueRemaining = limit - result.Position
	switch values[0] {
	case 1:
		result.Admitted = true
		return result, nil
	case 0:
		return result, ErrMinuteBucketFull
	case 2:
		result.Waiting = true
		if notify != nil {
			notify(result)
		}
	default:
		return AdmissionResult{}, fmt.Errorf("minute bucket admission returned status %d", values[0])
	}

	for {
		wait := time.Until(time.Unix((time.Now().Unix()/60+1)*60, 0))
		if wait <= 0 {
			wait = time.Millisecond
		}
		if wait > maxMinuteBucketWait {
			wait = maxMinuteBucketWait
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			_, _ = minuteBucketCancelScript.Run(context.Background(), l.client, []string{queueKey}, token).Result()
			return result, ctx.Err()
		case <-timer.C:
			if wait == maxMinuteBucketWait {
				_, _ = minuteBucketCancelScript.Run(context.Background(), l.client, []string{queueKey}, token).Result()
				return result, ErrMinuteBucketWaitTimeout
			}
		}
		claim, err := minuteBucketClaimScript.Run(ctx, l.client,
			[]string{fmt.Sprintf("rl:minute:%d:%d", keyID, time.Now().Unix()/60), queueKey}, token, limit).Int64Slice()
		if err != nil {
			return result, err
		}
		if len(claim) != 2 {
			return result, fmt.Errorf("minute bucket claim returned %d values", len(claim))
		}
		if claim[0] == 1 {
			result.Admitted = true
			result.Waiting = false
			result.Remaining = int(claim[1])
			return result, nil
		}
	}
}
