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
	"strings"
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
		// readBody 恢复 r.Body，因此下面每条 passthrough 路径都安全：
		// 无脱敏、脱敏失败、读取失败都会把原始 body 交给下游。
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

		// 有脱敏内容 → 用脱敏后的 body 覆盖，并把映射表放入请求 Context。
		// 无脱敏内容时 r.Body 已由 readBody 恢复为原始 body，无需处理。
		if sanitizedBody != nil {
			r.Body = io.NopCloser(bytes.NewReader(sanitizedBody))
			// 脱敏改写后长度变化，同步 ContentLength（仅此一项；
			// Content-Length header 是 server 端 Go 解析时填入的，下游
			// 全部走 r.ContentLength 字段，不再回读 header）。
			// 与 armor middleware.withReplayedBody 保持一致。
			r.ContentLength = int64(len(sanitizedBody))
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

	// 持久化映射表到 Redis（同时把每类本轮最大编号刷回 offset key）
	if m.redis != nil && sessionID != "" {
		if err := m.saveMapAndOffsets(ctx, sessionID, sm, usedCount); err != nil {
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
//
// 偏移量单独存于 session:{sid}:sanitize:offsets（Redis Hash，
// field=类型字符串, value=本类型已用最大编号）。每次请求只读一个
// 紧凑 hash，不再扫描整个 sanitize map。
func (m *SanitizeInputMiddleware) loadOffsets(ctx context.Context, sessionID string) map[SensitiveType]int {
	if m.redis == nil || sessionID == "" {
		return nil
	}
	vals, err := m.redis.HGetAll(ctx, SanitizeOffsetRedisKey(sessionID)).Result()
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

// saveMapAndOffsets 把映射表写入 Redis，并刷新每类最大编号到 offset key。
func (m *SanitizeInputMiddleware) saveMapAndOffsets(ctx context.Context, sessionID string, sm SanitizeMap, usedCount map[string]int) error {
	mapKey := SanitizeRedisKey(sessionID)

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

	// 刷新每类最大编号到 offset key（仅写入用过的类型，未用类型保留）
	if len(usedCount) > 0 {
		offsetFields := make(map[string]any, len(usedCount))
		for tStr, idx := range usedCount {
			offsetFields[tStr] = idx
		}
		offsetKey := SanitizeOffsetRedisKey(sessionID)
		if err := m.redis.HSet(ctx, offsetKey, offsetFields).Err(); err != nil {
			return err
		}
		_ = m.redis.Expire(ctx, offsetKey, m.ttl).Err()
	}

	// 刷新主 hash 的 TTL
	return m.redis.Expire(ctx, mapKey, m.ttl).Err()
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

	// === 验证占位符完整性（Phase 1 Task 1.1）===
	invalidPlaceholders := it.validatePlaceholders(ctx, req.ResponseBody, sm)
	if len(invalidPlaceholders) > 0 {
		it.logger.WarnContext(ctx, "sanitize_restore: detected invalid placeholders in LLM response",
			"invalid_placeholders", invalidPlaceholders,
			"invalid_count", len(invalidPlaceholders),
			"session_id", req.SessionID,
			"tenant_id", req.TenantID,
		)
		// TODO: 添加 Prometheus 指标（Task 1.1 后续）
		// metrics.SanitizePlaceholderTampering.WithLabelValues("llm_generated").Add(float64(len(invalidPlaceholders)))
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

	sm, err := it.loadMap(ctx, meta.SessionID)
	if err != nil {
		it.logger.Warn("sanitize_restore: stream chunk load map failed, passthrough",
			"error", err, "session_id", meta.SessionID)
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
// 与 domains/session/sanitize.go 保持一致的命名空间。
func SanitizeRedisKey(sessionID string) string {
	return fmt.Sprintf("session:%s:sanitize", sessionID)
}

// SanitizeOffsetRedisKey 生成会话级 offset（每类已用最大编号）的 Redis key。
//
// 独立于主 sanitize map：loadOffsets 时只读这个紧凑 hash，避免扫描整个 map。
// TTL 与主 hash 同步刷新。
func SanitizeOffsetRedisKey(sessionID string) string {
	return fmt.Sprintf("session:%s:sanitize:offsets", sessionID)
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
