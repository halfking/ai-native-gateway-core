package v2

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionCacheV2 is the V2 cache architecture that reads from session_turns
//
// Cache Levels:
//
//	L0: Turn delta storage (incremental messages in session_bodies)
//	L1: Compression metadata cache (in-memory LRU, no full body)
//	L2: Governance cache (Redis, verdicts only)
//	L3: Cold start (read from session_turns, not request_logs)
//
// This replaces the legacy SessionCache which reads from request_logs.
type SessionCacheV2 struct {
	l1 *CompressionMetaCache
	l2 *RedisGovernanceCache
	l3 *SessionTurnsReader

	db *pgxpool.Pool
}

// NewSessionCacheV2 creates a new V2 cache instance
func NewSessionCacheV2(db *pgxpool.Pool, redisAddr string) *SessionCacheV2 {
	return &SessionCacheV2{
		l1: NewCompressionMetaCache(1024),         // 1024 sessions in memory
		l2: NewRedisGovernanceCache(redisAddr, 0), // Use default TTL
		l3: NewSessionTurnsReader(db),
		db: db,
	}
}

// SessionStateV2 represents cached session state in V2 architecture
//
// This is lighter than legacy SessionState because it doesn't store
// full message bodies - only metadata needed for compression decisions.
type SessionStateV2 struct {
	SessionID  string
	TenantID   string
	LastTurnNo int
	UpdatedAt  time.Time

	// Compression metadata (L1)
	CompressionMeta CompressionMeta

	// Governance metadata (L2)
	GovernanceMeta GovernanceMeta
}

// CompressionMeta stores compression-related metadata
type CompressionMeta struct {
	LastCompressedAt     time.Time
	SummaryMarker        string
	CompressedPrefixHash string // Fingerprint of compressed content
	TokenEstimate        int
	MsgCount             int
	Strategy             string
	ToolsHash            string
}

// GovernanceMeta stores governance-related metadata
type GovernanceMeta struct {
	LastInjectionVerdict string
	LastOutputVerdict    string
	AuditedAt            time.Time
	SensitiveDetected    bool
}

// Get retrieves session state from cache hierarchy (L1 → L2 → L3)
func (c *SessionCacheV2) Get(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
	// Try L1 (in-memory)
	if state := c.l1.Get(tenantID, sessionID); state != nil {
		slog.DebugContext(ctx, "cache v2 l1 hit", "session_id", sessionID)
		return state, nil
	}

	// Try L2 (Redis governance cache)
	govMeta, err := c.l2.Get(ctx, tenantID, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "cache v2 l2 miss", "session_id", sessionID, "error", err)
	}

	// Try L3 (database cold start)
	state, err := c.l3.LoadState(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("cache v2 l3 load failed: %w", err)
	}

	// Populate governance metadata from L2 if available
	if govMeta != nil {
		state.GovernanceMeta = *govMeta
	}

	// Warm up L1
	c.l1.Set(state)

	slog.DebugContext(ctx, "cache v2 l3 loaded", "session_id", sessionID)
	return state, nil
}

// Set updates cache at all levels
func (c *SessionCacheV2) Set(ctx context.Context, state *SessionStateV2) error {
	// Update L1 (in-memory)
	c.l1.Set(state)

	// Update L2 (governance metadata)
	err := c.l2.Set(ctx, state.TenantID, state.SessionID, &state.GovernanceMeta)
	if err != nil {
		slog.WarnContext(ctx, "cache v2 l2 set failed", "session_id", state.SessionID, "error", err)
	}

	return nil
}

// Invalidate removes session from all cache levels
func (c *SessionCacheV2) Invalidate(ctx context.Context, tenantID, sessionID string) error {
	c.l1.Delete(tenantID, sessionID)
	return c.l2.Delete(ctx, tenantID, sessionID)
}

// HasState reports whether any prior session state exists. It is the
// minimal surface the compression layer needs to decide "new session vs
// continuation" without pulling the full SessionStateV2. Implemented in
// terms of Get so L1/L2/L3 semantics stay consistent.
func (c *SessionCacheV2) HasState(ctx context.Context, tenantID, sessionID string) (bool, error) {
	state, err := c.Get(ctx, tenantID, sessionID)
	if err != nil {
		return false, err
	}
	return state != nil, nil
}

// ─────────────────────────────────────────────────────────────
// L1: CompressionMetaCache (in-memory LRU)
// ─────────────────────────────────────────────────────────────

// CompressionMetaCache is an in-memory LRU cache for compression metadata
//
// Unlike legacy cache, this does NOT store full message bodies.
// It only stores metadata needed for compression decisions.
type CompressionMetaCache struct {
	mu       sync.RWMutex
	capacity int
	items    map[string]*cacheEntry
	lru      *lruList
}

type cacheEntry struct {
	state *SessionStateV2
	node  *lruNode
}

type lruNode struct {
	key  string
	prev *lruNode
	next *lruNode
}

type lruList struct {
	head *lruNode
	tail *lruNode
	size int
}

// NewCompressionMetaCache creates a new in-memory cache
// 2026-08-06 FIX (P2-3): Initialize LRU list head/tail pointers eagerly
// to avoid race condition in concurrent addToFront calls.
func NewCompressionMetaCache(capacity int) *CompressionMetaCache {
	lru := &lruList{
		head: &lruNode{},
		tail: &lruNode{},
	}
	// Eagerly initialize head/tail pointers to avoid lazy init race
	lru.head.next = lru.tail
	lru.tail.prev = lru.head

	return &CompressionMetaCache{
		capacity: capacity,
		items:    make(map[string]*cacheEntry),
		lru:      lru,
	}
}

// Get retrieves state from L1 cache
func (c *CompressionMetaCache) Get(tenantID, sessionID string) *SessionStateV2 {
	key := cacheKey(tenantID, sessionID)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[key]
	if !ok {
		return nil
	}

	// Move to front (most recently used)
	c.lru.moveToFront(entry.node)

	return entry.state
}

// Set stores state in L1 cache
func (c *CompressionMetaCache) Set(state *SessionStateV2) {
	key := cacheKey(state.TenantID, state.SessionID)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if entry, ok := c.items[key]; ok {
		entry.state = state
		c.lru.moveToFront(entry.node)
		return
	}

	// Evict if at capacity
	if c.lru.size >= c.capacity {
		c.evictLRU()
	}

	// Add new entry
	node := &lruNode{key: key}
	c.lru.addToFront(node)

	c.items[key] = &cacheEntry{
		state: state,
		node:  node,
	}
}

// Delete removes entry from L1 cache
func (c *CompressionMetaCache) Delete(tenantID, sessionID string) {
	key := cacheKey(tenantID, sessionID)

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.items[key]; ok {
		c.lru.remove(entry.node)
		delete(c.items, key)
	}
}

// evictLRU evicts the least recently used entry
func (c *CompressionMetaCache) evictLRU() {
	if c.lru.size == 0 {
		return
	}

	// Remove from tail (least recently used)
	node := c.lru.tail.prev
	if node == c.lru.head {
		return
	}

	c.lru.remove(node)
	delete(c.items, node.key)
}

func cacheKey(tenantID, sessionID string) string {
	return tenantID + ":" + sessionID
}

// LRU list operations
// 2026-08-06 NOTE: init() is no longer called lazily; NewCompressionMetaCache
// initializes head/tail eagerly to avoid race condition.
func (l *lruList) init() {
	l.head.next = l.tail
	l.tail.prev = l.head
}

func (l *lruList) addToFront(node *lruNode) {
	// 2026-08-06 FIX (P2-3): Removed lazy init check - NewCompressionMetaCache
	// now initializes head/tail pointers eagerly.
	node.next = l.head.next
	node.prev = l.head
	l.head.next.prev = node
	l.head.next = node
	l.size++
}

func (l *lruList) moveToFront(node *lruNode) {
	l.remove(node)
	l.addToFront(node)
}

func (l *lruList) remove(node *lruNode) {
	if node.prev != nil {
		node.prev.next = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	}
	l.size--
}

// ─────────────────────────────────────────────────────────────
// L3: SessionTurnsReader (cold start from database)
// ─────────────────────────────────────────────────────────────

// SessionTurnsReader loads session state from session_turns table
//
// This replaces the legacy reader which loaded from request_logs.
type SessionTurnsReader struct {
	db *pgxpool.Pool
}

// NewSessionTurnsReader creates a new database reader
func NewSessionTurnsReader(db *pgxpool.Pool) *SessionTurnsReader {
	return &SessionTurnsReader{db: db}
}

// LoadState loads session state from session_turns (last turn)
func (r *SessionTurnsReader) LoadState(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
	query := `
		SELECT 
			turn_no, ts,
			compression_strategy, compression_meta,
			COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0),
			COALESCE(injection_verdict, 'skip'),
			COALESCE(output_verdict, 'skip')
		FROM gateway.session_turns
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no DESC
		LIMIT 1
	`

	var state SessionStateV2
	var compressionMetaJSON []byte
	var promptTokens, completionTokens int
	var strategy string

	err := r.db.QueryRow(ctx, query, tenantID, sessionID).Scan(
		&state.LastTurnNo, &state.UpdatedAt,
		&strategy, &compressionMetaJSON,
		&promptTokens, &completionTokens,
		&state.GovernanceMeta.LastInjectionVerdict,
		&state.GovernanceMeta.LastOutputVerdict,
	)

	if err != nil {
		return nil, fmt.Errorf("load session state: %w", err)
	}

	state.SessionID = sessionID
	state.TenantID = tenantID
	state.CompressionMeta.Strategy = strategy
	state.CompressionMeta.TokenEstimate = promptTokens + completionTokens

	// Parse compression_meta JSON if present
	if len(compressionMetaJSON) > 0 {
		// TODO: Parse compression_meta fields
		// For now, we have the basic structure
	}

	return &state, nil
}
