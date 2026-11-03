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
	// ★ 不在这里记 hit：五态口径里「hit：旁路写成功（有注入可用）」
	//   ⇒ 记它的时点是**旁路写成功之后**（由 Trigger 记）。
	//   在这里记会把「HTTP 200 但旁路写失败」也算成 hit ⇒ hit 虚高、
	//   漏斗对不上（有注入可用 ≠ HTTP 200）。
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
	query := truncateRunes(strings.TrimSpace(firstPrompt), DefaultQueryRunes)
	if query == "" {
		// ★ 空 query ⇒ 本次调用什么也做不了（不召回、不写、不注入）⇒
		//   **压根没发起预热** ⇒ 不计 requested。
		//   理由：requested 是「真发起了预热」的分母；若把没发起的也计进去，
		//   requested 就永远大于 hit+miss+timeout+unavailable 之和 ⇒
		//   漏斗不闭合。
		//   ★ 五态标签集是 ADR §2 钉死的（requested|hit|miss|timeout|
		//     unavailable）⇒ 这里**不新增**第 6 个态来装「空 query」，
		//     而是让「没发起」不进入漏斗。
		return
	}
	RecordPrewarm(OutcomeRequested)
	go func() {
		defer func() {
			// G3：预热 panic 也绝不波及网关进程。
			if r := recover(); r != nil {
				// ★ panic 也必须记终态（unavailable 兜底）：recover 只写日志
				//   会让这一轮在漏斗里悬空——requested 已计、四终态都没记
				//   ⇒ hit+miss+timeout+unavailable 求和永远对不上 requested
				//   （漏斗泄漏一格）。
				RecordPrewarm(OutcomeUnavailable)
				logger.Warn("memora prewarm panicked (suppressed)", "session_id", sessionID, "panic", fmt.Sprint(r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), DefaultHTTPTimeout+DefaultWriteTimeout)
		defer cancel()
		payload, ok := client.Prewarm(ctx, tenantID, sessionID, query)
		if !ok {
			return // 客户端已按 miss|timeout|unavailable 记过态
		}
		wctx, wcancel := context.WithTimeout(context.Background(), DefaultWriteTimeout)
		defer wcancel()
		if err := writer.WriteMemoraContext(wctx, sessionID, payload, sessionTTL); err != nil {
			// ★ 旁路写失败**必须**记态：五态口径写明「timeout：800ms 预算内
			//   未完成（HTTP 或写超时）」⇒ 写超时属 timeout、其余属
			//   unavailable。原先此路径只写日志不记指标 ⇒ 写失败在指标上
			//   完全不可见 ⇒ requested 与各终态对不上、漏斗不闭合。
			RecordPrewarm(ClassifyWriteError(err))
			logger.Warn("memora prewarm side-write failed (non-blocking)",
				"session_id", sessionID, "error", err)
			return
		}
		// ★ hit 的时点：旁路写成功 ⇒ 确有注入可用（口径「hit：旁路写成功
		//   （有注入可用）」）。
		RecordPrewarm(OutcomeHit)
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
//
// ★ HSET 与 EXPIRE 必须在**一个事务**里（TxPipeline ⇒ MULTI/EXEC）：
//
//	两条独立命令时，首请求场景下 session:{id} 尚不存在 ⇒ HSET 造出一个
//	**没有 TTL** 的 key；若进程在两条命令之间崩溃/连接中断 ⇒ 该 key 永久
//	驻留 ⇒ Redis 内存单调增长（key 泄漏）。
//	放进同一事务 ⇒ 「建 key」与「设 TTL」要么都成、要么都不成 ⇒ 不存在
//	「有 key 没 TTL」的中间态。
func (w *RedisWriter) WriteMemoraContext(ctx context.Context, sessionID string, payload []byte, ttl time.Duration) error {
	if w == nil || w.client == nil {
		return errors.New("prewarm: nil redis writer")
	}
	key := "session:" + sessionID
	pipe := w.client.TxPipeline()
	pipe.HSet(ctx, key, map[string]any{FieldName: string(payload)})
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}
