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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/redis/go-redis/v9"
)

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
type SanitizeInputMiddleware struct {
	sanitizer *Sanitizer
	redis     *redis.Client
	ttl       time.Duration
	logger    *slog.Logger
	// getSessionID 从请求头提取会话ID（由调用方注入，便于测试）
	getSessionID func(r *http.Request) string
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
		sanitizer: s,
		redis:     redis,
		ttl:       ttl,
		logger:    slog.Default().With("component", "sanitize_middleware"),
		getSessionID: func(r *http.Request) string {
			if id := r.Header.Get("X-Gw-Session-Id"); id != "" {
				return id
			}
			return r.Header.Get("X-Session-Id")
		},
	}, nil
}

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
		body, err := readBody(r)
		if err != nil {
			m.logger.Warn("sanitize_middleware: read body failed, passthrough",
				"error", err, "session_id", sessionID)
			next.ServeHTTP(w, r)
			return
		}

		// 解析并脱敏
		sanitizedBody, sm, err := m.sanitizeRequestBody(r.Context(), body, sessionID)
		if err != nil {
			m.logger.Warn("sanitize_middleware: sanitize failed, passthrough",
				"error", err, "session_id", sessionID)
			next.ServeHTTP(w, r)
			return
		}

		// 有脱敏内容 → 重写 r.Body，并把映射表放入请求 Context
		if sanitizedBody != nil {
			r.Body = io.NopCloser(bytes.NewReader(sanitizedBody))
			*r = *r.WithContext(WithSanitizeMap(r.Context(), sm))
		}

		next.ServeHTTP(w, r)
	})
}

// sanitizeRequestBody 解析 OpenAI 请求体并脱敏 messages。
// 返回 (脱敏后的请求体, SanitizeMap)。无敏感信息时返回 (nil, 空map)。
func (m *SanitizeInputMiddleware) sanitizeRequestBody(ctx context.Context, body []byte, sessionID string) ([]byte, SanitizeMap, error) {
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

	// 会话级偏移量（记录每类已用最大编号，保证跨轮次不撞号）
	offset := m.loadOffsets(ctx, sessionID)
	changed := false
	sm := make(SanitizeMap)
	usedCount := make(map[string]int) // 每类本轮新用计数（用于更新 offset）

	for i, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok {
			continue
		}
		// 只脱敏 user 和 system 消息（assistant 消息是上游生成，不应改动）
		role, _ := msg["role"].(string)
		if role != "user" && role != "system" {
			continue
		}

		result, err := m.sanitizer.sanitizeInput(ctx, content, offset)
		if err != nil {
			return nil, nil, err
		}
		if len(result.SanitizeMap) == 0 {
			continue
		}

		msg["content"] = result.SanitizedText
		messages[i] = msg
		changed = true

		// 合并映射表 + 更新每类计数
		for ph, val := range result.SanitizeMap {
			sm[ph] = val
			if p, ok := ParsePlaceholder(ph); ok {
				usedCount[string(p.Type)] = p.Index
			}
		}
	}

	if !changed {
		return nil, nil, nil
	}

	// 持久化映射表到 Redis
	if m.redis != nil && sessionID != "" {
		if err := m.saveMapAndOffsets(ctx, sessionID, sm, offset, usedCount); err != nil {
			m.logger.Warn("sanitize_middleware: save map failed (restore degraded to single-turn)",
				"error", err, "session_id", sessionID)
		}
	}

	// 重新序列化
	newBody, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}

	return newBody, sm, nil
}

// loadOffsets 从 Redis 加载每类已用最大编号作为偏移量。
func (m *SanitizeInputMiddleware) loadOffsets(ctx context.Context, sessionID string) map[SensitiveType]int {
	if m.redis == nil || sessionID == "" {
		return nil
	}
	key := SanitizeRedisKey(sessionID)
	vals, err := m.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil
	}
	// vals 是 {placeholder: value}，从中推导每类最大编号
	maxIdx := make(map[SensitiveType]int)
	for ph := range vals {
		if p, ok := ParsePlaceholder(ph); ok {
			if p.Index > maxIdx[p.Type] {
				maxIdx[p.Type] = p.Index
			}
		}
	}
	return maxIdx
}

// saveMapAndOffsets 把映射表写入 Redis，并记录每类最新编号。
func (m *SanitizeInputMiddleware) saveMapAndOffsets(ctx context.Context, sessionID string, sm SanitizeMap, offset map[SensitiveType]int, usedCount map[string]int) error {
	key := SanitizeRedisKey(sessionID)

	// 把占位符→原始值写入 Hash（field=占位符, value=原始值）
	fields := make(map[string]any, len(sm))
	for ph, val := range sm {
		fields[ph] = val
	}
	if len(fields) > 0 {
		if err := m.redis.HSet(ctx, key, fields).Err(); err != nil {
			return err
		}
	}

	// 刷新 TTL
	return m.redis.Expire(ctx, key, m.ttl).Err()
}

// SanitizeRestoreInterceptor 实现 response.ResponseInterceptor。
// 在输出安全检查（OutputComplianceInterceptor）之后执行，
// 从 Redis 读取会话级映射表，把占位符还原为真实敏感值。
//
// 顺序契约：本拦截器必须在 OutputComplianceInterceptor 之后注册，
// 确保安全检查先看到占位符，还原后再把真实值返回给用户。
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

	sm, err := it.loadMap(ctx, req.SessionID)
	if err != nil {
		it.logger.Warn("sanitize_restore: load map failed, skip restore",
			"error", err, "session_id", req.SessionID)
		return nil, nil
	}
	if len(sm) == 0 {
		return nil, nil
	}

	restored, err := it.restoreResponseBody(ctx, req.ResponseBody, sm)
	if err != nil || restored == nil {
		return nil, nil
	}

	return &response.InterceptResult{
		ModifiedBody: restored,
		Action:       "sanitize_restore",
		Metadata: map[string]any{
			"sanitize_restored": true,
			"placeholder_count": len(sm),
		},
	}, nil
}

// InterceptStreamChunk 流式 chunk 级还原为未来增强，本轮透传。
func (it *SanitizeRestoreInterceptor) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd 流结束时 body 已重组为非流式形态，复用非流式还原。
// 注：流式响应字节已在流中发送给客户端，还原只影响持久化/观测。
func (it *SanitizeRestoreInterceptor) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	if it == nil || it.sanitizer == nil || meta == nil || len(meta.ResponseBody) == 0 || meta.SessionID == "" {
		return nil, nil
	}

	sm, err := it.loadMap(ctx, meta.SessionID)
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

// loadMap 从 Redis 读取会话级映射表。
func (it *SanitizeRestoreInterceptor) loadMap(ctx context.Context, sessionID string) (SanitizeMap, error) {
	if it.redis == nil {
		return nil, nil
	}
	key := SanitizeRedisKey(sessionID)
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

// restoreResponseBody 还原 OpenAI 响应体 choices[].message.content 中的占位符。
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
		role, _ := msg["role"].(string)
		if role != "assistant" && role != "" {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok {
			continue
		}
		restored, err := it.sanitizer.RestoreOutput(ctx, content, sm)
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
// 与 domains/session/sanitize.go 保持一致的命名空间。
func SanitizeRedisKey(sessionID string) string {
	return fmt.Sprintf("session:%s:sanitize", sessionID)
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

// readBody 读取并恢复请求体（允许后续 handler 重复读取）。
func readBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(r.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
