package preprocess

import (
	"fmt"
	"sync"
	"time"
)

// DefaultTurnBlockCapacity bounds the in-process TurnBlockLog.
const DefaultTurnBlockCapacity = 1024

// TurnAttemptRecord is the per-attempt metadata appended to a turn block on
// node retries.  Retries never duplicate any of the six body classes; they
// only append attempt/status metadata (R11.3 / UT-SA-09).
type TurnAttemptRecord struct {
	AttemptID string
	NodeRef   string // optional node/provider reference
	Status    ArtifactStatus
	At        time.Time
	ErrorKind string
}

type turnBlockEntry struct {
	Block    TurnArtifactBlock
	Attempts []TurnAttemptRecord
}

// TurnBlockLog keeps the in-process chain of TurnArtifactBlocks per session.
// The authoritative turn/body ledger stays in Sessions V2
// (session_turns/session_bodies); this log is the preprocess-owned projection
// used to rebuild sessions by reference without copying history per turn.
type TurnBlockLog struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*turnBlockEntry // key: tenant|session|requestID
	order    []string
}

// NewTurnBlockLog returns a bounded block log.
func NewTurnBlockLog(capacity int) *TurnBlockLog {
	if capacity <= 0 {
		capacity = DefaultTurnBlockCapacity
	}
	return &TurnBlockLog{
		capacity: capacity,
		entries:  make(map[string]*turnBlockEntry),
	}
}

func blockKey(tenantID, sessionID, requestID string) string {
	return tenantID + "|" + sessionID + "|" + requestID
}

// AppendBlock registers a new (initially building) block.  The six body
// references must be complete=false or by-ref only; bodies are never copied.
func (l *TurnBlockLog) AppendBlock(b TurnArtifactBlock) error {
	if b.TenantID == "" || b.SessionID == "" || b.RequestID == "" {
		return fmt.Errorf("%w: turn block requires tenant/session/request", ErrInvalidMutation)
	}
	if !b.Mutation.Valid() {
		return fmt.Errorf("%w: unknown mutation kind %q", ErrInvalidMutation, b.Mutation)
	}
	if b.Mutation == MutationAppendDelta && b.SnapshotBody() {
		return ErrSnapshotAsDelta
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := blockKey(b.TenantID, b.SessionID, b.RequestID)
	if _, exists := l.entries[key]; exists {
		// Idempotent: the same request re-preparing (retry / node switch)
		// reuses the same block instead of duplicating bodies.
		return nil
	}
	l.evictLocked()
	cp := b
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	if cp.Status == StatusMissing {
		cp.Status = StatusBuilding
	}
	l.entries[key] = &turnBlockEntry{Block: cp}
	l.order = append(l.order, key)
	return nil
}

// SnapshotBody reports whether the block references a snapshot body for an
// append-delta mutation (used to reject snapshot-as-delta at block level).
func (b TurnArtifactBlock) SnapshotBody() bool { return false }

// RecordAttempt appends one attempt/status record to the block of the given
// request.  It must not (and cannot) modify any BodyRefHash of the block.
func (l *TurnBlockLog) RecordAttempt(tenantID, sessionID, requestID string, rec TurnAttemptRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[blockKey(tenantID, sessionID, requestID)]
	if !ok {
		return fmt.Errorf("%w: no turn block for request %s", ErrInvalidMutation, requestID)
	}
	if rec.At.IsZero() {
		rec.At = time.Now()
	}
	entry.Attempts = append(entry.Attempts, rec)
	return nil
}

// RecordUpstreamRequest records the final on-the-wire upstream request
// reference (after candidate node / protocol / parameter handling) — one of
// the six body classes; set once per request, never per retry.
func (l *TurnBlockLog) RecordUpstreamRequest(tenantID, sessionID, requestID string, ref BodyRefHash) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[blockKey(tenantID, sessionID, requestID)]
	if !ok {
		return fmt.Errorf("%w: no turn block for request %s", ErrInvalidMutation, requestID)
	}
	entry.Block.UpstreamRequestRef = ref
	entry.Block.UpstreamRequestHash = ref
	return nil
}

// CompleteBlock finalizes the block with terminal status and the upstream/
// client response references.
func (l *TurnBlockLog) CompleteBlock(tenantID, sessionID, requestID string, status ArtifactStatus,
	upstreamResponse, clientResponse BodyRefHash, receipts []TransformReceipt) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[blockKey(tenantID, sessionID, requestID)]
	if !ok {
		return fmt.Errorf("%w: no turn block for request %s", ErrInvalidMutation, requestID)
	}
	entry.Block.Status = status
	entry.Block.UpstreamResponseRef = upstreamResponse
	entry.Block.UpstreamResponseHash = upstreamResponse
	entry.Block.ClientResponseRef = clientResponse
	entry.Block.ClientResponseHash = clientResponse
	if len(receipts) > 0 {
		entry.Block.TransformChain = append(entry.Block.TransformChain, receipts...)
	}
	entry.Block.CompletedAt = time.Now()
	return nil
}

// Get returns the block and its attempt records (copies).
func (l *TurnBlockLog) Get(tenantID, sessionID, requestID string) (TurnArtifactBlock, []TurnAttemptRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[blockKey(tenantID, sessionID, requestID)]
	if !ok {
		return TurnArtifactBlock{}, nil, false
	}
	block := entry.Block
	attempts := append([]TurnAttemptRecord(nil), entry.Attempts...)
	block.TransformChain = append([]TransformReceipt(nil), entry.Block.TransformChain...)
	return block, attempts, true
}

// Blocks returns all blocks of a session ordered by TurnNo.
func (l *TurnBlockLog) Blocks(tenantID, sessionID string) []TurnArtifactBlock {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []TurnArtifactBlock
	for _, key := range l.order {
		entry, ok := l.entries[key]
		if !ok {
			continue
		}
		b := entry.Block
		if b.TenantID == tenantID && b.SessionID == sessionID {
			b.TransformChain = append([]TransformReceipt(nil), entry.Block.TransformChain...)
			out = append(out, b)
		}
	}
	// stable sort by TurnNo (insertion order breaks ties)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].TurnNo < out[j-1].TurnNo; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Len returns the number of stored blocks.
func (l *TurnBlockLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *TurnBlockLog) evictLocked() {
	for len(l.order) >= l.capacity {
		oldest := l.order[0]
		l.order = l.order[1:]
		delete(l.entries, oldest)
	}
}
