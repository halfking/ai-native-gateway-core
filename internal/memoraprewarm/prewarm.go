// prewarm.go — 会话首请求的 memora 上下文预热（P1-2③ W1，ADR
// docs/adr/2026-11-02-memory-prewarm-injection.md §1）。
//
// G3 不变式的网关半边——**结构性**保证而非约定：
//   - 异步 goroutine：调用点 fire-and-forget，请求路径零等待；
//   - 800ms 总预算（HTTP 500ms + 旁路写 300ms）：超时即弃，不重试；
//   - 旁路写 Redis session hash 的 memora_context 字段（TTL 跟随会话）：
//     读侧（压缩组装）读不到就当没有——未配置/超时/不可用/miss 全同形；
//   - 任何失败只记计数，零错误上抛。
package memoraprewarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// 预算（ADR §1：800ms 总预算）。
const (
	DefaultHTTPTimeout  = 500 * time.Millisecond
	DefaultWriteTimeout = 300 * time.Millisecond
	// DefaultQueryRunes 是预热 query 的截取长度（首请求 prompt 前 500 字符）。
	DefaultQueryRunes = 500
	// FieldName 是 Redis session hash 上的旁路字段名。
	FieldName = "memora_context"
)

// Client 打 ASM 的 prewarm 端点（生产实现 PrewarmHTTPClient；测试 fake）。
type Client interface {
	// Prewarm 返回 (items JSON, true) 表示有可注入内容；
	// (nil, false) 表示不可用/超时/miss（调用方一律当"无注入"）。
	Prewarm(ctx context.Context, tenantID, sessionID, query string) ([]byte, bool)
}

// PrewarmHTTPClient 是 Client 的生产实现。
type PrewarmHTTPClient struct {
	baseURL    string // ASM 根地址，如 http://asm:8782
	token      string // plugin context token（与 /api/v1/* 同认证链）
	httpClient *http.Client
	logger     *slog.Logger
}

// NewPrewarmHTTPClient 从环境构造（AI_SESSION_MANAGER_URL 复用既有
// 配置面；PLUGIN_CONTEXT_TOKEN 同链）。baseURL/token 缺失 ⇒ 返回 nil
// （预热整体禁用——零 goroutine、零日志）。
func NewPrewarmHTTPClientFromEnv() *PrewarmHTTPClient {
	base := strings.TrimRight(os.Getenv("AI_SESSION_MANAGER_URL"), "/")
	token := os.Getenv("ASM_PLUGIN_CONTEXT_TOKEN")
	if base == "" || token == "" {
		return nil
	}
	return &PrewarmHTTPClient{
		baseURL:    base,
		token:      token,
		httpClient: &http.Client{Timeout: DefaultHTTPTimeout},
		logger:     slog.Default(),
	}
}

// Prewarm implements Client.
func (c *PrewarmHTTPClient) Prewarm(ctx context.Context, tenantID, sessionID, query string) ([]byte, bool) {
	body, _ := json.Marshal(map[string]any{
		"query":      query,
		"session_id": sessionID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/memory-recall/prewarm", strings.NewReader(string(body)))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		RecordPrewarm(ClassifyOutcome(0, err)) // timeout | unavailable
		return nil, false                      // 含超时：G3 超时即跳过
	}
	defer func() { _ = resp.Body.Close() }()
	// 204（不可用/miss 同形）与任何非 200 都当"无注入"。
	if resp.StatusCode != http.StatusOK {
		RecordPrewarm(ClassifyOutcome(resp.StatusCode, nil)) // miss | unavailable
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, false
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		RecordPrewarm(OutcomeUnavailable)
		return nil, false
	}
	RecordPrewarm(OutcomeHit)
	return payload, true
}

// RedisSideWriter 是旁路写的最小面（生产 *redis.Client HSet；测试 fake）。
type RedisSideWriter interface {
	WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error
}

// Trigger 在会话首请求点触发预热（fire-and-forget）。
// client/writer 任一为 nil ⇒ no-op（未装配=禁用，结构性无成本）。
// 返回值仅供测试观察生产路径是否启动（goroutine 内部结果不经此返回）。
func Trigger(client Client, writer RedisSideWriter, gate *Gate, logger *slog.Logger,
	tenantID, sessionID, firstPrompt string, sessionTTL time.Duration) {
	if client == nil || writer == nil || sessionID == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	if gate != nil && !gate.Allowed(tenantID) {
		return // 灰度门外（或开关关）：连 goroutine 都不启动
	}
	RecordPrewarm(OutcomeRequested)
	query := truncateRunes(strings.TrimSpace(firstPrompt), DefaultQueryRunes)
	if query == "" {
		return
	}
	go func() {
		defer func() {
			// G3：预热 panic 也绝不波及网关进程。
			if r := recover(); r != nil {
				logger.Warn("memora prewarm panicked (suppressed)", "session_id", sessionID, "panic", fmt.Sprint(r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), DefaultHTTPTimeout+DefaultWriteTimeout)
		defer cancel()
		payload, ok := client.Prewarm(ctx, tenantID, sessionID, query)
		if !ok {
			return
		}
		wctx, wcancel := context.WithTimeout(context.Background(), DefaultWriteTimeout)
		defer wcancel()
		if err := writer.WriteMemoraContext(wctx, sessionID, payload, sessionTTL); err != nil {
			logger.Warn("memora prewarm side-write failed (non-blocking)",
				"session_id", sessionID, "error", err)
		}
	}()
}

func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// RedisWriter 是生产旁路写适配器：HSet session:{id} 的 memora_context
// 字段并刷新 TTL（与 session_state.go 同一 key 空间；读侧经既有 hash
// 读取面，无需新通道）。
type RedisWriter struct {
	client *redis.Client
}

// NewRedisWriter 构造；client 不可为 nil（装配层保证）。
func NewRedisWriter(client *redis.Client) *RedisWriter { return &RedisWriter{client: client} }

// WriteMemoraContext implements RedisSideWriter.
func (w *RedisWriter) WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	if w == nil || w.client == nil {
		return errors.New("prewarm: nil redis writer")
	}
	if _, err := w.client.HSet(ctx, "session:"+sessionID, map[string]any{
		FieldName: string(payload),
	}).Result(); err != nil {
		return err
	}
	return w.client.Expire(ctx, "session:"+sessionID, ttl).Err()
}
