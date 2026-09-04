package v2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/monitoring"
	"github.com/kaixuan/llm-gateway-go/storage"
)

// SessionCacheV2 is the V2 cache architecture that reads from session_turns
//
// 层级链路（Task 3.1 双模式存储架构，读路径 L1 → L1.5 → L2 → L3）：
//
//	L0:   Turn delta storage (incremental messages in session_bodies)
//	L1:   CompressionMetaCache — 进程内 LRU，只存压缩元数据（无完整 body）
//	L1.5: FileCache — 本地磁盘上的 SessionStateV2 快照，进程重启后仍可命中；
//	      仅 lite 模式在读路径上使用（full 模式即使注入了 l1_5 也不读它）
//	L2:   RedisGovernanceCache — Redis 治理元数据（verdicts only）；
//	      仅 full 模式使用，lite 模式跳过（lite = SQLite + File + Memory，无 Redis）
//	L3:   SessionTurnsReader — 冷启动回源（读 session_turns，不是 request_logs）
//
// 存储模式（storage.StorageMode，经 effectiveMode 归一化）：
//   - full（默认；零值与未知值都归一化为 full）：读 L1→L2→L3，写 L1+L2，
//     L3 命中后回填 L1 与 L2。与历史 NewSessionCacheV2 行为等价。
//   - lite：读 L1→L1.5→L3，写 L1+L1.5，L3 命中后回填 L1 与 L1.5，不触碰 L2。
//
// Invalidate/Delete 与模式无关：只要对应层非空就逐层失效（L1、L1.5、L2），
// 保证模式切换后不会有任何一层残留脏数据。
//
// This replaces the legacy SessionCache which reads from request_logs.
type SessionCacheV2 struct {
	l1   *CompressionMetaCache
	l1_5 *FileCache            // L1.5 本地文件缓存（lite 模式），nil 表示未装配
	l2   *RedisGovernanceCache // L2 Redis 治理缓存（full 模式），nil 表示未装配
	l3   *SessionTurnsReader

	mode storage.StorageMode // 零值视为 full（见 effectiveMode）

	db *pgxpool.Pool
}

// NewSessionCacheV2 creates a new V2 cache instance
//
// Parameters:
//   - db: PG pool for l3 (session_turns reader)
//   - redisAddr: Redis server address (for l2 governance cache)
//   - redisDB: Redis logical database index (passed through to l2).
//     2026-08-25: 调用方必须传入 cfg.RedisDB (== LLM_GATEWAY_REDIS_DB), 避免
//     硬编码 0 污染 PMS 共享 db0.
func NewSessionCacheV2(db *pgxpool.Pool, redisAddr string, redisDB int) *SessionCacheV2 {
	return &SessionCacheV2{
		l1: NewCompressionMetaCache(1024),                  // 1024 sessions in memory
		l2: NewRedisGovernanceCache(redisAddr, 0, redisDB), // Use default TTL
		l3: NewSessionTurnsReader(db),
		db: db,
	}
}

// NewSessionCacheV2WithMode is the mode-aware assembly entry point for the
// dual-mode storage architecture (Task 3.1). It reuses NewSessionCacheV2 and
// then applies mode + L1.5 wiring, so the legacy constructor stays untouched.
//
//   - mode == StorageModeLite：l2 置空（lite 部署无 Redis），l1_5 使用 fileCache
//     （可为 nil，此时 lite 退化为 L1 → L3）。
//   - mode == StorageModeFull 或零值：行为与 NewSessionCacheV2 等价；fileCache
//     可为 nil（full 模式不使用 L1.5）。
func NewSessionCacheV2WithMode(db *pgxpool.Pool, redisAddr string, redisDB int, mode storage.StorageMode, fileCache *FileCache) *SessionCacheV2 {
	c := NewSessionCacheV2(db, redisAddr, redisDB)
	if c == nil {
		return nil
	}
	c.SetFileCache(fileCache, mode)
	return c
}

// SetFileCache 装配（或替换）L1.5 文件缓存并切换存储模式。仅供启动装配阶段
// 调用：它不加锁地改写 c 的字段，不能与在途请求并发。
// lite 模式会同时摘除 L2（Redis），保证 lite 部署不产生任何 Redis 访问。
func (c *SessionCacheV2) SetFileCache(fc *FileCache, mode storage.StorageMode) {
	if c == nil {
		return
	}
	c.mode = mode
	c.l1_5 = fc
	if c.effectiveMode() == storage.StorageModeLite {
		c.l2 = nil
	}
}

// effectiveMode 归一化存储模式：零值与未知值都视为 full，保证由
// NewSessionCacheV2 构造（mode 未设置）的缓存保持历史 full 语义。
func (c *SessionCacheV2) effectiveMode() storage.StorageMode {
	if c == nil || c.mode != storage.StorageModeLite {
		return storage.StorageModeFull
	}
	return c.mode
}

// Close releases the underlying L2 Redis connection pool. Safe to call on a
// cache whose L2 is disabled (nil client). Called during gateway shutdown.
func (c *SessionCacheV2) Close() error {
	if c == nil || c.l2 == nil {
		return nil
	}
	return c.l2.Close()
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

// CompressionMeta stores compression-related metadata.
// JSON names intentionally mirror the compression_meta payload emitted by the
// gateway so cold-start recovery preserves the same state as the hot cache.
type CompressionMeta struct {
	LastCompressedAt     time.Time
	RecentlyCompressedAt time.Time
	SummaryMarker        string
	CompressedPrefixHash string // Fingerprint of compressed content
	TokenEstimate        int
	MsgCount             int
	Strategy             string
	ToolsHash            string
	// Recovery metadata is body-free and schema-tolerant. These fields carry
	// coordinates and opaque hash/ref records only; summary plaintext is never
	// persisted in V2.
	CutMarker              map[string]interface{}
	PreSanitizeOffsetRange []int
	AlignmentMap           []map[string]interface{}
	SanitizeMapRef         string
	SanitizeMessageRefs    []map[string]interface{}
}

// GovernanceMeta stores governance-related metadata
type GovernanceMeta struct {
	LastInjectionVerdict string
	LastOutputVerdict    string
	AuditedAt            time.Time
	SensitiveDetected    bool
}

// Get retrieves session state from cache hierarchy
// (L1 → [lite: L1.5] → [full: L2] → L3, backfills warmer tiers on L3 hit)
func (c *SessionCacheV2) Get(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
	if c == nil {
		return nil, nil
	}
	if c.l1 != nil {
		if state := c.l1.Get(tenantID, sessionID); state != nil {
			monitoring.Default().RecordL1Hit()
			slog.DebugContext(ctx, "cache v2 l1 hit", "session_id", sessionID)
			return state, nil
		}
		monitoring.Default().RecordL1Miss()
	}

	// L1.5 (local file snapshot, lite mode only). 未命中/过期/损坏统一返回包装
	// errCacheMiss 的错误，按缓存未命中继续回源；其他错误记日志后同样放行
	// （缓存层 fail-open，绝不阻断主链路）。
	if c.effectiveMode() == storage.StorageModeLite && c.l1_5 != nil {
		if state, err := c.l1_5.Get(tenantID, sessionID); err == nil && state != nil {
			monitoring.Default().RecordL15Hit()
			if c.l1 != nil {
				c.l1.Set(state)
			}
			slog.DebugContext(ctx, "cache v2 l1.5 hit", "session_id", sessionID)
			return state, nil
		} else {
			monitoring.Default().RecordL15Miss()
			if err != nil && !errors.Is(err, errCacheMiss) {
				slog.WarnContext(ctx, "cache v2 l1.5 get failed", "session_id", sessionID, "error", err)
			}
		}
	}

	// L2 (Redis governance metadata, full mode only; lite skips Redis entirely).
	var govMeta *GovernanceMeta
	if c.effectiveMode() == storage.StorageModeFull && c.l2 != nil {
		var err error
		govMeta, err = c.l2.Get(ctx, tenantID, sessionID)
		if govMeta != nil {
			monitoring.Default().RecordL2Hit()
		} else {
			monitoring.Default().RecordL2Miss()
		}
		if err != nil {
			slog.WarnContext(ctx, "cache v2 l2 miss", "session_id", sessionID, "error", err)
		}
	}
	if c.l3 == nil {
		return nil, nil
	}

	l3Start := time.Now()
	state, err := c.l3.LoadState(ctx, tenantID, sessionID)
	monitoring.Default().RecordL3Query(time.Since(l3Start))
	if err != nil {
		return nil, fmt.Errorf("cache v2 l3 load failed: %w", err)
	}
	if state == nil {
		slog.DebugContext(ctx, "cache v2 l3 no prior state", "session_id", sessionID)
		return nil, nil
	}
	if govMeta != nil {
		state.GovernanceMeta = *govMeta
	}
	if c.l1 != nil {
		c.l1.Set(state)
	}
	// 回填更热的层，让下一个请求不必再冷启动：
	//   lite + l1_5 → 回填 L1.5；full + l2 → 回填 L2。
	if c.effectiveMode() == storage.StorageModeLite && c.l1_5 != nil {
		if err := c.l1_5.Set(state); err != nil {
			slog.WarnContext(ctx, "cache v2 l1.5 backfill failed", "session_id", sessionID, "error", err)
		}
	}
	if c.effectiveMode() == storage.StorageModeFull && c.l2 != nil {
		if err := c.l2.Set(ctx, state.TenantID, state.SessionID, &state.GovernanceMeta); err != nil {
			slog.WarnContext(ctx, "cache v2 l2 backfill failed", "session_id", sessionID, "error", err)
		}
	}
	slog.DebugContext(ctx, "cache v2 l3 loaded", "session_id", sessionID)
	return state, nil
}

// Set updates the cache at the tiers selected by mode:
// full → L1 + L2(Redis 治理元数据)；lite → L1 + L1.5(文件快照)。
// A nil state is a no-op (see CompressionMetaCache.Set) rather than a
// nil-deref on state.TenantID. 缓存层 fail-open：下层写失败只记日志。
func (c *SessionCacheV2) Set(ctx context.Context, state *SessionStateV2) error {
	if c == nil || state == nil {
		return nil
	}
	if c.l1 != nil {
		c.l1.Set(state)
	}
	if c.effectiveMode() == storage.StorageModeLite {
		if c.l1_5 != nil {
			if err := c.l1_5.Set(state); err != nil {
				slog.WarnContext(ctx, "cache v2 l1.5 set failed", "session_id", state.SessionID, "error", err)
			}
		}
		return nil
	}
	if c.l2 == nil {
		return nil
	}
	err := c.l2.Set(ctx, state.TenantID, state.SessionID, &state.GovernanceMeta)
	if err != nil {
		slog.WarnContext(ctx, "cache v2 l2 set failed", "session_id", state.SessionID, "error", err)
	}
	return nil
}

// Invalidate removes the session from every non-nil cache tier (L1, L1.5, L2).
// 失效与 mode 无关：残留任何一层都会让下一次 Get 读到已被删除的状态，
// 因此逐层尽力失效；L1.5 失败只记日志（fail-open），L2 的错误照历史语义返回。
func (c *SessionCacheV2) Invalidate(ctx context.Context, tenantID, sessionID string) error {
	if c == nil {
		return nil
	}
	if c.l1 != nil {
		c.l1.Delete(tenantID, sessionID)
	}
	if c.l1_5 != nil {
		if err := c.l1_5.Delete(tenantID, sessionID); err != nil {
			slog.WarnContext(ctx, "cache v2 l1.5 delete failed", "session_id", sessionID, "error", err)
		}
	}
	if c.l2 == nil {
		return nil
	}
	return c.l2.Delete(ctx, tenantID, sessionID)
}

// CompressionMetadata returns the compression portion of the current V2 state
// as a JSON-shaped map. It is intentionally small so compression can consume
// it without importing this package and creating an import cycle.
func (c *SessionCacheV2) CompressionMetadata(ctx context.Context, tenantID, sessionID string) (map[string]any, error) {
	state, err := c.Get(ctx, tenantID, sessionID)
	if err != nil || state == nil {
		return nil, err
	}
	meta := state.CompressionMeta
	return map[string]any{
		"last_compressed_at":        meta.LastCompressedAt,
		"recently_compressed_at":    meta.RecentlyCompressedAt,
		"summary_marker":            meta.SummaryMarker,
		"compressed_prefix_hash":    meta.CompressedPrefixHash,
		"token_estimate":            meta.TokenEstimate,
		"msg_count":                 meta.MsgCount,
		"strategy":                  meta.Strategy,
		"tools_hash":                meta.ToolsHash,
		"cut_marker":                meta.CutMarker,
		"pre_sanitize_offset_range": meta.PreSanitizeOffsetRange,
		"alignment_map":             meta.AlignmentMap,
		"sanitize_map_ref":          meta.SanitizeMapRef,
		"sanitize_message_refs":     meta.SanitizeMessageRefs,
	}, nil
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
	ttl      time.Duration
	items    map[string]*cacheEntry
	lru      *lruList
}

type cacheEntry struct {
	state     *SessionStateV2
	node      *lruNode
	expiresAt time.Time
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
	return newCompressionMetaCache(capacity, defaultGovernanceTTL)
}

func newCompressionMetaCache(capacity int, ttl time.Duration) *CompressionMetaCache {
	if ttl <= 0 {
		ttl = defaultGovernanceTTL
	}
	lru := &lruList{
		head: &lruNode{},
		tail: &lruNode{},
	}
	// Eagerly initialize head/tail pointers to avoid lazy init race
	lru.head.next = lru.tail
	lru.tail.prev = lru.head

	return &CompressionMetaCache{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[string]*cacheEntry),
		lru:      lru,
	}
}

func cloneSessionStateV2(state *SessionStateV2) *SessionStateV2 {
	if state == nil {
		return nil
	}
	clone := *state
	meta := state.CompressionMeta
	clone.CompressionMeta = meta
	clone.CompressionMeta.PreSanitizeOffsetRange = append([]int(nil), meta.PreSanitizeOffsetRange...)
	clone.CompressionMeta.AlignmentMap = cloneMapSlice(meta.AlignmentMap)
	clone.CompressionMeta.SanitizeMessageRefs = cloneMapSlice(meta.SanitizeMessageRefs)
	clone.CompressionMeta.CutMarker = cloneMap(meta.CutMarker)
	return &clone
}

func cloneMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	// Compression metadata is JSON-shaped and may contain nested arrays/maps.
	// Round-tripping it gives callers an actually independent value tree instead
	// of the shallow copy that previously aliased nested provenance records.
	raw, err := json.Marshal(src)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func cloneMapSlice(src []map[string]interface{}) []map[string]interface{} {
	if src == nil {
		return nil
	}
	out := make([]map[string]interface{}, len(src))
	for i, value := range src {
		out[i] = cloneMap(value)
	}
	return out
}

// Get retrieves state from L1 cache.
//
// Always returns an independent copy of the cached SessionStateV2 so callers
// cannot mutate the L1 entry. Compression metadata contains maps/slices, so
// those nested values are cloned as well.
func (c *CompressionMetaCache) Get(tenantID, sessionID string) *SessionStateV2 {
	key := cacheKey(tenantID, sessionID)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[key]
	if !ok {
		return nil
	}
	if !entry.expiresAt.IsZero() && !time.Now().Before(entry.expiresAt) {
		c.lru.remove(entry.node)
		delete(c.items, key)
		return nil
	}

	// Move to front (most recently used)
	c.lru.moveToFront(entry.node)

	// Return a copy; never expose the internal pointer or nested metadata.
	return cloneSessionStateV2(entry.state)
}

// Set stores state in L1 cache. A nil state is ignored: LoadState returns
// (nil, nil) for a brand-new session, and a nil-deref here took down the
// whole chat path on 2026-08-07. Callers should skip the warm-up entirely,
// but this guard keeps a nil from being fatal.
//
// A copy of the state is stored so later mutations by the caller cannot
// silently corrupt the cached entry.
func (c *CompressionMetaCache) Set(state *SessionStateV2) {
	if c == nil || state == nil {
		return
	}
	key := cacheKey(state.TenantID, state.SessionID)

	// Copy before locking so the closure is short.
	cp := cloneSessionStateV2(state)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if entry, ok := c.items[key]; ok {
		entry.state = cp
		entry.expiresAt = time.Now().Add(c.ttl)
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
		state:     cp,
		node:      node,
		expiresAt: time.Now().Add(c.ttl),
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
	// Length-prefix the tenant component so delimiter-containing identifiers
	// cannot collide across tenant/session tuples.
	return fmt.Sprintf("%d:%s%s", len(tenantID), tenantID, sessionID)
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

// sessionTurnsDB is the narrow database seam used by the cold-start reader.
// Keeping it separate from *pgxpool.Pool makes the latest-marker query
// testable without requiring a live database.
type sessionTurnsDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SessionTurnsReader loads session state from session_turns table
//
// This replaces the legacy reader which reads from request_logs.
type SessionTurnsReader struct {
	db sessionTurnsDB
}

// NewSessionTurnsReader creates a new database reader.
func NewSessionTurnsReader(db *pgxpool.Pool) *SessionTurnsReader {
	if db == nil {
		return &SessionTurnsReader{}
	}
	return newSessionTurnsReader(db)
}

func newSessionTurnsReader(db sessionTurnsDB) *SessionTurnsReader {
	return &SessionTurnsReader{db: db}
}

// LoadState loads session state from session_turns (last turn)
func (r *SessionTurnsReader) LoadState(ctx context.Context, tenantID, sessionID string) (*SessionStateV2, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	// Governance/token fields come from the latest turn, while compression
	// metadata comes from the latest turn that still carries a cut marker.
	// A later ordinary turn must not erase the durable recovery descriptor on
	// a cold restart; the latest outbound body is read independently by V2.
	query := `
		WITH latest AS (
			SELECT turn_no, ts, compression_strategy, compression_meta,
			       COALESCE(prompt_tokens, 0) AS prompt_tokens,
			       COALESCE(completion_tokens, 0) AS completion_tokens,
			       COALESCE(injection_verdict, 'skip') AS injection_verdict,
			       COALESCE(output_verdict, 'skip') AS output_verdict
			FROM public.session_turns_with_current_month
			WHERE tenant_id = $1 AND session_id = $2
			ORDER BY turn_no DESC, ts DESC
			LIMIT 1
		), marker_turn AS (
			SELECT compression_meta
			FROM public.session_turns_with_current_month
			WHERE tenant_id = $1 AND session_id = $2
			  AND compression_meta IS NOT NULL
			  AND compression_meta ? 'cut_marker'
			ORDER BY turn_no DESC, ts DESC
			LIMIT 1
		)
		SELECT latest.turn_no, latest.ts,
		       latest.compression_strategy,
		       COALESCE(marker_turn.compression_meta, latest.compression_meta),
		       latest.prompt_tokens, latest.completion_tokens,
		       latest.injection_verdict, latest.output_verdict
		FROM latest
		LEFT JOIN marker_turn ON TRUE
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

	// A genuinely new session has no rows yet. This is not an error: HasState
	// relies on LoadState returning (nil, nil) here so the caller classifies it
	// as "no prior state" rather than a hard failure that forces V1 fallback.
	// Without this, every new session would hit the err != nil branch in
	// tryLoadV2State and bypass V2 entirely. Mirrors TurnReader.LoadLatestOutbound.
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load session state: %w", err)
	}

	state.SessionID = sessionID
	state.TenantID = tenantID
	state.CompressionMeta.Strategy = strategy
	state.CompressionMeta.TokenEstimate = promptTokens + completionTokens

	// Parse compression_meta JSON if present. The payload is intentionally
	// decoded best-effort: malformed optional metadata must not hide a valid
	// turn or prevent the next request from using its outbound snapshot.
	if len(compressionMetaJSON) > 0 && string(compressionMetaJSON) != "null" {
		applyCompressionMeta(&state.CompressionMeta, compressionMetaJSON)
	}

	return &state, nil
}

func applyCompressionMeta(dst *CompressionMeta, raw []byte) {
	if dst == nil || len(raw) == 0 || string(raw) == "null" {
		return
	}
	var meta struct {
		LastCompressedAt       time.Time                `json:"last_compressed_at"`
		RecentlyCompressedAt   time.Time                `json:"recently_compressed_at"`
		SummaryMarker          string                   `json:"summary_marker"`
		CompressedPrefixHash   string                   `json:"compressed_prefix_hash"`
		TokenEstimate          int                      `json:"token_estimate"`
		MsgCount               int                      `json:"msg_count"`
		Strategy               string                   `json:"strategy"`
		ToolsHash              string                   `json:"tools_hash"`
		CutMarker              map[string]interface{}   `json:"cut_marker"`
		PreSanitizeOffsetRange []int                    `json:"pre_sanitize_offset_range"`
		AlignmentMap           []map[string]interface{} `json:"alignment_map"`
		SanitizeMapRef         string                   `json:"sanitize_map_ref"`
		SanitizeMessageRefs    []map[string]interface{} `json:"sanitize_message_refs"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return
	}
	dst.LastCompressedAt = meta.LastCompressedAt
	dst.RecentlyCompressedAt = meta.RecentlyCompressedAt
	dst.SummaryMarker = meta.SummaryMarker
	dst.CompressedPrefixHash = meta.CompressedPrefixHash
	dst.ToolsHash = meta.ToolsHash
	if meta.TokenEstimate > 0 {
		dst.TokenEstimate = meta.TokenEstimate
	}
	if meta.MsgCount > 0 {
		dst.MsgCount = meta.MsgCount
	}
	if meta.Strategy != "" {
		dst.Strategy = meta.Strategy
	}
	if len(meta.CutMarker) > 0 {
		dst.CutMarker = meta.CutMarker
		if len(meta.PreSanitizeOffsetRange) != 2 {
			if rawPair, ok := meta.CutMarker["pre_sanitize_offset_range"]; ok {
				if encoded, err := json.Marshal(rawPair); err == nil {
					_ = json.Unmarshal(encoded, &meta.PreSanitizeOffsetRange)
				}
			}
		}
	}
	if len(meta.PreSanitizeOffsetRange) == 2 {
		dst.PreSanitizeOffsetRange = append([]int(nil), meta.PreSanitizeOffsetRange...)
	}
	if len(meta.AlignmentMap) > 0 {
		dst.AlignmentMap = meta.AlignmentMap
	}
	if meta.SanitizeMapRef != "" {
		dst.SanitizeMapRef = meta.SanitizeMapRef
	}
	if len(meta.SanitizeMessageRefs) > 0 {
		dst.SanitizeMessageRefs = meta.SanitizeMessageRefs
	}
}
