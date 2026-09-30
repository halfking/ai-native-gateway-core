// Package sanitize - smart_sani_guard.go
//
// SmartSaniGuard 主链路集成组件：
//   - SanitizeInputMiddleware: 在真实 chatHandler 之前对请求体做可逆脱敏
//   - SanitizeRestoreInterceptor: 接入 ResponseInterceptor 链，在输出安全检查
//     之后把占位符还原为真实敏感值，再返回给客户端
//
// 设计要点（对齐用户需求"先安全检查，再还原"）：
//  1. 输入侧：检测敏感信息 → 替换为 {SENSITIVE:type:index} 占位符
//     → 占位符→原始值映射持久化到 Redis（会话级，TTL=30分钟）
//  2. 上游 LLM 只看到脱敏后的占位符
//  3. 响应侧：OutputComplianceInterceptor 先对含占位符的文本做安全检查
//     （敏感信息不暴露给安全检查服务）
//  4. SanitizeRestoreInterceptor 再还原占位符为真实值返回给用户
//
// 跨轮次占位符冲突：占位符索引在会话内连续递增（每类从1开始），
// 通过 Redis 中保存的每类计数偏移量，保证不同轮次不撞号。
package sanitize

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/redis/go-redis/v9"
)

// RestoreInterceptorName 是还原拦截器在链中的名字（日志/观测）
const RestoreInterceptorName = "sanitize_restore"

// Keep the sanitizer's pre-handler read cap aligned with streaming.maxBodySize.
// The sanitizer runs first and must not allocate an unbounded request body.
const maxSanitizeBodySize = 128 << 20
const defaultOffsetLeaseTTL = 15 * time.Second

var errSanitizeBodyTooLarge = errors.New("sanitize request body too large")

// SanitizeInputMiddleware 在真实 chatHandler 之前对请求体做可逆脱敏。
//
// 作用：把敏感信息（手机号/身份证/邮箱等）替换为占位符，确保：
//   - 上游 LLM 与日志/审计只看到脱敏文本
//   - 占位符→原始值映射存入 Redis（会话级），供响应侧还原
//
// 处理流程：
//  1. 解析 OpenAI chat.completions 请求体
//  2. 遍历 messages，对每条 user/system 消息做脱敏
//  3. 把脱敏后的请求体重写回 r.Body
//  4. 把 SanitizeMap 存入 Redis（key: session:sanitize:{sessionID}）
//
// 占位符索引使用会话级偏移量（Redis 里记录每类已用最大值），
// 避免跨轮次撞号。
type SanitizeInputMiddleware struct {
	sanitizer *Sanitizer
	redis     *redis.Client
	ttl       time.Duration
	logger    *slog.Logger
	// getSessionID 从请求头提取会话ID（由调用方注入，便于测试）
	getSessionID func(r *http.Request) string
	// sessionLocks serializes offset read/allocate/write per (tenant, session)
	// on this middleware instance. Redis keys remain tenant scoped for
	// cross-process isolation; this lock only removes the in-process
	// read-modify-write race, so it is keyed by session rather than global —
	// a process-wide mutex would also serialize unrelated sessions together
	// with their Redis round trips and regex scan, capping gateway throughput
	// at one request per detect latency.
	sessionLocks sanitizeSessionLocks
	// offsetLeaseTTL bounds crash recovery; an owner renews while sanitizing.
	// Tests can shorten it to exercise slow detector and lease-loss paths.
	offsetLeaseTTL time.Duration
	// censorSink（706，可选）：把占位符→原始值映射双写 DB
	// （public.session_censors），Redis 降级为热缓存。nil 时仅 Redis。
	censorSink CensorSink
}

// SetCensorSink wires the optional DB double-write sink (migration 706).
// Call before serving; nil disables the double-write.
func (m *SanitizeInputMiddleware) SetCensorSink(sink CensorSink) {
	m.censorSink = sink
}

// NewSanitizeInputMiddleware 创建输入脱敏中间件。
// redis 为 nil 时退化为仅内存脱敏（不持久化，单轮可用）。
func NewSanitizeInputMiddleware(s *Sanitizer, redis *redis.Client, ttl time.Duration) (*SanitizeInputMiddleware, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitize middleware: %w", ErrNilSanitizer)
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &SanitizeInputMiddleware{
		sanitizer:      s,
		redis:          redis,
		ttl:            ttl,
		offsetLeaseTTL: defaultOffsetLeaseTTL,
		logger:         slog.Default().With("component", "sanitize_middleware"),
		// 与 chatHandler（domains/streaming/session_routing.go 的
		// SessionHeadersPriority）保持一致的 5 个候选 header 顺序。
		// 不读 body 里的 session_id：中间件先于 chatHandler 解析 session，
		// body 解析属于 chatHandler 的核心职责，且 body 里 session_id 可能
		// 出现在 messages content 里（被中间件当作 PII 替换掉），会让中间件
		// 误读自身内容 — 因此本中间件只接受 header 形式的 sessionID。
		getSessionID: func(r *http.Request) string {
			for _, header := range sessionIDHeaderPriority {
				if v := strings.TrimSpace(r.Header.Get(header)); v != "" {
					return v
				}
			}
			return ""
		},
	}, nil
}

// sanitizeSessionLocks is a keyed mutex: callers holding the same key run one
// at a time, callers on different keys run fully in parallel. Entries are
// reference counted and removed when the last holder leaves, so the map cannot
// grow with the number of distinct sessions the process has ever served.
type sanitizeSessionLocks struct {
	mu    sync.Mutex
	locks map[string]*sanitizeSessionLock
}

type sanitizeSessionLock struct {
	// ch carries a single slot. Sending acquires; receiving releases.
	ch   chan struct{}
	refs int
}

func (l *sanitizeSessionLocks) lock(key string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*sanitizeSessionLock)
	}
	entry, ok := l.locks[key]
	if !ok {
		entry = &sanitizeSessionLock{ch: make(chan struct{}, 1)}
		l.locks[key] = entry
	}
	// Count the holder BEFORE acquiring: the entry must outlive every waiter,
	// otherwise the last release could delete a key another goroutine is
	// already blocked on and a third caller would get a fresh lock.
	entry.refs++
	l.mu.Unlock()

	entry.ch <- struct{}{}
	return func() {
		<-entry.ch
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

// sessionIDHeaderPriority 与 chatHandler 一致的会话 ID header 候选。
// 顺序与重要性递减对齐：X-Gw-Session-Id > X-Session-Id >
// X-Conversation-Id > X-Chat-Session-Id > X-Thread-Id。
var sessionIDHeaderPriority = []string{
	"X-Gw-Session-Id",
	"X-Session-Id",
	"X-Conversation-Id",
	"X-Chat-Session-Id",
	"X-Thread-Id",
}

// envelopeSessionID reads only the top-level session_id field. It never scans
// message content, so a user mentioning "session_id" cannot change ownership.
func envelopeSessionID(body []byte) string {
	var envelope struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.SessionID)
}

// Wrap 返回一个 http.Handler 包装器。在真实 handler 之前完成脱敏。
// 读取、检测或映射持久化失败时停止请求，避免原始敏感文本流向上游。
func (m *SanitizeInputMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m == nil || m.sanitizer == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		sessionID := m.getSessionID(r)
		tenantID := strings.TrimSpace(authenticatedTenant(r.Context()))
		if tenantID == "" {
			tenantID = "_unknown"
		}
		// readBody 恢复 r.Body；只有确认不需要脱敏时才让原始 body 进入下游。
		body, err := readBody(r)
		if err != nil {
			m.logger.Warn("sanitize_middleware: read body failed",
				"error", err, "session_id", sessionID)
			status := http.StatusBadRequest
			if errors.Is(err, errSanitizeBodyTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			writeSanitizeFailure(w, r, status)
			return
		}
		if bodySessionID := envelopeSessionID(body); bodySessionID != "" {
			sessionID = bodySessionID
		}

		// 解析并脱敏。只有存在跨请求共享的会话状态（Redis 租约 + offset +
		// 映射表）时才需要加锁，且按 (租户, 会话) 键控：同一会话的并发
		// 轮次仍串行，不同会话互不阻塞。没有会话 ID 或没有 Redis 时，
		// offset 与映射表都是本次调用私有的，不加锁也不会有竞态。
		release := func() {}
		if m.redis != nil && sessionID != "" {
			release = m.sessionLocks.lock(sanitizeOffsetKey(tenantID, sessionID))
		}
		sanitizedBody, sm, messageRefs, err := m.sanitizeRequestBody(r.Context(), body, sessionID, tenantID, r.URL.Path)
		release()
		if err != nil {
			m.logger.Warn("sanitize_middleware: sanitize failed",
				"error", err, "session_id", sessionID)
			status := http.StatusServiceUnavailable
			if errors.Is(err, errInvalidSanitizeInput) {
				status = http.StatusBadRequest
			}
			writeSanitizeFailure(w, r, status)
			return
		}

		// 有脱敏内容 → 用脱敏后的 body 覆盖，并把映射表放入请求 Context。
		// 无脱敏内容时 r.Body 已由 readBody 恢复为原始 body，无需处理。
		if sanitizedBody != nil {
			r.Body = io.NopCloser(bytes.NewReader(sanitizedBody))
			// 脱敏改写后长度变化，同步 ContentLength（仅此一项；
			// Content-Length header 是 server 端 Go 解析时填入的，下游
			// 全部走 r.ContentLength 字段，不再回写 header）。
			// 与 armor middleware.withReplayedBody 保持一致。
			r.ContentLength = int64(len(sanitizedBody))
			*r = *r.WithContext(WithSanitizeMap(r.Context(), sm))
			// SC-1 (docs/修订0811/19): 同时把脱敏桥接信息放入 ctx，供
			// session compressor 写入 SessionState v8 的 SanitizeMapRef /
			// SanitizeStats，使三层缓存的 L3 脱敏字段不再悬空。
			info := buildSanitizeInfoForSession(tenantID, sessionID, sm)
			info.MessageRefs = messageRefs
			*r = *r.WithContext(compression.WithSanitizeInfo(r.Context(), info))
		}

		next.ServeHTTP(w, r)
	})
}

// sanitizeRequestBody 按客户端协议脱敏可见文本，并保留非文本多模态字段。
// 返回 (脱敏后的请求体, SanitizeMap)。无敏感信息时返回 (nil, 空map)。
func (m *SanitizeInputMiddleware) sanitizeRequestBody(ctx context.Context, body []byte, sessionID string, tenantID, path string) ([]byte, SanitizeMap, []compression.SanitizedMessageRef, error) {
	// 会话级偏移量（记录每类已用最大编号，保证跨轮次不撞号）
	// The Redis lock spans load, allocation, and an atomic, lease-checked
	// map+offset commit. A lost lease or Redis failure must stop dispatch:
	// otherwise a second process can reuse a placeholder for another value.
	lease, err := m.acquireOffsetsLock(ctx, sessionID, tenantID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acquire sanitize offsets: %w", err)
	}
	defer m.releaseOffsetsLock(ctx, lease)
	offset, err := m.loadOffsets(ctx, sessionID, tenantID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load sanitize offsets: %w", err)
	}
	// loadOffsets legitimately returns nil on a cache miss or when Redis is
	// disabled. Keep a local map so allocating placeholder indexes below is
	// safe in both modes.
	if offset == nil {
		offset = make(map[SensitiveType]int)
	}
	worker := requestInputSanitizer{
		ctx: ctx, sanitizer: m.sanitizer, offset: offset,
		mapping: make(SanitizeMap), usedCount: make(map[string]int),
	}
	newBody, messageRefs, err := worker.sanitizeEnvelope(body, path)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(worker.mapping) == 0 {
		return nil, nil, nil, nil
	}

	// 持久化映射表到 Redis（同时把每类本轮最大编号刷回 offset key）
	if m.redis != nil && sessionID != "" {
		if err := m.saveMapAndOffsets(ctx, lease, sessionID, worker.mapping, worker.usedCount, tenantID); err != nil {
			return nil, nil, nil, fmt.Errorf("persist sanitize mapping: %w", err)
		}
	}
	return newBody, worker.mapping, messageRefs, nil
}

// releaseOffsetsLockScript deletes the lock only when the value still matches
// our token — never someone else's lease after a TTL expiry.
var releaseOffsetsLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

var renewOffsetsLockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

type sanitizeOffsetLease struct {
	key, token string
	stop, done chan struct{}
}

// acquireOffsetsLock takes the per-session lock. Redis-backed sessions never
// proceed unlocked; the commit script verifies this lease before writing.
func (m *SanitizeInputMiddleware) acquireOffsetsLock(ctx context.Context, sessionID, tenantID string) (sanitizeOffsetLease, error) {
	if m.redis == nil || sessionID == "" {
		return sanitizeOffsetLease{}, nil
	}
	key := sanitizeOffsetKey(tenantID, sessionID) + ":lock"
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return sanitizeOffsetLease{}, err
	}
	tok := hex.EncodeToString(token[:])
	leaseTTL := m.offsetLeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultOffsetLeaseTTL
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return sanitizeOffsetLease{}, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		ok, err := m.redis.SetNX(ctx, key, tok, leaseTTL).Result()
		if err != nil {
			return sanitizeOffsetLease{}, err
		}
		if ok {
			lease := sanitizeOffsetLease{key: key, token: tok, stop: make(chan struct{}), done: make(chan struct{})}
			go m.renewOffsetsLease(ctx, lease, leaseTTL)
			return lease, nil
		}
	}
	return sanitizeOffsetLease{}, errors.New("sanitize offsets lock busy")
}

func (m *SanitizeInputMiddleware) renewOffsetsLease(ctx context.Context, lease sanitizeOffsetLease, ttl time.Duration) {
	defer close(lease.done)
	interval := ttl / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-lease.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			ok, err := renewOffsetsLockScript.Run(renewCtx, m.redis, []string{lease.key}, lease.token, ttl.Milliseconds()).Int()
			cancel()
			if err != nil || ok != 1 {
				m.logger.Warn("sanitize_middleware: offsets lease renewal stopped", "error", err, "key", lease.key)
				return
			}
		}
	}
}

func (m *SanitizeInputMiddleware) releaseOffsetsLock(ctx context.Context, lease sanitizeOffsetLease) {
	if lease.key == "" {
		return
	}
	close(lease.stop)
	<-lease.done
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := releaseOffsetsLockScript.Run(releaseCtx, m.redis, []string{lease.key}, lease.token).Err(); err != nil {
		m.logger.Warn("sanitize_middleware: release offsets lock failed", "error", err)
	}
}

// loadOffsets 从 Redis 加载每类已用最大编号作为偏移量。
//
// 偏移量单独存于 session:{sid}:sanitize:offsets（Redis Hash，
// field=类型字符串, value=本类型已用最大编号）。每次请求只读一个
// 紧凑 hash，不再扫描整个 sanitize map。
func (m *SanitizeInputMiddleware) loadOffsets(ctx context.Context, sessionID string, tenantIDs ...string) (map[SensitiveType]int, error) {
	if m.redis == nil || sessionID == "" {
		return nil, nil
	}
	tenantID := firstSanitizeTenant(tenantIDs)
	vals, err := redissafe.SafeHGetAll(ctx, m.redis, sanitizeOffsetKey(tenantID, sessionID))
	if err != nil {
		if !errors.Is(err, redissafe.ErrKeyNotFound) {
			return nil, err
		}
		return m.recoverOffsetsFromMapFields(ctx, sessionID, tenantID)
	}
	if len(vals) == 0 {
		return m.recoverOffsetsFromMapFields(ctx, sessionID, tenantID)
	}
	maxIdx := make(map[SensitiveType]int, len(vals))
	for tStr, v := range vals {
		idx, parseErr := strconv.Atoi(v)
		if parseErr != nil || idx <= 0 {
			return nil, fmt.Errorf("invalid sanitize offset %q=%q", tStr, v)
		}
		maxIdx[SensitiveType(tStr)] = idx
	}
	return maxIdx, nil
}

// recoverOffsetsFromMapFields prevents placeholder reuse when the compact
// offsets hash expired before the primary sanitize map. It inspects only Redis
// field names (placeholders), never the mapped plaintext values.
func (m *SanitizeInputMiddleware) recoverOffsetsFromMapFields(ctx context.Context, sessionID, tenantID string) (map[SensitiveType]int, error) {
	if m.redis == nil || sessionID == "" {
		return nil, nil
	}
	fields, err := m.redis.HKeys(ctx, sanitizeMapKey(tenantID, sessionID)).Result()
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, nil
	}
	maxIdx := make(map[SensitiveType]int)
	for _, field := range fields {
		placeholder, ok := ParsePlaceholder(field)
		if !ok || placeholder.Index <= maxIdx[placeholder.Type] {
			continue
		}
		maxIdx[placeholder.Type] = placeholder.Index
	}
	return maxIdx, nil
}

// commitSanitizeMapScript checks lease ownership and writes the map and
// offsets together. Expired workers cannot overwrite another worker's map.
var commitSanitizeMapScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return redis.error_reply('sanitize offsets lease lost')
end
local function hash_or_absent(key)
  local kind = redis.call('TYPE', key).ok
  return kind == 'none' or kind == 'hash'
end
if not hash_or_absent(KEYS[2]) or not hash_or_absent(KEYS[3]) then
  return redis.error_reply('sanitize map or offsets wrong type')
end
local legacy = ARGV[5] == '1'
if legacy and (not hash_or_absent(KEYS[4]) or not hash_or_absent(KEYS[5])) then
  return redis.error_reply('legacy sanitize map or offsets wrong type')
end
local map_count = tonumber(ARGV[3])
local offset_count = tonumber(ARGV[4])
local pos = 6
for i = 1, map_count do
  if redis.call('HEXISTS', KEYS[2], ARGV[pos]) == 1 then
    return redis.error_reply('sanitize placeholder already exists')
  end
  if legacy and redis.call('HEXISTS', KEYS[4], ARGV[pos]) == 1 then
    return redis.error_reply('legacy sanitize placeholder already exists')
  end
  pos = pos + 2
end
pos = 6
for i = 1, map_count do
  redis.call('HSET', KEYS[2], ARGV[pos], ARGV[pos + 1])
  if legacy then redis.call('HSET', KEYS[4], ARGV[pos], ARGV[pos + 1]) end
  pos = pos + 2
end
for i = 1, offset_count do
  redis.call('HSET', KEYS[3], ARGV[pos], ARGV[pos + 1])
  if legacy then redis.call('HSET', KEYS[5], ARGV[pos], ARGV[pos + 1]) end
  pos = pos + 2
end
local ttl_ms = tonumber(ARGV[2])
redis.call('PEXPIRE', KEYS[2], ttl_ms)
if offset_count > 0 then redis.call('PEXPIRE', KEYS[3], ttl_ms) end
if legacy then
  redis.call('PEXPIRE', KEYS[4], ttl_ms)
  if offset_count > 0 then redis.call('PEXPIRE', KEYS[5], ttl_ms) end
end
return 1
`)

// saveMapAndOffsets writes placeholder values and each type's high-water mark.
func (m *SanitizeInputMiddleware) saveMapAndOffsets(ctx context.Context, lease sanitizeOffsetLease, sessionID string, sm SanitizeMap, usedCount map[string]int, tenantIDs ...string) error {
	tenantID := firstSanitizeTenant(tenantIDs)
	if lease.key == "" || lease.token == "" {
		return errors.New("sanitize offsets lease required")
	}
	mapKey := sanitizeMapKey(tenantID, sessionID)
	ttlMS := m.ttl.Milliseconds()
	if ttlMS < 1 {
		ttlMS = 1
	}
	legacyFlag := "0"
	if tenantID == "_unknown" {
		legacyFlag = "1"
	}
	args := make([]any, 0, 5+2*len(sm)+2*len(usedCount))
	args = append(args, lease.token, ttlMS, len(sm), len(usedCount), legacyFlag)
	for ph, val := range sm {
		args = append(args, ph, val)
	}
	for kind, idx := range usedCount {
		args = append(args, kind, idx)
	}
	keys := []string{lease.key, mapKey, sanitizeOffsetKey(tenantID, sessionID), SanitizeRedisKey(sessionID), SanitizeOffsetRedisKey(sessionID)}
	if err := commitSanitizeMapScript.Run(ctx, m.redis, keys, args...).Err(); err != nil {
		return err
	}
	// DB double-write is audit-only and remains best effort after Redis commit.
	if m.censorSink != nil {
		entries := make([]CensorEntry, 0, len(sm))
		for ph, val := range sm {
			e := CensorEntry{Placeholder: ph, Original: val}
			if p, ok := ParsePlaceholder(ph); ok {
				e.SensitiveType = string(p.Type)
			}
			entries = append(entries, e)
		}
		m.censorSink.SaveCensorMappings(ctx, tenantID, sessionID, entries)
	}
	return nil
}

// SanitizeRestoreInterceptor 实现 response.ResponseInterceptor。
// 在输出安全检查（OutputComplianceInterceptor）之后执行，
// 从 Redis 读取会话级映射表，把占位符还原为真实敏感值。
//
// 顺序契约：本拦截器必须在 OutputComplianceInterceptor 之后注册，
// 确保安全检查先看到占位符，还原后再把真实值返回给用户。
//
// 流式响应（2026-08-07 P2 修复）：
//
//	此前 chatHandler 流式响应路径不调用 InterceptorChain.InterceptStreamChunk，
//	客户端拿到的 SSE chunk 是脱敏后的占位符文本，敏感值被「永久加密」在
//	Redis 里但用户看不到。本次升级：实现 chunk-level 还原，解析 SSE
//	data 行 → JSON 反序列化 → 在 choices[].delta.content / content_block_delta
//	等字段上做占位符替换 → 重新序列化 → 返回 ModifiedChunk。流式 chunk
//	在写入客户端前经 InterceptorChain 拦截（见 domains/streaming 的
//	interceptingStreamWriter 接入点），还原后用户看到真实值。
//
//	单个 SSE 事件内部的多次 Write 会由 streaming.interceptingStreamWriter
//	先组装完整后再调用本拦截器；跨完整事件的 placeholder 前缀由 request-local
//	StreamMeta.State 按协议 delta lane 暂存，且有 lane/字节上限。
//	Redis 映射缺失/读取失败时仍遮蔽未知完整 marker，但已知值无法恢复；
//	无法解析的事件若有待续 marker 尾片则阻断，避免原样泄漏脱离前缀的片段。
type SanitizeRestoreInterceptor struct {
	sanitizer *Sanitizer
	redis     *redis.Client
	ttl       time.Duration
	logger    *slog.Logger
}

const (
	streamRestoreStateKey   = "sanitize_restore"
	maxStreamRestoreLanes   = 64
	maxPlaceholderTailBytes = 256
)

// streamRestoreState is owned by one response StreamMeta, never by the shared
// interceptor. Carry is bounded both by lane count and by placeholder length.
// Once lane capacity is exhausted, the rest of that stream is blocked so a
// later delta cannot leak a suffix detached from its withheld prefix.
type streamRestoreState struct {
	mu      sync.Mutex
	tails   map[string]string
	blocked bool
}

// NewSanitizeRestoreInterceptor 创建还原拦截器。
func NewSanitizeRestoreInterceptor(s *Sanitizer, redis *redis.Client, ttl time.Duration) (*SanitizeRestoreInterceptor, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitize restore interceptor: %w", ErrNilSanitizer)
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &SanitizeRestoreInterceptor{
		sanitizer: s,
		redis:     redis,
		ttl:       ttl,
		logger:    slog.Default().With("component", "sanitize_restore"),
	}, nil
}

// Name 返回拦截器名称（日志用）。
func (it *SanitizeRestoreInterceptor) Name() string { return RestoreInterceptorName }

// InterceptNonStream 还原非流式响应中的占位符。
func (it *SanitizeRestoreInterceptor) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if it == nil || it.sanitizer == nil || req == nil || len(req.ResponseBody) == 0 {
		return nil, nil
	}
	if req.SessionID == "" {
		// Without a session we cannot load the mapping table, so nothing in
		// this body can be restored. A reserved marker must still not reach
		// the client (the stream wing blocks for the same reason); mask it
		// rather than pass the internal token through.
		if hasReservedPlaceholder(req.ResponseBody) {
			it.logger.WarnContext(ctx, "sanitize_restore: no session id, masking placeholders")
			return it.maskUnrecognizedBody(ctx, req, SanitizeMap{})
		}
		return nil, nil
	}

	sm, err := it.loadMap(ctx, req.SessionID, req.TenantID)
	if err != nil {
		it.logger.Warn("sanitize_restore: load map failed, mask placeholders",
			"error", err, "session_id", req.SessionID)
		// Fail closed for gateway placeholders even when the map is unavailable:
		// known values cannot be restored, but raw internal tokens must not leak.
		//
		// Native shapes (Messages / Responses) block outright: their restorers
		// rebuild structured tool arguments and content blocks, and masking
		// with an empty map would corrupt those payloads (native_restore_test
		// contract). Legacy chat choices keep the mask-and-pass behavior
		// (smart_sani_guard_test contract).
		if root := mustDecodeJSONMap(req.ResponseBody); root != nil &&
			(isNativeMessagesShape(root) || isNativeResponsesShape(root)) &&
			bytes.Contains(req.ResponseBody, []byte("{SENSITIVE:")) {
			return &response.InterceptResult{ShouldBlock: true}, nil
		}
		sm = SanitizeMap{}
	}
	if len(sm) == 0 && !hasReservedPlaceholder(req.ResponseBody) {
		return nil, nil
	}

	// === 验证占位符完整性（Phase 1 Task 1.1）===
	invalidPlaceholders := it.validatePlaceholders(ctx, req.ResponseBody, sm)
	if len(invalidPlaceholders) > 0 {
		it.logger.WarnContext(ctx, "sanitize_restore: detected invalid placeholders in LLM response",
			"invalid_placeholders", invalidPlaceholders,
			"invalid_count", len(invalidPlaceholders),
			"session_id", req.SessionID,
			"tenant_id", req.TenantID,
		)
		// SC-2 (docs/修订0811/19): the Phase 1 TODO metric — surface
		// placeholder forgery / sanitizer drift as a Prometheus series
		// instead of only a log line.
		metrics.SanitizePlaceholderTamperingTotal.WithLabelValues("llm_generated").Add(float64(len(invalidPlaceholders)))
	}

	// Native shapes first: Anthropic Messages (type=message) and OpenAI
	// Responses (object=response) carry content blocks / tool arguments the
	// legacy choices-only restorer cannot walk. restoreNative* mutates the
	// decoded map in place and reports (changed, recognized, error); an
	// unrecognized body falls through to the legacy choices restorer.
	//
	// Recognized bodies with a residual marker anywhere (vendor fields,
	// media data, escaped tokens) block: the restorers only touch known
	// visible lanes, so what is left over is a marker we cannot restore
	// and must not emit (chat_restore_test contract).
	nativeRoot := mustDecodeJSONMap(req.ResponseBody)
	if nativeRoot != nil {
		// Run both restorers for effect: messages first, then responses
		// (a body matches at most one shape; the other reports
		// recognized=false without touching the tree). Restorers mutate
		// nativeRoot in place.
		mChanged, mOK, mErr := it.restoreNativeMessagesBody(ctx, nativeRoot, sm)
		rChanged, rOK, rErr := it.restoreNativeResponsesBody(ctx, nativeRoot, sm)
		if (mOK && mErr != nil) || (rOK && rErr != nil) {
			it.logger.WarnContext(ctx, "sanitize_restore: native restore blocked",
				"messages_err", mErr, "responses_err", rErr, "session_id", req.SessionID)
			return &response.InterceptResult{ShouldBlock: true}, nil
		}
		if (mOK && mChanged) || (rOK && rChanged) {
			if out, merr := json.Marshal(nativeRoot); merr == nil && !bytes.Contains(out, []byte("{SENSITIVE:")) {
				return &response.InterceptResult{ModifiedBody: out, Action: "sanitize_restore",
					Metadata: map[string]any{"sanitize_restored": true, "placeholder_count": len(sm)}}, nil
			}
		}
	}

	// Marshal-path marker guard: any body that still carries a reserved
	// marker after the restore pass must never leave the interceptor
	// unchecked. Native shapes (Messages / Responses) block outright — their
	// restorers only touch known visible lanes, so a leftover marker means a
	// vendor field / media object / escaped token we cannot safely rewrite
	// (chat_restore_test contract). Legacy chat choices are rewritten too,
	// so the same residual check applies with the escaped-variant probe;
	// genuinely malformed JSON with a raw marker also blocks.
	//
	// A body that is valid JSON but matches NONE of the three known envelopes
	// (vendor-specific error envelopes, Gemini `candidates`, bare `output_text`)
	// used to fall through every guard and reach `return nil, nil` with the
	// internal marker intact. Blocking outright would turn a routine upstream
	// error envelope into a gateway 502, so mask instead: known placeholders
	// are restored, unknown ones become [REDACTED], and the client still gets
	// a well-formed response.
	if hasReservedPlaceholder(req.ResponseBody) {
		if nativeRoot == nil {
			// Truncated / malformed JSON body containing a marker.
			it.logger.WarnContext(ctx, "sanitize_restore: malformed body carries reserved marker",
				"session_id", req.SessionID)
			return &response.InterceptResult{ShouldBlock: true}, nil
		}
		if isNativeMessagesShape(nativeRoot) || isNativeResponsesShape(nativeRoot) {
			it.logger.WarnContext(ctx, "sanitize_restore: residual marker in recognized native body",
				"session_id", req.SessionID)
			return &response.InterceptResult{ShouldBlock: true}, nil
		}
		if !isChatChoicesShape(nativeRoot) {
			return it.maskUnrecognizedBody(ctx, req, sm)
		}
	}

	restored, err := it.restoreResponseBody(ctx, req.ResponseBody, sm)
	if err != nil || restored == nil {
		// No visible lane changed: either not a chat body at all (passthrough)
		// or a recognized chat body whose marker sits outside the visible
		// lanes (vendor field, media object). The latter must never leak.
		if nativeRoot != nil && isChatChoicesShape(nativeRoot) && hasReservedPlaceholder(req.ResponseBody) {
			it.logger.WarnContext(ctx, "sanitize_restore: residual marker in chat body outside visible lanes",
				"session_id", req.SessionID)
			return &response.InterceptResult{ShouldBlock: true}, nil
		}
		return nil, nil
	}
	if hasReservedPlaceholder(restored) {
		// The choices restorer changed something yet a marker survived
		// (unmapped placeholder forged as raw token, vendor field beside
		// choices). Fail closed instead of emitting a half-restored body.
		it.logger.WarnContext(ctx, "sanitize_restore: residual marker in chat body after restore",
			"session_id", req.SessionID)
		return &response.InterceptResult{ShouldBlock: true}, nil
	}

	return &response.InterceptResult{
		ModifiedBody: restored,
		Action:       "sanitize_restore",
		Metadata: map[string]any{
			"sanitize_restored":    true,
			"placeholder_count":    len(sm),
			"invalid_placeholders": len(invalidPlaceholders),
		},
	}, nil
}

// InterceptStreamChunk 流式 chunk 级还原：解析 SSE data 行 JSON，
// 在 delta.content / text 等字段上做占位符替换，返回 ModifiedChunk。
// chain 调用方（InterceptorChain.InterceptStreamChunk）会用 ModifiedChunk
// 替换原 chunk 写入客户端。
//
// 映射为空或 Redis 读取失败时仍检查/遮蔽 placeholder；已知值因无映射不能恢复。
// 不含待续 marker 且无可处理 delta 的 chunk 返回 nil，交由 chain 原样透传。
func (it *SanitizeRestoreInterceptor) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	if it == nil || it.sanitizer == nil || len(chunk) == 0 {
		return nil, nil
	}
	if meta == nil || meta.SessionID == "" {
		// A reserved gateway marker must never reach the client regardless of
		// session bookkeeping; an ordinary frame without a session is opaque
		// data and keeps its original framing (sse_restore.go probe rules).
		if meta == nil && hasReservedPlaceholder(chunk) {
			return &response.ChunkResult{ShouldBlock: true}, nil
		}
		if meta != nil && meta.SessionID == "" && hasReservedPlaceholder(chunk) {
			return &response.ChunkResult{ShouldBlock: true}, nil
		}
		return nil, nil
	}

	sm, err := it.loadMap(ctx, meta.SessionID, meta.TenantID)
	if err != nil {
		it.logger.Warn("sanitize_restore: stream chunk load map failed, masking placeholders",
			"error", err, "session_id", meta.SessionID)
		sm = SanitizeMap{}
	}
	if meta.State == nil {
		// Production writers initialize State before passing chunks. This
		// fallback keeps direct interceptor use safe; callers that invoke chunks
		// concurrently must provide a shared StreamState explicitly.
		meta.State = response.NewStreamState()
	}
	state, _ := meta.State.GetOrCreate(streamRestoreStateKey, func() any {
		return &streamRestoreState{tails: make(map[string]string)}
	}).(*streamRestoreState)
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.blocked {
		return &response.ChunkResult{ShouldBlock: true}, nil
	}

	var wire []byte
	changedAny := false
	for _, event := range splitSSEEvents(chunk) {
		modified, changed, block := it.restoreSSEEvent(ctx, event, sm, state)
		if block {
			state.blocked = true
			return &response.ChunkResult{ShouldBlock: true}, nil
		}
		if changed {
			wire = append(wire, modified...)
		} else {
			// Unchanged events keep their original framing bytes verbatim
			// (heartbeat comments, opaque payloads, [DONE] markers) so a
			// mixed chunk is byte-identical outside the rewritten events.
			wire = append(wire, event...)
		}
		changedAny = changedAny || changed
	}
	if !changedAny {
		return nil, nil
	}
	return &response.ChunkResult{
		ModifiedChunk: wire,
	}, nil
}

// maskUnrecognizedBody is the last-resort fail-closed path: the body is valid
// JSON but matches none of the envelopes the structured restorers understand
// (or no mapping table is available at all), so no lane-based restorer will
// ever touch the bytes carrying a marker. Restore what we can from the raw
// text, redact the rest, and hand the body back — a leaked internal token and
// a 502 on a routine upstream error envelope are both worse than a redacted
// field.
func (it *SanitizeRestoreInterceptor) maskUnrecognizedBody(ctx context.Context, req *response.InterceptRequest, sm SanitizeMap) (*response.InterceptResult, error) {
	if req == nil || len(req.ResponseBody) == 0 {
		return nil, nil
	}
	if len(sm) == 0 {
		sm = SanitizeMap{}
	}
	masked, err := it.sanitizer.RestoreOutputOrMask(ctx, string(req.ResponseBody), sm)
	if err != nil {
		it.logger.WarnContext(ctx, "sanitize_restore: mask unrecognized body failed, blocking",
			"session_id", req.SessionID, "error", err)
		return &response.InterceptResult{ShouldBlock: true}, nil
	}
	if masked == string(req.ResponseBody) || hasReservedPlaceholder([]byte(masked)) {
		it.logger.WarnContext(ctx, "sanitize_restore: unrecognized body still carries marker after mask, blocking",
			"session_id", req.SessionID)
		return &response.InterceptResult{ShouldBlock: true}, nil
	}
	it.logger.WarnContext(ctx, "sanitize_restore: unrecognized envelope, masked placeholders",
		"session_id", req.SessionID, "placeholder_count", len(sm))
	return &response.InterceptResult{
		ModifiedBody: []byte(masked),
		Action:       "sanitize_restore_masked",
		Metadata: map[string]any{
			"sanitize_restored": true,
			"placeholder_count": len(sm),
			"masked":            true,
		},
	}, nil
}

// mustDecodeJSONMap decodes body, tolerating malformed JSON (returns nil,
// which callers treat as unrecognized). Numbers decode as json.Number so
// token counts beyond 2^53 round-trip exactly through restore + re-marshal.
func mustDecodeJSONMap(body []byte) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil
	}
	return raw
}

func isNativeMessagesShape(root map[string]any) bool {
	if root == nil {
		return false
	}
	typ, _ := root["type"].(string)
	return typ == "message"
}

// isChatChoicesShape 判定 legacy OpenAI chat completions 形态
// （顶层含 choices 数组）。
func isChatChoicesShape(root map[string]any) bool {
	if root == nil {
		return false
	}
	_, ok := root["choices"].([]any)
	return ok
}

func isNativeResponsesShape(root map[string]any) bool {
	if root == nil {
		return false
	}
	object, _ := root["object"].(string)
	return object == "response"
}

// InterceptStreamEnd 流结束时 body 已重组为非流式形态，复用非流式还原。
//
// 顺序契约决定了这里只能「试还原」，不能把结果写回：流式字节早已下发，
// 而 OutputCompliance 必须先看到占位符。因此本次调用不改变任何输出，只用
// 于观测——restored 非 nil 表示该响应体存在可还原的可见 lane。
// 注意 meta.ResponseBody 仍保持占位符原文。
func (it *SanitizeRestoreInterceptor) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	if it == nil || it.sanitizer == nil || meta == nil || len(meta.ResponseBody) == 0 || meta.SessionID == "" {
		return nil, nil
	}

	sm, err := it.loadMap(ctx, meta.SessionID, meta.TenantID)
	if err != nil || len(sm) == 0 {
		return nil, nil
	}

	restored, err := it.restoreResponseBody(ctx, meta.ResponseBody, sm)
	if err != nil || restored == nil {
		return nil, nil
	}

	return &response.EndResult{
		Action: "sanitize_restore",
		Metadata: map[string]any{
			"sanitize_restored": true,
			"placeholder_count": len(sm),
		},
	}, nil
}

// restoreStreamChunk 解析单条 SSE chunk（"data: <json>\n\n" 格式），
// 在所有可能的 content 字段上做占位符替换，返回完整的 reframed chunk
// （保留原始 SSE 帧结构：注释行 / event: 行 / data: 前缀 / 末尾 \n\n）。
//
// 协议适配（2026-08-07）：
//   - OpenAI chat completion delta：choices[].delta.content
//   - OpenAI Responses API text/tool/refusal/transcript delta events
//   - Anthropic Messages delta：content_block_delta.delta.text
//
// 解析失败 / 不识别 schema → 返回 (nil, false, nil)，由 caller 原样透传。
// 成功但无 placeholder → 返回 (nil, false, nil)，同样原样透传（避免无谓的
// JSON 重序列化引入额外 marshal/unmarshal 噪声）。
//
// 跨 SSE 事件的占位符尾片保存在本请求 StreamMeta.State 中；未完成尾片
// 不会透传，连接结束时随 stream writer 生命周期释放。
func (it *SanitizeRestoreInterceptor) restoreStreamChunk(ctx context.Context, chunk []byte, sm SanitizeMap, meta *response.StreamMeta, state *streamRestoreState) ([]byte, bool, bool, error) {
	// 1. 按行扫描。SSE 帧结构：注释行（:...）/ event: 行 / data: 行 + 末尾 \n\n。
	//    LLM 流式 chunk 实际只发一条 data 行；遇到多 data 行时退化为「拼接所有
	//    data 内容」，但 framing（event:/注释/末尾 \n\n）单独保留。
	var jsonPayload []byte
	var trailing []byte // 末尾 \n\n 之类的尾缀

	rest := chunk
	// SSE 帧分隔：行以单个 \n 分隔，事件终止符是裸 \n\n（空行）。
	// 我们需要保留「行分隔符 + 末尾空白」，否则重组时帧结构会丢 \n\n。
	// 因此 data: 行的 \n 不立即消费，留在 rest 中作为 trailing 一部分。
	var prefixBuf bytes.Buffer

	for {
		lineEnd := -1
		for i := 0; i < len(rest); i++ {
			if rest[i] == '\n' {
				lineEnd = i
				break
			}
		}
		if lineEnd == -1 {
			break
		}
		line := rest[:lineEnd]

		if bytes.HasPrefix(line, []byte("data: ")) {
			data := bytes.TrimPrefix(line, []byte("data: "))
			if len(data) > 0 && !bytes.Equal(data, []byte("[DONE]")) {
				if len(jsonPayload) > 0 {
					jsonPayload = append(jsonPayload, ' ')
				}
				jsonPayload = append(jsonPayload, data...)
				// rest 保留为 trailing（含 data 行的 \n + 后续空行 + 帧终止符 \n\n）
				rest = rest[lineEnd:]
				break
			}
			// [DONE] 帧：保留在 prefixBuf（+ \n 一起）
			prefixBuf.Write(line)
			prefixBuf.WriteByte('\n')
		} else {
			prefixBuf.Write(line)
			prefixBuf.WriteByte('\n')
		}
		rest = rest[lineEnd+1:]
	}
	if len(jsonPayload) == 0 {
		return nil, false, false, nil
	}
	// 此时 rest 是「data: 行换行符之后剩余的内容」。SSE 帧分隔符
	// （\n\n 或 \n<空行>\n）就在这里。直接保留为尾缀，无需进一步解析。
	trailing = rest

	// 2. JSON 反序列化为通用结构。失败 → 透传（不要因为单 chunk 格式问题
	//    阻断整条流；典型场景：chunk 携带的是控制字段而非 content 增量）。
	var raw map[string]any
	if err := json.Unmarshal(jsonPayload, &raw); err != nil {
		if len(state.tails) > 0 {
			// We withheld a placeholder prefix from a previous event. An opaque
			// data payload cannot be assigned to a protocol lane, so passing it
			// through could expose the detached remainder of that marker.
			return nil, false, true, nil
		}
		return nil, false, false, nil
	}

	// 3. 在三种 delta schema 中做占位符替换
	changed := false
	blocked := false
	var fieldChanged bool
	fieldChanged, blocked = it.restoreStreamOpenAIDelta(ctx, raw, sm, state)
	changed = fieldChanged || changed
	if blocked {
		return nil, false, true, nil
	}
	fieldChanged, blocked = it.restoreStreamAnthropicDelta(ctx, raw, sm, state)
	changed = fieldChanged || changed
	if blocked {
		return nil, false, true, nil
	}
	fieldChanged, blocked = it.restoreStreamResponsesDelta(ctx, raw, sm, state)
	changed = fieldChanged || changed
	if blocked {
		return nil, false, true, nil
	}
	if !changed {
		return nil, false, false, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false, false, nil
	}

	// 4. 重组 framing：注释行 + event: 行 + data: + 重新序列化的 JSON + 尾缀
	var buf bytes.Buffer
	buf.Write(prefixBuf.Bytes())
	buf.WriteString("data: ")
	buf.Write(out)
	if len(trailing) > 0 {
		buf.Write(trailing)
	}
	return buf.Bytes(), true, false, nil
}

// restoreStreamOpenAIDelta 处理 OpenAI chat completion delta schema
// (choices[].delta.content + choices[].delta.tool_calls[*].function.arguments)。
// 返回是否替换了占位符。
func (it *SanitizeRestoreInterceptor) restoreStreamOpenAIDelta(ctx context.Context, raw map[string]any, sm SanitizeMap, state *streamRestoreState) (bool, bool) {
	choices, ok := raw["choices"].([]any)
	if !ok {
		return false, false
	}
	changed := false
	for choiceIndex, cAny := range choices {
		c, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		choiceLane := streamLaneValue(c, "index", choiceIndex)
		delta, ok := c["delta"].(map[string]any)
		if !ok {
			continue
		}
		if didChange, blocked := it.restoreStreamStringField(ctx, delta, "content", sm, state, "openai.choice."+choiceLane+".content"); blocked {
			return changed, true
		} else if didChange {
			changed = true
		}
		if didChange, blocked := it.restoreStreamStringField(ctx, delta, "refusal", sm, state, "openai.choice."+choiceLane+".refusal"); blocked {
			return changed, true
		} else if didChange {
			changed = true
		}
		if functionCall, ok := delta["function_call"].(map[string]any); ok {
			if didChange, blocked := it.restoreStreamStringField(ctx, functionCall, "arguments", sm, state, "openai.choice."+choiceLane+".function_call.arguments"); blocked {
				return changed, true
			} else if didChange {
				changed = true
			}
		}
		// 流式 tool_calls.arguments：上游按 token 增量下发
		// {"tool_calls":[{"index":0,"function":{"arguments":"..."}}]}
		// 同一 SSE frame 里只有一个工具的一段 arguments 增量；做占位符替换。
		toolCalls, _ := delta["tool_calls"].([]any)
		for toolPos, toolAny := range toolCalls {
			tool, ok := toolAny.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tool["function"].(map[string]any)
			if !ok {
				continue
			}
			lane := "openai.choice." + choiceLane + ".tool." + streamLaneValue(tool, "index", toolPos) + ".arguments"
			if didChange, blocked := it.restoreStreamStringField(ctx, fn, "arguments", sm, state, lane); blocked {
				return changed, true
			} else if didChange {
				changed = true
			}
		}
	}
	return changed, false
}

// restoreStreamAnthropicDelta 处理 Anthropic Messages delta schema
// (type="content_block_delta" + delta.text / delta.input)。返回是否替换了占位符。
func (it *SanitizeRestoreInterceptor) restoreStreamAnthropicDelta(ctx context.Context, raw map[string]any, sm SanitizeMap, state *streamRestoreState) (bool, bool) {
	// Anthropic 在 SSE 流中既发送 type="content_block_start" 等控制事件，
	// 也发送 type="content_block_delta" 携带 delta.text 文本增量，
	// 以及 type=input_json_delta 携带 tool_use.input 的 JSON 片段。
	// 统一在 content_block_delta 上处理：text 走普通替换，
	// partial_json 走 JSON 字符串占位符替换（增量通常为 { 或 key 部分）。
	t, _ := raw["type"].(string)
	if t != "content_block_delta" {
		return false, false
	}
	delta, ok := raw["delta"].(map[string]any)
	if !ok {
		return false, false
	}
	index := streamLaneValue(raw, "index", 0)
	changed := false
	if didChange, blocked := it.restoreStreamStringField(ctx, delta, "text", sm, state, "anthropic."+index+".text"); blocked {
		return changed, true
	} else if didChange {
		changed = true
	}
	// Anthropic tool_use.input 是 partial JSON：先把增量累计成一个 JSON 字符串
	// 做占位符替换（占位符必须整段出现才会被识别，所以一般 incremental
	// 输出含 partial 字段名/数字的场景不会误命中；只要完整 JSON 落地时
	// 落在一次 Write 里就能命中）。
	if didChange, blocked := it.restoreStreamStringField(ctx, delta, "input", sm, state, "anthropic."+index+".input"); blocked {
		return changed, true
	} else if didChange {
		// delta.input 可能是 string（Anthropic 增量）或 map（旧版）；
		// restoreStringField 只处理 string 路径。
		changed = true
	}
	if didChange, blocked := it.restoreStreamStringField(ctx, delta, "partial_json", sm, state, "anthropic."+index+".partial_json"); blocked {
		return changed, true
	} else if didChange {
		changed = true
	}
	return changed, false
}

// restoreStreamResponsesDelta 处理 OpenAI Responses API delta schema
// (type="response.output_text.delta" + delta)。返回是否替换了占位符。
func (it *SanitizeRestoreInterceptor) restoreStreamResponsesDelta(ctx context.Context, raw map[string]any, sm SanitizeMap, state *streamRestoreState) (bool, bool) {
	t, _ := raw["type"].(string)
	field := ""
	switch t {
	case "response.output_text.delta", "response.function_call_arguments.delta", "response.refusal.delta", "response.audio_transcript.delta":
		field = "delta"
	default:
		return false, false
	}
	lane := "responses." + t + "." + streamLaneValue(raw, "output_index", 0) + "." + streamLaneValue(raw, "content_index", 0) + "." + streamLaneID(raw, "item_id")
	return it.restoreStreamStringField(ctx, raw, field, sm, state, lane)
}

func streamLaneValue(object map[string]any, key string, fallback int) string {
	if value, ok := object[key].(float64); ok && value >= 0 && value <= 1<<31-1 && value == float64(int64(value)) {
		return fmt.Sprint(int64(value))
	}
	return fmt.Sprint(fallback)
}

func streamLaneID(object map[string]any, key string) string {
	if value, ok := object[key].(string); ok && value != "" {
		// Opaque provider IDs are untrusted. Store a fixed-size key component,
		// not the original value, in request-local carry state.
		digest := sha256.Sum256([]byte(value))
		return hex.EncodeToString(digest[:])
	}
	return ""
}

// restoreStreamStringField holds only a syntactically valid placeholder prefix
// at the end of a delta. The held bytes are scoped to the response writer via
// StreamMeta.State and are never emitted raw. If the next delta completes the
// token, replacement/masking happens before any part reaches the client.
func (it *SanitizeRestoreInterceptor) restoreStreamStringField(ctx context.Context, object map[string]any, field string, sm SanitizeMap, state *streamRestoreState, lane string) (bool, bool) {
	value, ok := object[field].(string)
	if !ok {
		return false, false
	}
	previous := state.tails[lane]
	combined := previous + value
	delete(state.tails, lane)

	safe := combined
	tail := ""
	start, partial := incompletePlaceholderStart(combined)
	if partial {
		candidate := combined[start:]
		if len(candidate) > maxPlaceholderTailBytes {
			safe = combined[:start] + "[REDACTED]"
		} else {
			safe = combined[:start]
			tail = candidate
		}
	}

	restored, err := it.sanitizer.RestoreOutputOrMask(ctx, safe, sm)
	if err != nil {
		return false, false
	}
	if previous != "" && tail == "" && restored == combined {
		// The continuation made the buffered prefix syntactically invalid. A
		// plain brace prefix is common in streamed JSON/code, so release it with
		// the continuation to preserve the response. Once the reserved marker
		// prefix has started, redact this delta instead of either exposing a
		// malformed internal token or aborting the whole client stream.
		if strings.HasPrefix(previous, "{SENSITIVE:") {
			object[field] = "[REDACTED]"
			return true, false
		}
		object[field] = combined
		return true, false
	}
	if tail != "" {
		if _, exists := state.tails[lane]; !exists && len(state.tails) >= maxStreamRestoreLanes {
			// Do not let later chunks resume without the prefix they depend on.
			return false, true
		}
		state.tails[lane] = tail
	}
	if previous == "" && tail == "" && restored == value {
		return false, false
	}
	object[field] = restored
	return true, false
}

// incompletePlaceholderStart finds a valid prefix of the placeholder grammar
// at the end of text. Invalid brace text is ordinary model output and passes
// through; a full placeholder is handled by RestoreOutputOrMask.
func incompletePlaceholderStart(text string) (int, bool) {
	start := strings.LastIndexByte(text, '{')
	if start < 0 {
		return 0, false
	}
	candidate := text[start:]
	if !validPlaceholderPrefix(candidate) {
		return 0, false
	}
	return start, true
}

func validPlaceholderPrefix(candidate string) bool {
	const marker = "{SENSITIVE:"
	if len(candidate) <= len(marker) {
		return strings.HasPrefix(marker, candidate)
	}
	if !strings.HasPrefix(candidate, marker) || strings.Contains(candidate, "}") {
		return false
	}
	rest := candidate[len(marker):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		if rest == "" {
			return true
		}
		for _, r := range rest {
			if !(r >= 'a' && r <= 'z') && r != '_' {
				return false
			}
		}
		return true
	}
	if colon == 0 {
		return false
	}
	for _, r := range rest[:colon] {
		if !(r >= 'a' && r <= 'z') && r != '_' {
			return false
		}
	}
	index := rest[colon+1:]
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// restoreStringField 把 m[field]（必须为 string）做占位符替换后写回。
// 替换成功（内容变化）返回 true；字段不存在/非 string/无 placeholder 返回 false。
func restoreStringField(ctx context.Context, s *Sanitizer, m map[string]any, field string, sm SanitizeMap) bool {
	v, ok := m[field].(string)
	if !ok {
		return false
	}
	restored, err := s.RestoreOutputOrMask(ctx, v, sm)
	if err != nil || restored == v {
		return false
	}
	m[field] = restored
	return true
}

// loadMap 从 Redis 读取会话级映射表。
func (it *SanitizeRestoreInterceptor) loadMap(ctx context.Context, sessionID string, tenantIDs ...string) (SanitizeMap, error) {
	if it.redis == nil {
		return nil, nil
	}
	tenant := firstSanitizeTenant(tenantIDs)
	key := sanitizeMapKey(tenant, sessionID)
	// audit-24h-20260828-r4 P2: Use SafeHGetAll to prevent WRONGTYPE errors
	// when the sanitize map key collides with a non-hash type. ErrKeyNotFound
	// is the cache-miss path — fall through to the empty-map return below
	// rather than propagate (the rest of the function is nil-tolerant).
	vals, err := redissafe.SafeHGetAll(ctx, it.redis, key)
	if err != nil {
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			vals = nil
		} else {
			return nil, err
		}
	}
	if len(vals) == 0 && tenant == "" {
		key = SanitizeRedisKey(sessionID)
		vals, err = redissafe.SafeHGetAll(ctx, it.redis, key)
		if err != nil {
			if errors.Is(err, redissafe.ErrKeyNotFound) {
				vals = nil
			} else {
				return nil, err
			}
		}
	}
	if len(vals) == 0 {
		return nil, nil
	}
	sm := make(SanitizeMap, len(vals))
	for ph, val := range vals {
		sm[ph] = val
	}
	// 每次还原都刷新 TTL（用户继续会话）。刷新主 map 与 offset hash
	// 一起进行；否则 offset 可能先过期，下一轮输入会从 1 重编号并覆盖
	// 仍存活的 placeholder 映射。
	if err := it.redis.Expire(ctx, key, it.ttl).Err(); err != nil {
		return nil, err
	}
	_ = it.redis.Expire(ctx, sanitizeOffsetKey(tenant, sessionID), it.ttl).Err()
	return sm, nil
}

// validatePlaceholders 验证响应中的占位符是否都在映射表中。
// 返回不在映射表中的占位符列表（LLM 生成的伪造占位符）。
func (it *SanitizeRestoreInterceptor) validatePlaceholders(ctx context.Context, body []byte, sm SanitizeMap) []string {
	var invalidPlaceholders []string

	matches := PlaceholderPattern.FindAllString(string(body), -1)
	seen := make(map[string]bool)

	for _, match := range matches {
		if seen[match] {
			continue // 去重
		}
		seen[match] = true

		if _, ok := sm[match]; !ok {
			// LLM 生成了不在映射表中的占位符
			invalidPlaceholders = append(invalidPlaceholders, match)
		}
	}

	return invalidPlaceholders
}

// restoreResponseBody 还原 OpenAI 响应体 choices[].message.content + tool_calls
// 中的占位符，确保下游拿到真实敏感值。
//
// 还原策略（统一使用 RestoreOutputOrMask）：
//   - role=assistant：还原为真实敏感值；映射表中没有的占位符用 [REDACTED] 替换
//     防止上游注入的占位符文本泄漏
//   - role=user/tool/function 等：还原 + mask（语义同 assistant，但生产路径上
//     这些 role 的响应消息通常不含 placeholder）
//   - message.tool_calls[*].function.arguments：JSON 字符串，按 key/value 遍历还原
//     （网关在调用工具前必须拿到真实敏感值，否则下游工具拿到的就是占位符文本）
//   - 顶层 tool_calls[*].function.arguments：同上（部分 schema 把 tool_calls
//     直接挂在 choices 而非 message 上）
func (it *SanitizeRestoreInterceptor) restoreResponseBody(ctx context.Context, body []byte, sm SanitizeMap) ([]byte, error) {
	// UseNumber, like the native branch: a plain Unmarshal would re-render any
	// usage/token count above 2^53 in float form and silently rewrite the
	// client's accounting numbers (native_restore contract).
	raw := mustDecodeJSONMap(body)
	if raw == nil {
		return nil, errors.New("sanitize_restore: response body is not a JSON object")
	}
	choices, ok := raw["choices"].([]any)
	if !ok {
		return nil, nil
	}
	changed := false
	for _, cAny := range choices {
		c, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		// 1) message visible text lanes: content / refusal / reasoning_content
		if msg, ok := c["message"].(map[string]any); ok {
			for _, field := range []string{"content", "refusal", "reasoning_content"} {
				if restoreLegacyTextField(ctx, it.sanitizer, msg, field, sm) {
					changed = true
				}
			}
			// 2) message.function_call.arguments（旧版单函数调用形态）
			if fn, ok := msg["function_call"].(map[string]any); ok {
				if restoreLegacyFunctionArgs(ctx, it.sanitizer, fn, sm) {
					changed = true
				}
			}
			// 3) message.tool_calls[*].function.arguments
			if restoreToolCallsArgs(ctx, it.sanitizer, msg, "tool_calls", sm) {
				changed = true
			}
		}
		// 4) completion 风格 choices[].text
		if restoreLegacyTextField(ctx, it.sanitizer, c, "text", sm) {
			changed = true
		}
		// 5) 顶层 tool_calls[*].function.arguments（部分 schema 透传）
		if restoreToolCallsArgs(ctx, it.sanitizer, c, "tool_calls", sm) {
			changed = true
		}
	}
	if !changed {
		return nil, nil
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// restoreLegacyTextField 对 obj[field]（字符串）做占位符还原；已映射的还原成
// 原值，未映射的 mask 成 [REDACTED]。仅当字段被改动时返回 true。
func restoreLegacyTextField(ctx context.Context, s *Sanitizer, obj map[string]any, field string, sm SanitizeMap) bool {
	value, ok := obj[field].(string)
	if !ok || !PlaceholderPattern.MatchString(value) {
		return false
	}
	restored, err := s.RestoreOutputOrMask(ctx, value, sm)
	if err != nil || restored == value {
		return false
	}
	obj[field] = restored
	return true
}

// restoreLegacyFunctionArgs 还原 function.arguments（JSON 字符串）里的占位符；
// arguments 不是合法 JSON 对象时退化为普通字符串还原。
func restoreLegacyFunctionArgs(ctx context.Context, s *Sanitizer, fn map[string]any, sm SanitizeMap) bool {
	argsStr, ok := fn["arguments"].(string)
	if !ok || !PlaceholderPattern.MatchString(argsStr) {
		return false
	}
	var argsObj map[string]any
	if err := json.Unmarshal([]byte(argsStr), &argsObj); err != nil {
		// arguments 不是合法 JSON 对象：当作普通字符串做占位符还原
		restored, rerr := s.RestoreOutputOrMask(ctx, argsStr, sm)
		if rerr == nil && restored != argsStr {
			fn["arguments"] = restored
			return true
		}
		return false
	}
	if restoreJSONRecursive(ctx, s, argsObj, sm) {
		raw, mErr := json.Marshal(argsObj)
		if mErr == nil {
			fn["arguments"] = string(raw)
			return true
		}
	}
	return false
}

// restoreToolCallsArgs 在 obj[key]（数组）中遍历每个 tool_call，
// 对 function.arguments（JSON 字符串）按 key/value 还原占位符；
// function.arguments 解析失败或不含占位符时整段保留原样。
// 返回是否发生了修改。
func restoreToolCallsArgs(ctx context.Context, s *Sanitizer, obj map[string]any, key string, sm SanitizeMap) bool {
	arr, ok := obj[key].([]any)
	if !ok || len(arr) == 0 {
		return false
	}
	changed := false
	for _, tcAny := range arr {
		tc, ok := tcAny.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tc["function"].(map[string]any)
		if !ok {
			continue
		}
		if restoreLegacyFunctionArgs(ctx, s, fn, sm) {
			changed = true
		}
	}
	return changed
}

// restoreJSONRecursive 在 m 或 restoreJSONRecursive 在 m 的字符串字段里做占位符还原
// （浅遍历一个 JSON 对象：值为字符串时还原；值为对象/数组时递归）。
// 任何一字段被改动即返回 true。
func restoreJSONRecursive(ctx context.Context, s *Sanitizer, m map[string]any, sm SanitizeMap) bool {
	changed := false
	for k, v := range m {
		switch vv := v.(type) {
		case string:
			if !PlaceholderPattern.MatchString(vv) {
				continue
			}
			restored, err := s.RestoreOutputOrMask(ctx, vv, sm)
			if err == nil && restored != vv {
				m[k] = restored
				changed = true
			}
		case map[string]any:
			if restoreJSONRecursive(ctx, s, vv, sm) {
				changed = true
			}
		case []any:
			for i, item := range vv {
				switch it := item.(type) {
				case string:
					if !PlaceholderPattern.MatchString(it) {
						continue
					}
					restored, err := s.RestoreOutputOrMask(ctx, it, sm)
					if err == nil && restored != it {
						vv[i] = restored
						changed = true
					}
				case map[string]any:
					if restoreJSONRecursive(ctx, s, it, sm) {
						changed = true
					}
				}
			}
		}
	}
	return changed
}

// HashTenant returns the stable 16-char tenant hash used in Redis key scopes.
func HashTenant(tenantID string) string {
	if tenantID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tenantID))
	return hex.EncodeToString(sum[:8])
}

func firstSanitizeTenant(tenantIDs []string) string {
	if len(tenantIDs) == 0 {
		return ""
	}
	return strings.TrimSpace(tenantIDs[0])
}

// sanitizeMapKey preserves the legacy key only when the caller has no tenant
// identity at all. Once a tenant is known, every read/write must use the
// tenant-scoped form to prevent cross-tenant placeholder restoration.
func sanitizeMapKey(tenantID, sessionID string) string {
	if tenantID == "" {
		tenantID = "_unknown"
	}
	return SanitizeRedisKey(HashTenant(tenantID), sessionID)
}

func sanitizeOffsetKey(tenantID, sessionID string) string {
	if tenantID == "" {
		tenantID = "_unknown"
	}
	return SanitizeOffsetRedisKey(HashTenant(tenantID), sessionID)
}

// SanitizeRedisKey returns a tenant-scoped Redis key when passed tenantHash
// and sessionID. The legacy single-argument form is retained for callers that
// lack tenant context; new request paths must use the two-argument form.
func SanitizeRedisKey(parts ...string) string {
	if len(parts) >= 2 {
		return fmt.Sprintf("session:%s:%s:sanitize", parts[0], parts[1])
	}
	if len(parts) == 1 {
		return fmt.Sprintf("session:%s:sanitize", parts[0])
	}
	return ""
}

// buildSanitizeInfoForSession derives the compression.SanitizeInfo bridge
// payload from the merged placeholder map (SC-1). Counts are classified by
// parsing the placeholder token, so the merged map (no per-message
// Fragments) is enough.
func buildSanitizeInfoForSession(tenantID, sessionID string, sm SanitizeMap) compression.SanitizeInfo {
	keys := make(map[string]struct{}, len(sm))
	for ph := range sm {
		keys[ph] = struct{}{}
	}
	return compression.BuildSanitizeInfo(HashTenant(tenantID), sessionID, func(placeholder string) (string, bool) {
		p, ok := ParsePlaceholder(placeholder)
		if !ok {
			return "", false
		}
		return string(p.Type), true
	}, keys)
}

// SanitizeOffsetRedisKey 生成会话级 offset（每类已用最大编号）的 Redis key。
//
// 独立于主 sanitize map：loadOffsets 时只读这个紧凑 hash，避免扫描整个 map。
// TTL 与主 hash 同步刷新。
func SanitizeOffsetRedisKey(parts ...string) string {
	if len(parts) >= 2 {
		return fmt.Sprintf("session:%s:%s:sanitize:offsets", parts[0], parts[1])
	}
	if len(parts) == 1 {
		return fmt.Sprintf("session:%s:sanitize:offsets", parts[0])
	}
	return ""
}

// WithSanitizeMap 把 SanitizeMap 放入 Context（供同请求内下游读取）。
func WithSanitizeMap(ctx context.Context, sm SanitizeMap) context.Context {
	return context.WithValue(ctx, sanitizeMapCtxKey{}, sm)
}

// SanitizeMapFromContext 从 Context 读取 SanitizeMap。
func SanitizeMapFromContext(ctx context.Context) (SanitizeMap, bool) {
	sm, ok := ctx.Value(sanitizeMapCtxKey{}).(SanitizeMap)
	return sm, ok
}

type sanitizeMapCtxKey struct{}

// readBody reads at most the streaming handler's 128 MiB cap plus one byte.
// It restores accepted bytes for the downstream handler. Read failures and
// oversized bodies are rejected by the middleware, never dispatched.
func readBody(r *http.Request) ([]byte, error) {
	return readBodyLimit(r, maxSanitizeBodySize)
}

func readBodyLimit(r *http.Request, limit int) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	if r.ContentLength > int64(limit) {
		return nil, errSanitizeBodyTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if len(b) > limit {
		return nil, errSanitizeBodyTooLarge
	}
	// Keep the prior replay contract for successful reads and partial reads.
	r.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return b, nil
}
