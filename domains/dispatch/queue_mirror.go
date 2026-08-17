package dispatch

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Dispatch queue Redis mirror (会话优化 v4 R1.3 gap ⑤ / T2 item 4, §9.1).
//
// The QueueMirror projects dispatch queue state (Tier-1 model lane depths,
// Tier-2 credential lane depths, aggregate in-flight, per-request retry_at)
// into independent VERSIONED Redis keys so other instances and admin tooling
// can observe this gateway's queue pressure and REBUILD METADATA after a
// restart.
//
// ⚠️ OBSERVATION-ONLY CONTRACT: the mirror is never consulted by the
// dispatch hot path and MUST NOT be used to resume execution after a
// restart. Real execution recovery belongs exclusively to the durable task
// lane (snapshot + lease + fencing). See UT-DQ-09 / UT-CO-05.
//
// Key layout (prefix carries the schema version; v1):
//
//	llmgw:dispatch:mirror:v1:t1_depth:{model}      → Tier-1 lane depth (int)
//	llmgw:dispatch:mirror:v1:t2_depth:{credential} → Tier-2 lane depth (int)
//	llmgw:dispatch:mirror:v1:inflight              → aggregate in-flight (int)
//	llmgw:dispatch:mirror:v1:retry_at:{request_id} → retry_at unix millis
//
// All keys carry a TTL (default 10 min) so a dead instance's mirror fades
// out instead of lying forever. Writes are ASYNC BYPASS: a bounded channel
// feeds one worker goroutine; when the channel is full ops are dropped and
// counted — mirroring must never slow admission or forwarding (UT-DQ-07).

const (
	// DefaultQueueMirrorKeyVersion is the mirror schema version.
	DefaultQueueMirrorKeyVersion = "v1"
	// DefaultQueueMirrorPrefix is the key namespace root.
	DefaultQueueMirrorPrefix = "llmgw:dispatch:mirror"
	// DefaultQueueMirrorTTL bounds how long a stale mirror key survives.
	DefaultQueueMirrorTTL = 10 * time.Minute
	// DefaultQueueMirrorBuffer is the async write channel capacity.
	DefaultQueueMirrorBuffer = 256
)

// mirrorRedis is the minimal go-redis surface the mirror needs (satisfied by
// *redis.Client / *redis.ClusterClient and, in tests, by a go-redis client
// pointed at miniredis).
type mirrorRedis interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
}

// QueueMirrorOperation is one async mirror write. A non-nil done channel is
// closed once the worker has applied (or dropped) the op — the Flush barrier.
type QueueMirrorOperation struct {
	key   string
	value string
	del   bool
	done  chan struct{}
}

// LaneDepth is one mirrored lane-depth reading.
type LaneDepth struct {
	// Key is the model name (Tier-1) or credential id string (Tier-2).
	Key string
	// Depth is the last mirrored depth value.
	Depth int64
}

// RetryAtEntry is one mirrored pending retry.
type RetryAtEntry struct {
	// RequestID is the mirrored request identity.
	RequestID string
	// RetryAt is the scheduled retry time.
	RetryAt time.Time
}

// MirrorMetadata is the read-only rebuild projection (UT-DQ-09).
type MirrorMetadata struct {
	// Models lists Tier-1 lane depths.
	Models []LaneDepth
	// Credentials lists Tier-2 lane depths.
	Credentials []LaneDepth
	// InFlight is the aggregate in-flight gauge.
	InFlight int64
	// Retries lists pending retry_at entries.
	Retries []RetryAtEntry
}

// QueueMirror mirrors queue observations into Redis. A nil *QueueMirror (or
// nil client at construction) is a no-op sink, matching the pipeline's
// optional wiring.
type QueueMirror struct {
	client mirrorRedis
	prefix string
	ttl    time.Duration

	ops chan QueueMirrorOperation
	wg  sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// NewQueueMirror builds a mirror. Nil client returns nil (all methods are
// nil-receiver no-ops) so callers can wire unconditionally.
func NewQueueMirror(client mirrorRedis) *QueueMirror {
	if client == nil {
		return nil
	}
	m := &QueueMirror{
		client: client,
		prefix: DefaultQueueMirrorPrefix + ":" + DefaultQueueMirrorKeyVersion,
		ttl:    DefaultQueueMirrorTTL,
		ops:    make(chan QueueMirrorOperation, DefaultQueueMirrorBuffer),
	}
	m.wg.Add(1)
	go m.worker()
	return m
}

// worker drains the async op channel. Redis errors are swallowed: the mirror
// degrades silently (it is a bypass, never an authority).
func (m *QueueMirror) worker() {
	defer m.wg.Done()
	ctx := context.Background()
	for op := range m.ops {
		func() {
			defer func() {
				_ = recover()
				if op.done != nil {
					close(op.done)
				}
			}()
			if op.key == "" {
				return // Flush barrier: no Redis traffic of its own
			}
			if op.del {
				_ = m.client.Del(ctx, op.key).Err()
				return
			}
			_ = m.client.Set(ctx, op.key, op.value, m.ttl).Err()
		}()
	}
}

func (m *QueueMirror) enqueue(op QueueMirrorOperation) {
	if m == nil {
		return
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		if op.done != nil {
			close(op.done)
		}
		return
	}
	select {
	case m.ops <- op:
	default:
		// Bounded bypass: drop rather than block the dispatch hot path.
		metricOverflow.WithLabelValues("queue_mirror_full").Inc()
		if op.done != nil {
			close(op.done)
		}
	}
}

// ObserveQueue mirrors depth/in-flight transitions (QueueObservationSink
// shape). Overflow/governor observations are intentionally not mirrored.
func (m *QueueMirror) ObserveQueue(observation QueueObservation) {
	if m == nil {
		return
	}
	switch observation.Kind {
	case QueueModelDepth:
		if observation.Model == "" {
			return
		}
		m.enqueue(QueueMirrorOperation{
			key:   m.prefix + ":t1_depth:" + observation.Model,
			value: strconv.FormatInt(observation.Depth, 10),
		})
	case QueueCredentialDepth:
		if observation.CredentialID <= 0 {
			return
		}
		m.enqueue(QueueMirrorOperation{
			key:   m.prefix + ":t2_depth:" + strconv.Itoa(observation.CredentialID),
			value: strconv.FormatInt(observation.Depth, 10),
		})
	case QueueInFlight:
		m.enqueue(QueueMirrorOperation{
			key:   m.prefix + ":inflight",
			value: strconv.FormatInt(observation.InFlight, 10),
		})
	}
}

// MirrorRetryAt records one pending timed retry.
func (m *QueueMirror) MirrorRetryAt(requestID string, retryAt time.Time) {
	if m == nil || requestID == "" {
		return
	}
	m.enqueue(QueueMirrorOperation{
		key:   m.prefix + ":retry_at:" + requestID,
		value: strconv.FormatInt(retryAt.UnixMilli(), 10),
	})
}

// ClearRetryAt drops one retry mirror key (retry fired / request terminal).
func (m *QueueMirror) ClearRetryAt(requestID string) {
	if m == nil || requestID == "" {
		return
	}
	m.enqueue(QueueMirrorOperation{key: m.prefix + ":retry_at:" + requestID, del: true})
}

// Flush blocks until every op enqueued before the barrier has been applied
// (or dropped). Bounded wait keeps a wedged worker from hanging callers.
func (m *QueueMirror) Flush() {
	if m == nil {
		return
	}
	done := make(chan struct{})
	m.enqueue(QueueMirrorOperation{done: done})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Close stops the worker goroutine.
func (m *QueueMirror) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	close(m.ops)
	m.wg.Wait()
}

// RebuildMetadata reads the mirror back into a metadata projection. It is
// READ-ONLY: rebuilding never mutates queue state nor restarts execution
// (UT-DQ-09 asserts calling it has no execution side effects).
func (m *QueueMirror) RebuildMetadata(ctx context.Context) (MirrorMetadata, error) {
	metadata := MirrorMetadata{
		Models:      []LaneDepth{},
		Credentials: []LaneDepth{},
		Retries:     []RetryAtEntry{},
	}
	if m == nil {
		return metadata, nil
	}
	var cursor uint64
	for {
		keys, next, err := m.client.Scan(ctx, cursor, m.prefix+":*", 100).Result()
		if err != nil {
			return metadata, err
		}
		for _, key := range keys {
			value, err := m.client.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			m.classifyKey(key, value, &metadata)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return metadata, nil
}

// classifyKey folds one mirror key into the metadata projection. Keys are
// built as prefix+infix+name, so HasPrefix checks are unambiguous.
func (m *QueueMirror) classifyKey(key, value string, metadata *MirrorMetadata) {
	const (
		t1Infix     = ":t1_depth:"
		t2Infix     = ":t2_depth:"
		inflightKey = ":inflight"
		retryInfix  = ":retry_at:"
	)
	depth, _ := strconv.ParseInt(value, 10, 64)
	switch {
	case strings.HasPrefix(key, m.prefix+t1Infix):
		metadata.Models = append(metadata.Models, LaneDepth{
			Key:   strings.TrimPrefix(key, m.prefix+t1Infix),
			Depth: depth,
		})
	case strings.HasPrefix(key, m.prefix+t2Infix):
		metadata.Credentials = append(metadata.Credentials, LaneDepth{
			Key:   strings.TrimPrefix(key, m.prefix+t2Infix),
			Depth: depth,
		})
	case key == m.prefix+inflightKey:
		metadata.InFlight = depth
	case strings.HasPrefix(key, m.prefix+retryInfix):
		ms, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return
		}
		metadata.Retries = append(metadata.Retries, RetryAtEntry{
			RequestID: strings.TrimPrefix(key, m.prefix+retryInfix),
			RetryAt:   time.UnixMilli(ms),
		})
	}
}
