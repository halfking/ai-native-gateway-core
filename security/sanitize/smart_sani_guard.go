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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/metrics"
	"github.com/redis/go-redis/v9"
)

// HashTenant returns the first 16 hex chars (8 bytes) of sha256(tenantID),
// matching the format used by domains/session/preprocess.hash16 so the same
// tenant_id always produces the same key segment in Redis regardless of which
// package derived it. Keeping a local copy avoids a new cross-tree import
// (security/sanitize → domains/session/preprocess); any drift must be caught
// by the cross-package test in domains/session/preprocess.
//
// T11-P0: required so sanitize Redis keys can be scoped per-tenant
// (session:{tenantHash}:{sessionID}:sanitize) and prevent cross-tenant
// placeholder leakage.
func HashTenant(tenantID string) string {
	if tenantID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tenantID))
	return hex.EncodeToString(sum[:8])
}

// RestoreInterceptorName 是还原拦截器在链中的名字（日志/观测）
const RestoreInterceptorName = "sanitize_restore"

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
//
// T11-P0: 写入 Redis 时同时按 tenant 隔离（session:{tenantHash}:{sid}:sanitize），
// 防止跨租户共享 sessionID 时互相读到对方的占位符映射表；getTenantID 由调用方
// 注入（与 getSessionID 同风格），生产侧从 request.Context 取真实 tenantID。
type SanitizeInputMiddleware struct {
	sanitizer *Sanitizer
	redis     *redis.Client
	ttl       time.Duration
	logger    *slog.Logger
	// getSessionID 从请求头提取会话ID（由调用方注入，便于测试）
	getSessionID func(r *http.Request) string
	// getTenantID 从请求/上下文提取 tenantID（由调用方注入）。nil 时退化为
	// "_unknown" — 所有没显式注入 tenant 的测试/旧调用点都走这个 sentinel
	// bucket，Redis key 上仍带 hash 段（用于将来按桶清理）。
	getTenantID func(r *http.Request) string
}

// NewSanitizeInputMiddleware 创建输入脱敏中间件。
// redis 为 nil 时退化为仅内存脱敏（不持久化，单轮可用）。
//
// 兼容旧签名：getTenantID 留空，使用 header 默认值（X-Gw-Tenant-Id 优先）；
// 若 header 也为空，落到 "_unknown" bucket 并打 WARN 日志。
func NewSanitizeInputMiddleware(s *Sanitizer, redis *redis.Client, ttl time.Duration) (*SanitizeInputMiddleware, error) {
	return NewSanitizeInputMiddlewareWithTenant(s, redis, ttl, nil)
}

// NewSanitizeInputMiddlewareWithTenant 与 NewSanitizeInputMiddleware 等价，
// 但允许调用方显式注入 tenantID 提取函数（生产侧用 request.Context 取真实值）。
// getTenantID 为 nil 时与旧行为一致：按 header 取，无值则用 "_unknown"。
func NewSanitizeInputMiddlewareWithTenant(s *Sanitizer, redis *redis.Client, ttl time.Duration, getTenantID func(r *http.Request) string) (*SanitizeInputMiddleware, error) {
	if s == nil {
		return nil, fmt.Errorf("sanitize middleware: %w", ErrNilSanitizer)
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if getTenantID == nil {
		getTenantID = defaultTenantIDFromHeader
	}
	return &SanitizeInputMiddleware{
		sanitizer: s,
		redis:     redis,
		ttl:       ttl,
		logger:    slog.Default().With("component", "sanitize_middleware"),
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
		getTenantID: getTenantID,
	}, nil
}

// defaultTenantIDFromHeader 从 header 提取 tenantID（兼容旧行为）。
// 顺序：X-Gw-Tenant-Id > X-Tenant-Id > X-Tenant。无值时返回 "_unknown"
// 并由 caller 负责打 WARN。
func defaultTenantIDFromHeader(r *http.Request) string {
	for _, header := range tenantIDHeaderPriority {
		if v := strings.TrimSpace(r.Header.Get(header)); v != "" {
			return v
		}
	}
	return ""
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

// tenantIDHeaderPriority 与 sessionID 风格保持一致：X-Gw-Tenant-Id 优先。
// 生产侧推荐用 NewSanitizeInputMiddlewareWithTenant 注入真实取数函数
// （从 ctx 取经鉴权后的 tenantID），header 兜底仅用于测试/未鉴权路径。
var tenantIDHeaderPriority = []string{
	"X-Gw-Tenant-Id",
	"X-Tenant-Id",
	"X-Tenant",
}

// unknownTenantSentinel 是 getTenantID 返回空值时的占位桶。
//
// 2026-08-22（T11-P0）：当 caller 没注入 tenant getter、且 header 也为空，
// 中间件会落到这个 bucket。所有走这个 bucket 的请求共享同一组 Redis key，
// 等于把"无 tenant"流量合并到一个伪 tenant 下 — 比"完全不区分 tenant"
// （旧行为，等于全部 session 共享同一组 key）严格得多。WARN 日志用于让运维
// 知道有租户上下文缺失的生产流量。
const unknownTenantSentinel = "_unknown"

// Wrap 返回一个 http.Handler 包装器。
// 在真实 handler 之前执行脱敏；脱敏失败时降级放行（不阻断请求）。
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
		tenantID := m.getTenantID(r)
		if tenantID == "" {
			tenantID = unknownTenantSentinel
			m.logger.WarnContext(r.Context(), "sanitize_middleware: tenant context missing, falling back to sentinel bucket",
				"session_id", sessionID,
				"sentinel", unknownTenantSentinel,
			)
		}
		tenantHash := HashTenant(tenantID)
		// readBody 恢复 r.Body，因此下面每条 passthrough 路径都安全：
		// 无脱敏、脱敏失败、读取失败都会把原始 body 交给下游。
		body, err := readBody(r)
		if err != nil {
			m.logger.Warn("sanitize_middleware: read body failed, passthrough",
				"error", err, "session_id", sessionID, "tenant_id", tenantID)
			next.ServeHTTP(w, r)
			return
		}

		// 解析并脱敏
		sanitizedBody, sm, err := m.sanitizeRequestBody(r.Context(), body, tenantHash, sessionID)
		if err != nil {
			m.logger.Warn("sanitize_middleware: sanitize failed, passthrough",
				"error", err, "session_id", sessionID, "tenant_id", tenantID)
			next.ServeHTTP(w, r)
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
			*r = *r.WithContext(compression.WithSanitizeInfo(r.Context(),
				buildSanitizeInfoForSession(tenantHash, sessionID, sm)))
		}

		next.ServeHTTP(w, r)
	})
}

// sanitizeRequestBody 解析 OpenAI 请求体并脱敏 messages。
// 返回 (脱敏后的请求体, SanitizeMap)。无敏感信息时返回 (nil, 空map)。
//
// T11-P0: tenantHash 进入 Redis key（allocateOffsets / saveMapAndOffsets 都用它），
// 确保每个租户的会话偏移量和映射表互不可见。占位符 index 的预占走 Lua/HINCRBY
// 原子分配（详见 allocateOffsets），不再有 read-modify-write race。
func (m *SanitizeInputMiddleware) sanitizeRequestBody(ctx context.Context, body []byte, tenantHash, sessionID string) ([]byte, SanitizeMap, error) {
	// 解析请求体为通用结构
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, err
	}

	messagesRaw, ok := raw["messages"]
	if !ok {
		return nil, nil, nil // 无 messages 字段，跳过
	}
	messages, ok := messagesRaw.([]any)
	if !ok {
		return nil, nil, nil
	}

	// === Pass 1：只检测、不分配 index。统计每类需要多少个新编号 ===
	typeIndexBase := make(map[SensitiveType]int) // 每类本轮新占的数量（用于 Lua 预占）
	preScan := make([]sanitizeMessage, 0, len(messages))

	for i, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" && role != "system" {
			continue // 只脱敏 user 和 system；assistant 是上游生成，不应改动
		}

		// offset 传 nil — 第一遍只数 fragment，不产生 placeholder
		result, err := m.sanitizer.sanitizeInput(ctx, content, nil)
		if err != nil {
			return nil, nil, err
		}
		if len(result.SanitizeMap) == 0 {
			continue
		}
		for _, frag := range result.Fragments {
			typeIndexBase[frag.Type]++
		}
		preScan = append(preScan, sanitizeMessage{Index: i, Msg: msg, First: result})
	}

	if len(preScan) == 0 {
		return nil, nil, nil
	}

	// === 原子预占：Lua/HINCRBY 一次性为本请求每类预留 N 个编号 ===
	var startByType map[SensitiveType]int
	if m.redis != nil && sessionID != "" {
		allocated, allocErr := m.allocateOffsets(ctx, tenantHash, sessionID, typeIndexBase)
		if allocErr != nil {
			// 预占失败：降级为单轮（不用会话级 offset，但仍要写 map 供本次响应还原）
			m.logger.Warn("sanitize_middleware: allocate offsets failed, single-turn fallback",
				"error", allocErr, "session_id", sessionID, "tenant_hash", tenantHash)
			startByType = nil
		} else {
			startByType = allocated
		}
	}

	// === Pass 2：用预占到的 start offset 真正生成 placeholder，写入 body ===
	finalSM := make(SanitizeMap)
	changed := false
	for _, entry := range preScan {
		result, err := m.sanitizer.sanitizeInput(ctx, entry.Msg["content"].(string), startByType)
		if err != nil {
			return nil, nil, err
		}
		if len(result.SanitizeMap) == 0 {
			continue
		}
		entry.Msg["content"] = result.SanitizedText
		messages[entry.Index] = entry.Msg
		changed = true
		for ph, val := range result.SanitizeMap {
			finalSM[ph] = val
		}
	}

	if !changed {
		return nil, nil, nil
	}

	// 持久化映射表到 Redis（offset 已被 Lua 预占，此处只写 map + TTL）
	if m.redis != nil && sessionID != "" {
		if err := m.saveMapAndOffsets(ctx, tenantHash, sessionID, finalSM); err != nil {
			m.logger.Warn("sanitize_middleware: save map failed (restore degraded to single-turn)",
				"error", err, "session_id", sessionID, "tenant_hash", tenantHash)
		}
	}

	// 重新序列化
	newBody, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}

	return newBody, finalSM, nil
}

// sanitizeMessage 缓存第一遍 sanitize 的输入（不持有第一遍结果中的 placeholder，
// 因为第二遍会用 startByType 重新生成；只保留原始 msg 与 fragments 用于排版）。
type sanitizeMessage struct {
	Index int
	Msg   map[string]any
	First *SanitizeResult
}

// allocateOffsets 用一条 Lua 脚本对 offset key 做原子 HINCRBY，预留每类 N 个编号，
// 返回每类的「起始 base」index（即 HINCRBY 之前的旧值，旧值为 0 时起始 base = 0）。
//
// 设计要点（T11-P0）：
//   - 单条 Lua 内：先读旧值（作为起始 base），再 HINCRBY 增加 delta，最后 EXPIRE。
//     整段脚本在 Redis 单线程内执行，没有 read-modify-write race。
//   - 「起始 base」是 sanitizer.sanitizeInput 的 offset 参数语义：sanitizer 用
//     offset[t] 作为 base，本轮每个 fragment 的 index = 内部累加 + base。所以
//     想要第一个新 placeholder 是 base+1 时，offset = base；想要它是 base+0
//     时，offset = base-1。SanitizeInputWithOffset 的 doc 明确：「下一编号
//     从 offset+1 起」，因此本函数返回 prev（旧值）即可。
//   - EXPIRE 在脚本末尾一次性刷新，避免单独一次 round-trip。
//   - ARGV 顺序：[type, delta, type, delta, ..., ttlSec]；首参数数对由 caller 拼装。
//
// 输入 needByType 是本请求每类需要新增的 placeholder 数；返回 startByType 是每类的
// 起始 base（用于 sanitizeInput 的 offset 参数）。
func (m *SanitizeInputMiddleware) allocateOffsets(ctx context.Context, tenantHash, sessionID string, needByType map[SensitiveType]int) (map[SensitiveType]int, error) {
	if len(needByType) == 0 {
		return map[SensitiveType]int{}, nil
	}
	// 排序：让 ARGV 顺序稳定（便于测试 + 调试可读性；Redis Lua 不依赖顺序）
	types := make([]SensitiveType, 0, len(needByType))
	for t := range needByType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return string(types[i]) < string(types[j]) })

	args := make([]any, 0, 2*len(types)+1)
	for _, t := range types {
		args = append(args, string(t), needByType[t])
	}
	ttlSec := int(m.ttl.Seconds())
	if ttlSec <= 0 {
		ttlSec = 1800
	}
	args = append(args, ttlSec)

	key := SanitizeOffsetRedisKey(tenantHash, sessionID)
	res, err := allocateOffsetsScript.Run(ctx, m.redis, []string{key}, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("allocate offsets: %w", err)
	}

	// res 是 []interface{}，每个元素是 int64（旧值，即 sanitizeInput 用的 base）。
	arr, ok := res.([]any)
	if !ok {
		return nil, fmt.Errorf("allocate offsets: unexpected reply type %T", res)
	}
	if len(arr) != len(types) {
		return nil, fmt.Errorf("allocate offsets: reply length %d != types %d", len(arr), len(types))
	}
	startByType := make(map[SensitiveType]int, len(types))
	for i, t := range types {
		v, ok := arr[i].(int64)
		if !ok {
			return nil, fmt.Errorf("allocate offsets: type %s reply not int64 (%T)", t, arr[i])
		}
		startByType[t] = int(v)
	}
	return startByType, nil
}

// allocateOffsetsScript: 单脚本里读取 prev、原子 HINCRBY、刷新 EXPIRE。
//
// KEYS[1] = offset key
// ARGV    = [type1, delta1, type2, delta2, ..., ttlSec]
//
// 返回值：每个 type 对应的「起始 base」（prev，旧值；字段不存在时 prev = 0）。
//
// 顺序不依赖 ARGV 配对的相对次序，但调用方（allocateOffsets）传入时按 type 名字典序
// 排序，保证脚本输出顺序与 caller 期望一致。
var allocateOffsetsScript = redis.NewScript(`
local n = (#ARGV - 1) / 2
local ttl = ARGV[#ARGV]
local result = {}
for i = 1, n do
  local t = ARGV[i*2 - 1]
  local inc = tonumber(ARGV[i*2])
  local prev = tonumber(redis.call('HGET', KEYS[1], t) or '0')
  redis.call('HINCRBY', KEYS[1], t, inc)
  result[i] = prev
end
if ttl ~= '' then
  redis.call('EXPIRE', KEYS[1], ttl)
end
return result
`)

// loadOffsets 仅用于诊断/兼容旧测试代码：读 offset key 返回每类当前最大编号。
//
// T11-P0 之后生产路径不再使用本函数（占位 index 由 allocateOffsets 原子预占）。
// 保留它是为了让诊断工具 / 测试代码能以更轻量的方式观察 session 级偏移量。
func (m *SanitizeInputMiddleware) loadOffsets(ctx context.Context, tenantHash, sessionID string) map[SensitiveType]int {
	if m.redis == nil || sessionID == "" {
		return nil
	}
	vals, err := m.redis.HGetAll(ctx, SanitizeOffsetRedisKey(tenantHash, sessionID)).Result()
	if err != nil || len(vals) == 0 {
		return nil
	}
	maxIdx := make(map[SensitiveType]int, len(vals))
	for tStr, v := range vals {
		var idx int
		if _, scanErr := fmt.Sscanf(v, "%d", &idx); scanErr != nil || idx <= 0 {
			continue
		}
		maxIdx[SensitiveType(tStr)] = idx
	}
	return maxIdx
}

// saveMapAndOffsets 把映射表写入 Redis。offset 已在 allocateOffsets 中原子预占，
// 此处只负责 HSET 主 map + EXPIRE（两个 key 都需要刷新 TTL）。
//
// T11-P0 之前此函数同时写 offset key（read-modify-write race）；新版 offset 由 Lua
// 一次性预占，调用方无需再传 usedCount。
func (m *SanitizeInputMiddleware) saveMapAndOffsets(ctx context.Context, tenantHash, sessionID string, sm SanitizeMap) error {
	mapKey := SanitizeRedisKey(tenantHash, sessionID)

	// 写占位符→原始值
	if len(sm) > 0 {
		fields := make(map[string]any, len(sm))
		for ph, val := range sm {
			fields[ph] = val
		}
		if err := m.redis.HSet(ctx, mapKey, fields).Err(); err != nil {
			return err
		}
	}

	// 刷新主 hash 的 TTL
	if err := m.redis.Expire(ctx, mapKey, m.ttl).Err(); err != nil {
		return err
	}

	// offset key 也刷新 TTL（allocateOffsets 已设过，这里冗余刷新以确保 map 与 offset
	// 的 expire 时刻接近 — 否则 map 先 expire 时 offset key 还在，后续请求会从「幽灵」
	// offset 起点开始编号；只要二者同步 expire 就能避免这个不一致）。
	offsetKey := SanitizeOffsetRedisKey(tenantHash, sessionID)
	return m.redis.Expire(ctx, offsetKey, m.ttl).Err()
}

// injectPlaceholderProtection 向 messages 数组注入 System Prompt 占位符保护指令
//
// Phase 2 Task 2.3: 防止 LLM 篡改或泄露占位符格式
//
// 行为：
//  1. 查找第一条 system 消息
//  2. 如果存在，向其 content 追加保护指令
//  3. 如果不存在，在数组开头插入新的 system 消息
//
// 参数：
//   - messages: []any，每个元素是 map[string]any（OpenAI 格式）
func (m *SanitizeInputMiddleware) injectPlaceholderProtection(messages []any) {
	if !IsSanitizeSystemPromptEnabled() {
		return
	}

	// 查找第一条 system 消息
	for _, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" {
			continue
		}

		// 找到 system 消息，注入保护指令
		content, _ := msg["content"].(string)
		msg["content"] = InjectPlaceholderProtection(content)
		return
	}

	// 没有 system 消息，在开头插入新消息
	newSystemMsg := map[string]any{
		"role":    "system",
		"content": InjectPlaceholderProtection(""),
	}

	// 在数组开头插入（避免 append 后 messages 指向新的底层数组）
	// 由于 messages 是切片，这里需要用反射或类型断言来修改原数组
	// 简化实现：直接在切片开头插入（调用方会重新序列化整个 raw）
	copy(messages[1:], messages)
	messages[0] = newSystemMsg
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
//	先组装完整后再调用本拦截器；但如果上游把一个 placeholder 拆到多个
//	独立 SSE 事件，当前接口不会跨事件重组，相关文本会按事件原样透传。
//
//	降级：chain 为 nil / Redis 不可用 / chunk JSON 解析失败时均返回 nil，
//	原样透传 chunk 到客户端。
//
// T11-P0：loadMap 需要 tenantHash（与输入中间件对齐），让还原走与写入同一条
// session:{tenantHash}:{sid}:sanitize key。tenant 从 InterceptRequest.TenantID /
// StreamMeta.TenantID 取（调用方在 dispatcher 处已经灌入）；若为空则落到
// unknownTenantSentinel 桶并打 WARN。
type SanitizeRestoreInterceptor struct {
	sanitizer *Sanitizer
	redis     *redis.Client
	ttl       time.Duration
	logger    *slog.Logger
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

// tenantHashFor 把 request/stream meta 上的 TenantID 转成 hash。
// 空值时落到 unknownTenantSentinel 桶，并打 WARN 日志（与输入中间件对齐）。
func (it *SanitizeRestoreInterceptor) tenantHashFor(ctx context.Context, tenantID, sessionID string) string {
	if tenantID == "" {
		tenantID = unknownTenantSentinel
		it.logger.WarnContext(ctx, "sanitize_restore: tenant context missing, falling back to sentinel bucket",
			"session_id", sessionID,
			"sentinel", unknownTenantSentinel,
		)
	}
	return HashTenant(tenantID)
}

// Name 返回拦截器名称（日志用）。
func (it *SanitizeRestoreInterceptor) Name() string { return RestoreInterceptorName }

// InterceptNonStream 还原非流式响应中的占位符。
func (it *SanitizeRestoreInterceptor) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if it == nil || it.sanitizer == nil || req == nil || len(req.ResponseBody) == 0 {
		return nil, nil
	}
	if req.SessionID == "" {
		return nil, nil
	}

	tenantHash := it.tenantHashFor(ctx, req.TenantID, req.SessionID)
	sm, err := it.loadMap(ctx, tenantHash, req.SessionID)
	if err != nil {
		it.logger.Warn("sanitize_restore: load map failed, skip restore",
			"error", err, "session_id", req.SessionID, "tenant_id", req.TenantID)
		return nil, nil
	}
	if len(sm) == 0 {
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

	restored, err := it.restoreResponseBody(ctx, req.ResponseBody, sm)
	if err != nil || restored == nil {
		return nil, nil
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
// 降级：chain 为 nil / Redis 不可用 / 不含 sessionID / 无 placeholder 时
// 返回 nil，原样透传。
func (it *SanitizeRestoreInterceptor) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	if it == nil || it.sanitizer == nil || len(chunk) == 0 {
		return nil, nil
	}
	if meta == nil || meta.SessionID == "" {
		return nil, nil
	}

	tenantHash := it.tenantHashFor(ctx, meta.TenantID, meta.SessionID)
	sm, err := it.loadMap(ctx, tenantHash, meta.SessionID)
	if err != nil {
		it.logger.Warn("sanitize_restore: stream chunk load map failed, passthrough",
			"error", err, "session_id", meta.SessionID, "tenant_id", meta.TenantID)
		return nil, nil
	}
	if len(sm) == 0 {
		return nil, nil
	}

	modified, changed, err := it.restoreStreamChunk(ctx, chunk, sm)
	if err != nil || !changed {
		// 解析失败 / 无 placeholder → 原样透传（不要因为格式问题阻断流式）
		return nil, nil
	}

	return &response.ChunkResult{
		ModifiedChunk: modified,
	}, nil
}

// InterceptStreamEnd 流结束时 body 已重组为非流式形态，复用非流式还原。
// 注：流式响应字节已在流中发送给客户端，还原只影响持久化/观测。
func (it *SanitizeRestoreInterceptor) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	if it == nil || it.sanitizer == nil || meta == nil || len(meta.ResponseBody) == 0 || meta.SessionID == "" {
		return nil, nil
	}

	tenantHash := it.tenantHashFor(ctx, meta.TenantID, meta.SessionID)
	sm, err := it.loadMap(ctx, tenantHash, meta.SessionID)
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
//   - OpenAI Responses API delta：response.output_text.delta / content_part.delta
//   - Anthropic Messages delta：content_block_delta.delta.text
//
// 解析失败 / 不识别 schema → 返回 (nil, false, nil)，由 caller 原样透传。
// 成功但无 placeholder → 返回 (nil, false, nil)，同样原样透传（避免无谓的
// JSON 重序列化引入额外 marshal/unmarshal 噪声）。
//
// 跨 chunk 占位符：RestoreOutputOrMask 是纯字符串替换，未匹配部分由下一个
// chunk 继续处理，因此 chunk-by-chunk 处理是安全的。
func (it *SanitizeRestoreInterceptor) restoreStreamChunk(ctx context.Context, chunk []byte, sm SanitizeMap) ([]byte, bool, error) {
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
		return nil, false, nil
	}
	// 此时 rest 是「data: 行换行符之后剩余的内容」。SSE 帧分隔符
	// （\n\n 或 \n<空行>\n）就在这里。直接保留为尾缀，无需进一步解析。
	trailing = rest

	// 2. JSON 反序列化为通用结构。失败 → 透传（不要因为单 chunk 格式问题
	//    阻断整条流；典型场景：chunk 携带的是控制字段而非 content 增量）。
	var raw map[string]any
	if err := json.Unmarshal(jsonPayload, &raw); err != nil {
		return nil, false, nil
	}

	// 3. 在三种 delta schema 中做占位符替换
	changed := false
	changed = it.restoreStreamOpenAIDelta(ctx, raw, sm) || changed
	changed = it.restoreStreamAnthropicDelta(ctx, raw, sm) || changed
	changed = it.restoreStreamResponsesDelta(ctx, raw, sm) || changed
	if !changed {
		return nil, false, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false, nil
	}

	// 4. 重组 framing：注释行 + event: 行 + data: + 重新序列化的 JSON + 尾缀
	var buf bytes.Buffer
	buf.Write(prefixBuf.Bytes())
	buf.WriteString("data: ")
	buf.Write(out)
	if len(trailing) > 0 {
		buf.Write(trailing)
	}
	return buf.Bytes(), true, nil
}

// restoreStreamOpenAIDelta 处理 OpenAI chat completion delta schema
// (choices[].delta.content)。返回是否替换了占位符。
func (it *SanitizeRestoreInterceptor) restoreStreamOpenAIDelta(ctx context.Context, raw map[string]any, sm SanitizeMap) bool {
	choices, ok := raw["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, cAny := range choices {
		c, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := c["delta"].(map[string]any)
		if !ok {
			continue
		}
		if !restoreStringField(ctx, it.sanitizer, delta, "content", sm) {
			continue
		}
		changed = true
	}
	return changed
}

// restoreStreamAnthropicDelta 处理 Anthropic Messages delta schema
// (type="content_block_delta" + delta.text)。返回是否替换了占位符。
func (it *SanitizeRestoreInterceptor) restoreStreamAnthropicDelta(ctx context.Context, raw map[string]any, sm SanitizeMap) bool {
	// Anthropic 在 SSE 流中既发送 type="content_block_start" 等控制事件，
	// 也发送 type="content_block_delta" 携带 delta.text 文本增量。
	// 只处理 content_block_delta，避免误改控制字段。
	if t, _ := raw["type"].(string); t != "content_block_delta" {
		return false
	}
	delta, ok := raw["delta"].(map[string]any)
	if !ok {
		return false
	}
	return restoreStringField(ctx, it.sanitizer, delta, "text", sm)
}

// restoreStreamResponsesDelta 处理 OpenAI Responses API delta schema
// (type="response.output_text.delta" + delta)。返回是否替换了占位符。
func (it *SanitizeRestoreInterceptor) restoreStreamResponsesDelta(ctx context.Context, raw map[string]any, sm SanitizeMap) bool {
	if t, _ := raw["type"].(string); t != "response.output_text.delta" {
		return false
	}
	return restoreStringField(ctx, it.sanitizer, raw, "delta", sm)
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
//
// T11-P0: tenantHash 与写入侧（SanitizeRedisKey）严格对齐；缺 tenant 的请求
// 由 caller（tenantHashFor）负责落到 unknownTenantSentinel 桶。
func (it *SanitizeRestoreInterceptor) loadMap(ctx context.Context, tenantHash, sessionID string) (SanitizeMap, error) {
	if it.redis == nil {
		return nil, nil
	}
	key := SanitizeRedisKey(tenantHash, sessionID)
	vals, err := it.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil
	}
	sm := make(SanitizeMap, len(vals))
	for ph, val := range vals {
		sm[ph] = val
	}
	// 每次还原都刷新 TTL（用户继续会话）
	_ = it.redis.Expire(ctx, key, it.ttl).Err()
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

// restoreResponseBody 还原 OpenAI 响应体 choices[].message.content 中的占位符。
//
// 还原策略（统一使用 RestoreOutputOrMask）：
//   - role=assistant：还原为真实敏感值；映射表中没有的占位符用 [REDACTED] 替换
//     防止上游注入的占位符文本泄漏
//   - role=user/tool/function 等：还原 + mask（语义同 assistant，但生产路径上
//     这些 role 的响应消息通常不含 placeholder）
func (it *SanitizeRestoreInterceptor) restoreResponseBody(ctx context.Context, body []byte, sm SanitizeMap) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
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
		msg, ok := c["message"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok {
			continue
		}

		// 一律用 RestoreOutputOrMask：已知占位符还原 + 未知占位符 mask，
		// 防止 {SENSITIVE:type:99} 这种 raw 文本泄漏到客户端。
		restored, err := it.sanitizer.RestoreOutputOrMask(ctx, content, sm)
		if err != nil {
			continue
		}
		if restored != content {
			msg["content"] = restored
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

// SanitizeRedisKey 生成会话级映射表的 Redis key。
//
// T11-P0 v2: key 增加 tenantHash 段（session:{tenantHash}:{sessionID}:sanitize），
// 防止跨租户共享 sessionID 时互相读到对方的占位符映射表。tenantHash 由 caller
// 通过 HashTenant(tenantID) 派生；空值时 caller（中间件 Wrap）已经负责落到
// unknownTenantSentinel 并打过 WARN 日志，所以本函数不再做二次 fallback。
// 压缩层（domains/hooks/compression.SessionSanitizeRedisKey）必须镜像同一
// 格式，由 TestSessionSanitizeRedisKeyMatches 锁定。
func SanitizeRedisKey(tenantHash, sessionID string) string {
	return fmt.Sprintf("session:%s:%s:sanitize", tenantHash, sessionID)
}

// buildSanitizeInfoForSession derives the compression.SanitizeInfo bridge
// payload from the merged placeholder map (SC-1). Counts are classified by
// parsing the placeholder token, so the merged map (no per-message
// Fragments) is enough.
//
// T11-P0: tenantHash 进入 MapRef（与写入侧 Redis key 对齐）。
func buildSanitizeInfoForSession(tenantHash, sessionID string, sm SanitizeMap) compression.SanitizeInfo {
	keys := make(map[string]struct{}, len(sm))
	for ph := range sm {
		keys[ph] = struct{}{}
	}
	return compression.BuildSanitizeInfo(tenantHash, sessionID, func(placeholder string) (string, bool) {
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
//
// T11-P0 v2: 与 SanitizeRedisKey 同形态（session:{tenantHash}:{sid}:sanitize:offsets）。
func SanitizeOffsetRedisKey(tenantHash, sessionID string) string {
	return fmt.Sprintf("session:%s:%s:sanitize:offsets", tenantHash, sessionID)
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

// readBody 读取请求体并把它恢复回 r.Body，使后续 handler 仍能完整读取。
//
// 2026-08-07 事故修复：此前只读取、不恢复，导致下游 chatHandler 读到空 body
// 并以 json_parse_error 400 拒绝请求。恢复动作必须在读取后立即完成 —
// 包含读取失败的情况（已消费的字节数不可退回，但把已读部分接回去比留一个
// 耗尽的 Body 更接近原状，且下游会给出准确的 body_read_error）。
func readBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	b := buf.Bytes()
	// 把已读字节接回 r.Body，避免下游拿到耗尽的 Body。
	// 不调用 r.Body.Close()：Go server 端 *http.Request 的 Body.Close 是
	// no-op（eofReader 链），关闭原 reader 不会归还连接；保持与 armor
	// middleware.withReplayedBody 一致即可。
	r.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return b, nil
}
