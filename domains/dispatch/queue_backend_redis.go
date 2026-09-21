package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisQueueBackend (V6-W1.7 U2, docs/架构优化v6/10-dual-backend-queue.md §2.1)
// is the cluster-wide admission plane: Tier-0 waiting-room admission, lane
// capacity reservations and the due-parked ZSET, accounted in Redis so
// multiple gateway instances share one capacity budget.
//
// Accounting layout (one hash pair per capacity family, hash-tagged into one
// cluster slot so the admit/release scripts stay single-slot atomic):
//
//	llmgw:dispatch:queue:v1:{qbt}:counts     HASH {instance → held total admissions}
//	llmgw:dispatch:queue:v1:{qbt}:hb         HASH {instance → last heartbeat ms}
//	llmgw:dispatch:queue:v1:{qbl:<kind>:<id>}:counts / :hb   (same pair per lane)
//	llmgw:dispatch:queue:v1:due              ZSET {member: instance|request_id, score: due ms}
//
// Self-healing (D2): every count entry is paired with a heartbeat field; a
// dead instance stops refreshing its hb, and within queueStaleWindow (30s)
// any admit/release touching the family sweeps its counts away — cluster
// capacity returns without operator action. Live instances refresh their hb
// on every admit/release and from the heartbeat loop (10s), so they are
// never swept while holding admissions.
//
// Fail-open (§2.2, the OPPOSITE of the Governor): on Redis errors the
// backend degrades — admits locally and lets the in-process primitives be
// the bound — flips dispatch_queue_backend_degraded, and recovers on the
// first successful heartbeat/op (D5).

const (
	queueHeartbeatInterval = 10 * time.Second
	queueStaleWindowMS     = 30_000
	queueHashTTL           = 2 * time.Minute // whole-family floor; per-instance self-heal is hb-based
	queueDueKey            = "llmgw:dispatch:queue:v1:due"
	queueTotalSlot         = "{qbt}"
	queueLaneSlotPrefix    = "{qbl:"
	// dueResidueAge bounds how long dead-instance members may linger in the
	// due ZSET before the heartbeat sweep removes them (D8).
	dueResidueAge = time.Hour
)

func queueLaneSlot(kind LaneKind, id string) string {
	return queueLaneSlotPrefix + string(kind) + ":" + id + "}"
}

func queueFamilyKeys(slot string) (counts, hb string) {
	return "llmgw:dispatch:queue:v1:" + slot + ":counts",
		"llmgw:dispatch:queue:v1:" + slot + ":hb"
}

// queueScriptAdmit atomically sums the live instances' counts (sweeping
// stale entries away), then increments the caller's own field when the
// cluster sum is under the cap. KEYS[1]=counts, KEYS[2]=hb;
// ARGV[1]=instance, ARGV[2]=cap, ARGV[3]=stale window ms. Returns
// {admitted(0/1), cluster_sum_after}.
var queueScriptAdmit = redis.NewScript(`
local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)
local stale = tonumber(ARGV[3])
local sum = 0
local fields = redis.call('HGETALL', KEYS[1])
for i = 1, #fields, 2 do
  local inst = fields[i]
  local hb = redis.call('HGET', KEYS[2], inst)
  if hb and now_ms - tonumber(hb) <= stale then
    sum = sum + tonumber(fields[i + 1] or '0')
  else
    redis.call('HDEL', KEYS[1], inst)
    redis.call('HDEL', KEYS[2], inst)
  end
end
if sum >= tonumber(ARGV[2]) then
  return {0, sum}
end
redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
redis.call('HSET', KEYS[2], ARGV[1], tostring(now_ms))
redis.call('PEXPIRE', KEYS[1], ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
return {1, sum + 1}
`)

// queueScriptRelease decrements the caller's field, floored at zero and
// no-op when the family or field was already swept away (the capacity then
// returned via the TTL/heal path). ARGV[1]=instance, ARGV[2]=hash TTL ms.
// Returns released(0/1).
var queueScriptRelease = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], ARGV[1])
if not cur then
  return 0
end
local n = tonumber(cur)
if n <= 0 then
  return 0
end
local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)
redis.call('HINCRBY', KEYS[1], ARGV[1], -1)
redis.call('HSET', KEYS[2], ARGV[1], tostring(now_ms))
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`)

// queueScriptHeartbeat refreshes the caller's hb field (creating it when
// absent) without touching counts. ARGV[1]=instance, ARGV[2]=hash TTL ms.
var queueScriptHeartbeat = redis.NewScript(`
local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)
redis.call('HSET', KEYS[2], ARGV[1], tostring(now_ms))
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`)

// queueScriptSum sums the live instances' counts for observability.
// ARGV[1]=stale window ms.
var queueScriptSum = redis.NewScript(`
local t = redis.call('TIME')
local now_ms = t[1] * 1000 + math.floor(t[2] / 1000)
local sum = 0
local fields = redis.call('HGETALL', KEYS[1])
for i = 1, #fields, 2 do
  local inst = fields[i]
  local hb = redis.call('HGET', KEYS[2], inst)
  if hb and now_ms - tonumber(hb) <= tonumber(ARGV[1]) then
    sum = sum + tonumber(fields[i + 1] or '0')
  end
end
return sum
`)

// redisQueueBackend implements QueueBackend against a shared Redis.
type redisQueueBackend struct {
	client     *redis.Client
	instanceID string

	degraded atomic.Bool
	heldTotal atomic.Int64
	heldModel atomic.Int64
	heldCred  atomic.Int64

	// laneMu guards the tracked lane families (heartbeats refresh the own
	// hb field in each family this instance ever reserved).
	laneMu sync.Mutex
	lanes  map[string]struct{}

	hbCancel context.CancelFunc
	hbWG     sync.WaitGroup
	stopOnce sync.Once
}

// NewRedisQueueBackend builds the cluster backend. instanceID must be stable
// per process (it keys the count/hash fields; two instances sharing an ID
// would merge their budgets).
func NewRedisQueueBackend(client *redis.Client, instanceID string) QueueBackend {
	return &redisQueueBackend{
		client:     client,
		instanceID: instanceID,
		lanes:      make(map[string]struct{}),
	}
}

func (b *redisQueueBackend) Kind() QueueBackendKind { return QueueBackendRedis }

func (b *redisQueueBackend) Open(ctx context.Context) error {
	if b.client == nil {
		return fmt.Errorf("dispatch queue backend: nil redis client")
	}
	if err := b.Heartbeat(ctx); err != nil {
		return fmt.Errorf("dispatch queue backend: initial heartbeat failed: %w", err)
	}
	child, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b.hbCancel = cancel
	b.hbWG.Add(1)
	go b.runHeartbeat(child)
	return nil
}

func (b *redisQueueBackend) Close() error {
	b.stopOnce.Do(func() {
		if b.hbCancel != nil {
			b.hbCancel()
		}
	})
	b.hbWG.Wait()
	return nil
}

func (b *redisQueueBackend) runHeartbeat(ctx context.Context) {
	defer b.hbWG.Done()
	ticker := time.NewTicker(queueHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.Heartbeat(ctx); err != nil {
				b.markDegraded("heartbeat", err)
			}
		}
	}
}

// Heartbeat refreshes the instance liveness fields (total + every lane
// family this instance touched) and sweeps due-ZSET residue older than
// dueResidueAge. A success clears the degraded flag (D5 auto-recovery).
func (b *redisQueueBackend) Heartbeat(ctx context.Context) error {
	if b.client == nil {
		return fmt.Errorf("nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, totalHB := queueFamilyKeys(queueTotalSlot)
	hbKeys := []string{totalHB}
	b.laneMu.Lock()
	for slot := range b.lanes {
		_, hb := queueFamilyKeys(slot)
		hbKeys = append(hbKeys, hb)
	}
	b.laneMu.Unlock()
	// Heartbeat each family via the script (server-side TIME).
	var errs []error
	for _, key := range hbKeys {
		if err := queueScriptHeartbeat.Run(ctx, b.client, []string{countsKeyOf(key), key},
			b.instanceID, queueHashTTL.Milliseconds()).Err(); err != nil {
			errs = append(errs, err)
		}
	}
	// Due residue sweep (D8): drop members scored older than now-1h.
	nowMS := time.Now().UnixMilli()
	if err := b.client.ZRemRangeByScore(ctx, queueDueKey, "-inf",
		strconv.FormatInt(nowMS-dueResidueAge.Milliseconds(), 10)).Err(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("heartbeat: %w", errs[0])
	}
	b.clearDegraded()
	return nil
}

// countsKeyOf derives the counts key of a hb key (…:hb → …:counts).
func countsKeyOf(hbKey string) string {
	return hbKey[:len(hbKey)-len(":hb")] + ":counts"
}

func (b *redisQueueBackend) TryAdmitTotal(ctx context.Context, qr *QueuedRequest, cap int) (Admission, bool) {
	return b.admit(ctx, LaneTotal, "", queueTotalSlot, cap)
}

func (b *redisQueueBackend) TryReserveLane(ctx context.Context, kind LaneKind, id string, cap int) (Admission, bool) {
	if kind != LaneModel && kind != LaneCredential {
		return Admission{}, true // unknown lane kinds never gate admission
	}
	slot := queueLaneSlot(kind, id)
	b.laneMu.Lock()
	b.lanes[slot] = struct{}{}
	b.laneMu.Unlock()
	return b.admit(ctx, kind, id, slot, cap)
}

// admit runs the admit script with one short retry (D1); on Redis failure
// it fail-opens: admit without cluster accounting, flip degraded, and let
// the in-process primitives bound this instance (§2.2).
func (b *redisQueueBackend) admit(ctx context.Context, kind LaneKind, id, slot string, cap int) (Admission, bool) {
	if cap <= 0 {
		return Admission{}, true // unbounded family
	}
	counts, hb := queueFamilyKeys(slot)
	args := []any{b.instanceID, cap, queueStaleWindowMS, queueHashTTL.Milliseconds()}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(20 * time.Millisecond) // D1: single short retry
		}
		res, err := queueScriptAdmit.Run(ctx, b.client, []string{counts, hb}, args...).Slice()
		if err != nil {
			lastErr = err
			continue
		}
		if len(res) < 2 {
			lastErr = fmt.Errorf("admit script malformed reply")
			continue
		}
		if v, _ := res[0].(int64); v == 1 {
			b.clearDegraded()
			return b.mint(kind, id, slot), true
		}
		metricQueueBackendRejected.WithLabelValues(string(b.Kind()), string(kind)).Inc()
		return Admission{}, false // cluster capacity genuinely full
	}
	b.markDegraded("admit", lastErr)
	return Admission{}, true // fail-open
}

// Release is idempotent per token; the pipeline guards with a take-once
// swap. A release for an already-swept family is a no-op server-side.
func (b *redisQueueBackend) Release(a Admission) {
	if a.Kind == "" {
		return
	}
	counts, hb := queueFamilyKeys(a.Key)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := queueScriptRelease.Run(ctx, b.client, []string{counts, hb},
		b.instanceID, queueHashTTL.Milliseconds()).Int(); err != nil {
		// Fail-open plane: a lost release leaks at most one slot for
		// queueStaleWindow (self-heal) — log at debug, never panic.
		slog.Debug("dispatch queue backend: release failed", "lane", a.Kind, "error", err)
	}
	b.unmint(a)
}

func (b *redisQueueBackend) mint(kind LaneKind, id, slot string) Admission {
	switch kind {
	case LaneModel:
		b.heldModel.Add(1)
		metricQueueBackendHeld.WithLabelValues(string(b.Kind()), string(kind)).Inc()
	case LaneCredential:
		b.heldCred.Add(1)
		metricQueueBackendHeld.WithLabelValues(string(b.Kind()), string(kind)).Inc()
	default:
		b.heldTotal.Add(1)
		metricQueueBackendHeld.WithLabelValues(string(b.Kind()), string(LaneTotal)).Inc()
	}
	return Admission{Kind: kind, ID: id, Key: slot}
}

func (b *redisQueueBackend) unmint(a Admission) {
	switch a.Kind {
	case LaneModel:
		b.heldModel.Add(-1)
		metricQueueBackendHeld.WithLabelValues(string(b.Kind()), string(a.Kind)).Dec()
	case LaneCredential:
		b.heldCred.Add(-1)
		metricQueueBackendHeld.WithLabelValues(string(b.Kind()), string(a.Kind)).Dec()
	default:
		b.heldTotal.Add(-1)
		metricQueueBackendHeld.WithLabelValues(string(b.Kind()), string(LaneTotal)).Dec()
	}
}

func (b *redisQueueBackend) ParkDue(ctx context.Context, requestID string, dueAt time.Time) error {
	if b.client == nil || requestID == "" || dueAt.IsZero() {
		return nil
	}
	member := b.instanceID + "|" + requestID
	// Score is instance epoch ms (D4 note: Redis TIME yields "now", not a
	// future due timestamp; the sweep uses the same epoch family).
	return b.client.ZAdd(ctx, queueDueKey, redis.Z{
		Score: float64(dueAt.UnixMilli()),
		Member: member,
	}).Err()
}

func (b *redisQueueBackend) ClearDue(ctx context.Context, requestID string) error {
	if b.client == nil || requestID == "" {
		return nil
	}
	return b.client.ZRem(ctx, queueDueKey, b.instanceID+"|"+requestID).Err()
}

func (b *redisQueueBackend) Snapshot(ctx context.Context) (QueueBackendStats, error) {
	stats := QueueBackendStats{
		Kind:       b.Kind(),
		InstanceID: b.instanceID,
		TotalHeld:  b.heldTotal.Load(),
		ModelHeld:  b.heldModel.Load(),
		CredHeld:   b.heldCred.Load(),
		Degraded:   b.degraded.Load(),
	}
	if b.client == nil {
		return stats, nil
	}
	counts, hb := queueFamilyKeys(queueTotalSlot)
	sum, err := queueScriptSum.Run(ctx, b.client, []string{counts, hb}, queueStaleWindowMS).Int()
	if err != nil {
		return stats, fmt.Errorf("cluster total sum: %w", err)
	}
	stats.ClusterTotal = int64(sum)
	return stats, nil
}

func (b *redisQueueBackend) markDegraded(op string, err error) {
	if b.degraded.CompareAndSwap(false, true) {
		metricQueueBackendDegraded.WithLabelValues(string(b.Kind())).Set(1)
		slog.Warn("dispatch queue backend degraded: failing open to local admission bounds",
			"op", op, "instance", b.instanceID, "error", err)
	}
}

func (b *redisQueueBackend) clearDegraded() {
	if b.degraded.CompareAndSwap(true, false) {
		metricQueueBackendDegraded.WithLabelValues(string(b.Kind())).Set(0)
		slog.Info("dispatch queue backend recovered", "instance", b.instanceID)
	}
}
