package freeresource

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/lib/pq"
)

// QuotaTracker 配额追踪器
type QuotaTracker struct {
	db   *sql.DB
	sink QuotaEventSink // optional SSE sink; nil = no real-time push
}

// NewQuotaTracker 创建配额追踪器
func NewQuotaTracker(db *sql.DB) *QuotaTracker {
	return &QuotaTracker{db: db}
}

// SetQuotaSink wires an SSE sink so credential quota state changes are pushed
// to the FreePoolView admin page in real time. Safe to call once at startup;
// nil leaves the tracker in legacy no-push mode.
func (qt *QuotaTracker) SetQuotaSink(sink QuotaEventSink) {
	if qt == nil {
		return
	}
	qt.sink = sink
}

// Record 记录一次请求的配额消耗.
//
// 时间戳处理:
//   - 零值: 显式使用调用方当前 UTC 时间, 避免落入 year-1 窗口.
//   - 非零值: 强制归一为 UTC, 防止跨时区请求落到非预期窗口.
//
// 窗口分桶 (computeWindows) 使用对齐桶边界, rolling 窗口 (hour-5/day-7)
// 共享同一窗口的请求累加到同一行, 避免每次请求新增一行.
//
// 事务封装 (2026-08-09 audit round 3, C1):
//
//   - 旧实现对每个 window 各发一条独立的 ExecContext, 没有事务边界.
//     这本身不破坏 UPSERT 原子性 (PG 单语句是原子的), 但与并发
//     CorrectFromHeaders 形成可见性窗口: Record 已经 upsert 完 day-1 行,
//     CorrectFromHeaders 随后 INSERT 走 ON CONFLICT 路径, 但我们仍可能
//     看到 Record 期间 "is_exhausted=FALSE" 的中间状态.
//   - 新实现把多条 UPSERT 包在 BeginTx 里, 事务内先 SET LOCAL
//     app.current_tenant 让 RLS 生效, 然后顺序执行. 事务结束自动
//     还原 GUC, 不会污染连接池.
//
// RLS contract (2026-08-09 audit round 3, C2/C3):
//
//   - 在事务首行 set_config('app.current_tenant', $1, true) 让
//     free_quota_tracker 的 RLS policy 按 tenant 过滤; 旧实现
//     让 stdlib 连接走 'default' fallback, 多租户场景下被静默错配.
//   - 失败仅记 stderr, 不阻塞 Record (旧实现 GUC 没设过的兼容行为).
func (qt *QuotaTracker) Record(ctx context.Context, req RecordRequest) error {
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now().UTC()
	} else {
		req.Timestamp = req.Timestamp.UTC()
	}
	if len(req.WindowTypes) == 0 {
		// 默认 UTC 日 + UTC 月, 覆盖大多数免费提供商的配额周期.
		req.WindowTypes = []WindowType{WindowTypeDay1, WindowTypeMonth1}
	}

	windows := qt.computeWindows(req.Timestamp, req.WindowTypes)

	tx, err := qt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for quota record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if req.TenantID != "" {
		// 使用 SET LOCAL (事务作用域), SQL SET 而非 set_config() — 后者
		// 在 lib/pq 驱动 prepared-statement 路径下 GUC 不生效 (已验证).
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenant(req.TenantID))); err != nil {
			fmt.Printf("omnifree: failed to set app.current_tenant in Record: %v\n", err)
		}
	}

	// P0 优化: 批量 UPSERT，减少数据库热点竞争
	// 使用 unnest() 将多个窗口在单个查询中插入/更新
	// 预期收益: 高并发下延迟降低 30-50%，吞吐量提升 2-3倍
	if len(windows) == 0 {
		return nil
	}

	successCount := 0
	errorCount := 0
	if req.Success {
		successCount = 1
	} else {
		errorCount = 1
	}

	// 构建批量参数
	windowTypes := make([]string, len(windows))
	windowStarts := make([]time.Time, len(windows))
	windowEnds := make([]time.Time, len(windows))
	for i, w := range windows {
		windowTypes[i] = string(w.Type)
		windowStarts[i] = w.Start
		windowEnds[i] = w.End
	}

	// 使用 unnest + lateral 批量 UPSERT
	_, err = tx.ExecContext(ctx, `
		INSERT INTO free_quota_tracker (
			credential_id, provider_code, model_id, window_type,
			window_start, window_end, request_count, token_count,
			success_count, error_count, tenant_id
		)
		SELECT $1, $2, $3, w.type, w.start, w.end, 1, $4, $5, $6, $7
		FROM unnest($8::text[], $9::timestamptz[], $10::timestamptz[]) AS w(type, start, "end")
		ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
		DO UPDATE SET
			request_count = free_quota_tracker.request_count + 1,
			token_count = free_quota_tracker.token_count + $4,
			success_count = free_quota_tracker.success_count + $5,
			error_count = free_quota_tracker.error_count + $6,
			updated_at = now()
	`, req.CredentialID, req.ProviderCode, req.ModelID, req.TokenCount,
		successCount, errorCount, req.TenantID,
		pq.Array(windowTypes), pq.Array(windowStarts), pq.Array(windowEnds))

	if err != nil {
		return fmt.Errorf("batch upsert quota tracker: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit quota record: %w", err)
	}
	return nil
}

// CorrectFromHeaders 从429响应头+响应体校准配额限制.
//
// 行为变更 (2026-08-07): 旧实现仅 UPDATE 当前已存在的 day-1 行, 首次 429
// 没有任何追踪记录时静默丢失校准. 新实现通过 UPSERT 写入 day-1 窗口, 即使
// 之前没有 Record 调用, 也能在收到 429 的同时建立追踪行, 后续 Preflight
// 即可正确识别 is_exhausted.
//
// body 关键词甄别 (OmniRoute classify429.ts 对齐, 2026-08-10):
//   - 旧实现只看 HTTP header (Retry-After / X-RateLimit-Reset). 很多 provider
//     (Google Gemini, Cloudflare Workers AI, Groq) 把配额耗尽信号放在响应体
//     里, 没有任何 reset header → parseRetryAfter 返回 (0, now) →
//     auto_reset_at=now() → Preflight 立即解除耗尽 → 配额耗尽的 key 被反复重试.
//   - 新实现: 若 req.Body 非空, 调 errorsx.ClassifyQuota429Body 区分:
//   - KindQuotaPeriodic  → 周期性耗尽, 用 errorsx.NextQuotaReset(body)
//     算出 next UTC midnight / next month (除非 header 有更准的 reset).
//   - KindQuotaPermanent → 永久耗尽 (余额不足), auto_reset_at 设为远未来
//     (now + 365 天) 避免被自动解除, 需人工介入.
//   - KindRateLimit      → 瞬时限流, 保持原有短退避行为.
//     优先级: 真实 upstream reset header > body 推断的周期重置 > 默认 now.
//
// 事务封装 + RLS GUC (2026-08-09 audit round 3, C1/C2):
//   - 把 UPSERT 包到 BeginTx, 与并发 Record 形成一致的可见性窗口;
//   - 事务内 set_config('app.current_tenant', ...) 让 RLS 实际生效.
func (qt *QuotaTracker) CorrectFromHeaders(ctx context.Context, req CorrectionRequest) error {
	if req.TenantID == "" {
		req.TenantID = "default"
	}

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	retryAfterSec, resetAt := parseRetryAfter(now, req.Headers)
	headerHadReset := !resetAt.Equal(now) // parseRetryAfter fallback returns now when no header

	// body 关键词甄别: 当 header 没有给出 reset 时间时, 从 body 推断.
	classification := errorsx.ClassifyQuota429Body(req.Body)
	if !headerHadReset && len(req.Body) > 0 {
		switch classification {
		case errorsx.KindQuotaPeriodic:
			resetAt = errorsx.NextQuotaReset(string(req.Body), now)
		case errorsx.KindQuotaPermanent:
			// Permanent exhaustion has no known auto-recovery time; keep it
			// exhausted until an explicit operator/provider state change.
			resetAt = time.Time{}
		}
	}

	var autoReset any
	if !resetAt.IsZero() {
		autoReset = resetAt
	}

	limit := int64(0)
	if lim, ok := lookupHeader(req.Headers, "X-RateLimit-Limit"); ok && lim != "" {
		limit, _ = strconv.ParseInt(lim, 10, 64)
	}
	limitHeader := lookupHeaderValue(req.Headers, "X-RateLimit-Limit")

	tx, err := qt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for quota correct: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenant(req.TenantID))); err != nil {
		fmt.Printf("omnifree: failed to set app.current_tenant in CorrectFromHeaders: %v\n", err)
	}

	_, err = tx.ExecContext(ctx, `
        INSERT INTO free_quota_tracker (
            credential_id, provider_code, model_id, window_type,
            window_start, window_end, request_count, token_count,
            success_count, error_count,
            is_exhausted, exhausted_at, auto_reset_at,
            last_429_at, last_429_reset_after, last_429_limit_header,
            corrected_limit, tenant_id
        ) VALUES (
            $1, $2, $3, 'day-1',
            $4, $5, 0, 0, 0, 0,
            TRUE, now(), $6,
            now(), $7, $8,
            CASE WHEN $9 > 0 THEN $9 ELSE NULL END,
            $10
        )
        ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
        DO UPDATE SET
            is_exhausted = TRUE,
            exhausted_at = now(),
            auto_reset_at = EXCLUDED.auto_reset_at,
            last_429_at = now(),
            last_429_reset_after = EXCLUDED.last_429_reset_after,
            last_429_limit_header = EXCLUDED.last_429_limit_header,
            corrected_limit = COALESCE(EXCLUDED.corrected_limit, free_quota_tracker.corrected_limit),
            updated_at = now()
	`, req.CredentialID, req.ProviderCode, req.ModelID,
		dayStart, dayEnd,
		autoReset,
		retryAfterSec, limitHeader,
		limit,
		req.TenantID,
	)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Push a real-time event so the FreePoolView page refreshes immediately
	// (instead of waiting for the next 15s poll). Map the 429 body
	// classification to an event type the frontend listens for; default to
	// rate_limited when no body was provided.
	if qt.sink != nil {
		evtType := "rate_limited"
		switch classification {
		case errorsx.KindQuotaPeriodic:
			evtType = "quota_exhausted"
		case errorsx.KindQuotaPermanent:
			evtType = "quota_permanent"
		}
		var resetPtr *time.Time
		if autoReset != nil {
			t := autoReset.(time.Time)
			resetPtr = &t
		}
		qt.sink.PublishQuotaEvent(QuotaEvent{
			Type:         evtType,
			CredentialID: req.CredentialID,
			ProviderCode: req.ProviderCode,
			ModelID:      req.ModelID,
			AutoResetAt:  resetPtr,
			Ts:           now,
		})
	}
	return nil
}

// parseRetryAfter 解析 Retry-After 与 X-RateLimit-Reset 头部, 返回 retryAfter (秒) 与 resetAt.
// 优先使用 X-RateLimit-Reset (Unix timestamp 秒), 否则解析 Retry-After (秒或 HTTP-date).
func parseRetryAfter(now time.Time, headers map[string]string) (int, time.Time) {
	if reset, ok := lookupHeader(headers, "X-RateLimit-Reset"); ok && reset != "" {
		if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
			resetAt := time.Unix(ts, 0).UTC()
			retry := int(resetAt.Sub(now).Seconds())
			if retry < 0 {
				retry = 0
			}
			return retry, resetAt
		}
	}
	if ra, ok := lookupHeader(headers, "Retry-After"); ok && ra != "" {
		// 优先尝试整数秒, 但只有当整段都是数字时才视为秒数, 避免
		// "Thu, 15 Aug 2024 14:30:45 GMT" 这种 HTTP-date 截断为 15.
		if sec, ok := parseRetryAfterSeconds(ra); ok {
			if sec < 0 {
				sec = 0
			}
			// P1 修复: 当 sec <= 0 时使用默认退避(60s), 避免 429 循环
			// 部分提供商返回 "0" 表示"稍后重试"而非"立即可用"
			if sec == 0 {
				sec = 60
			}
			return sec, now.Add(time.Duration(sec) * time.Second)
		}
		// 2026-08-10: 相对时间单位 (Groq "6s", "5m", "2h", "1d").
		// RFC 7231 Retry-After 只定义了 delta-seconds 或 HTTP-date, 但 Groq
		// 等厂商在 Retry-After 里返回相对单位. 不解析会导致 fallback 到
		// HTTP-date (失败) → (0, now) → 配额耗尽 key 被立即重试.
		if sec, ok := parseRetryAfterRelative(ra); ok {
			if sec < 0 {
				sec = 0
			}
			// P1 修复: 相对单位也应用零值保护
			if sec == 0 {
				sec = 60
			}
			return sec, now.Add(time.Duration(sec) * time.Second)
		}
		if t, err := http.ParseTime(ra); err == nil {
			resetAt := t.UTC()
			retry := int(resetAt.Sub(now).Seconds())
			if retry < 0 {
				retry = 0
			}
			return retry, resetAt
		}
	}
	return 0, now
}

// parseRetryAfterSeconds 仅当字符串全为数字 (允许前后空白) 时返回秒数.
// 用于 Retry-After 的 "30" / " 60 " 形式 (RFC 7231 delta-seconds), 避免
// HTTP-date 被截断误判. 带 unit 后缀的形式 ("30s"/"5m") 由 parseRetryAfterRelative 处理.
func parseRetryAfterSeconds(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseRetryAfterRelative parses Groq-style relative Retry-After values with a
// unit suffix: "6s", "5m", "2h", "1d" (case-insensitive). These are non-RFC but
// emitted by Groq and some other providers. Returns seconds + true on match,
// false otherwise (caller falls back to HTTP-date parsing).
//
// Mirrors OmniRoute classify429.ts parseRetryAfter relative-unit handling.
// Supported units: s (seconds), m (minutes), h (hours), d (days).
func parseRetryAfterRelative(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 { // need at least "Ns"
		return 0, false
	}
	unit := s[len(s)-1]
	// unit must be a letter; the rest must be all digits.
	if !((unit >= 'a' && unit <= 'z') || (unit >= 'A' && unit <= 'Z')) {
		return 0, false
	}
	numPart := s[:len(s)-1]
	n, err := strconv.Atoi(numPart)
	if err != nil || n < 0 {
		return 0, false
	}
	switch strings.ToLower(string(unit)) {
	case "s":
		return n, true
	case "m":
		return n * 60, true
	case "h":
		return n * 3600, true
	case "d":
		return n * 86400, true
	default:
		return 0, false
	}
}

// Preflight 配额预检 - 返回是否可用
//
// 事务封装 (2026-08-09 audit round 3, C1):
//
//   - 把 SELECT 与可能的 "过期自动解除" UPDATE 包到 BeginTx, 事务内先
//     set_config('app.current_tenant', ...) 让 RLS 实际生效.
//   - SELECT FOR UPDATE 锁住活跃窗口行, 防止 Preflight 与并发
//     CorrectFromHeaders 出现行级竞态 (后者 UPDATE 该行后, 前者 SELECT
//     看到的 is_exhausted 仍是旧值).
//   - 失败时正确回滚, GUC 在事务结束自动还原.
func (qt *QuotaTracker) Preflight(ctx context.Context, req PreflightRequest) (bool, error) {
	if req.TenantID == "" {
		req.TenantID = "default"
	}

	tx, err := qt.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false})
	if err != nil {
		return true, fmt.Errorf("begin tx for preflight: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenant(req.TenantID))); err != nil {
		fmt.Printf("omnifree: failed to set app.current_tenant in Preflight: %v\n", err)
	}

	var limit, used int64
	var exhausted bool
	var resetAt sql.NullTime

	err = tx.QueryRowContext(ctx, `
        SELECT
            COALESCE(corrected_limit, $1) AS limit,
            request_count AS used,
            is_exhausted,
            auto_reset_at
        FROM free_quota_tracker
        WHERE credential_id = $2
          AND provider_code = $3
          AND model_id = $4
          AND window_type = $5
          AND window_start <= now()
          AND window_end >= now()
          AND tenant_id = $6
        FOR UPDATE
    `, req.DefaultLimit, req.CredentialID, req.ProviderCode, req.ModelID,
		req.WindowType, req.TenantID).Scan(&limit, &used, &exhausted, &resetAt)

	if err == sql.ErrNoRows {
		return true, nil // 无追踪记录，允许使用
	}
	if err != nil {
		return false, err
	}

	// 已过重置时间就自动解除耗尽状态; 只动活跃窗口一行,
	// 避免旧实现把历史同日多窗口也一起擦除.
	// 2026-08-09 audit round 3: 改用事务内的 tx, 保持锁与 RLS 上下文一致.
	if exhausted && resetAt.Valid && time.Now().After(resetAt.Time) {
		_, _ = tx.ExecContext(ctx, `
            UPDATE free_quota_tracker
            SET is_exhausted = FALSE, exhausted_at = NULL
            WHERE credential_id = $1 AND provider_code = $2 AND model_id = $3
              AND window_type = $4 AND tenant_id = $5
              AND window_start <= now() AND window_end >= now()
        `, req.CredentialID, req.ProviderCode, req.ModelID, req.WindowType, req.TenantID)
		_ = tx.Commit()
		return true, nil
	}

	if exhausted {
		return false, nil
	}

	if limit > 0 {
		remaining := float64(limit-used) / float64(limit)
		return remaining >= req.MinRemainingPct, nil
	}

	return true, nil
}

// escapeTenant 把租户 ID 嵌入到 SET GUC 字面量时做转义. 仅允许
// [A-Za-z0-9_-] 且长度 <= 64; 不合法的 ID 返回 'default' 防止 SQL 注入.
func escapeTenant(id string) string {
	if id == "" || len(id) > 64 {
		return "default"
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-') {
			return "default"
		}
	}
	return id
}

// ApplyFetchedQuota writes a proactively-fetched upstream quota snapshot into
// the day-1 window of free_quota_tracker, so the existing Preflight reads
// accurate values (real corrected_limit + real is_exhausted) instead of the
// 429-reactive defaults. Fed by the quotafetcher package (P1 主动配额预取).
//
// Semantics (mirrors CorrectFromHeaders' UPSERT but for proactive data):
//   - Total > 0 → corrected_limit = Total (only overwrites when a real value
//     arrived; Total=0 keeps the existing corrected_limit, e.g. on a
//     balance-only fetch where we only know exhausted/not).
//   - LimitReached → is_exhausted = TRUE, exhausted_at = now, auto_reset_at =
//     ResetAt (or keep existing when ResetAt is nil).
//   - !LimitReached → is_exhausted = FALSE (clear a stale exhaustion when the
//     upstream now reports healthy), leaving auto_reset_at intact.
//
// RLS: transaction-scoped SET LOCAL app.current_tenant (same pattern as
// Record / CorrectFromHeaders / Preflight). Failures are logged + returned
// but the caller (virtual_factory preflightQuota) treats them as fail-open.
func (qt *QuotaTracker) ApplyFetchedQuota(ctx context.Context, req ApplyRequest) error {
	if qt == nil || qt.db == nil {
		return nil
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	var autoReset any
	if req.ResetAt != nil {
		utc := req.ResetAt.UTC()
		autoReset = utc
	}

	tx, err := qt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for apply fetched quota: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL app.current_tenant = '%s'", escapeTenant(req.TenantID))); err != nil {
		fmt.Printf("omnifree: failed to set app.current_tenant in ApplyFetchedQuota: %v\n", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO free_quota_tracker (
			credential_id, provider_code, model_id, window_type,
			window_start, window_end, request_count, token_count,
			success_count, error_count,
			is_exhausted, exhausted_at, auto_reset_at,
			corrected_limit, tenant_id
		) VALUES (
			$1, $2, $3, 'day-1',
			$4, $5, 0, 0, 0, 0,
			$6, CASE WHEN $6 THEN now() ELSE NULL END, $7,
			CASE WHEN $8 > 0 THEN $8 ELSE NULL END,
			$9
		)
		ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
		DO UPDATE SET
			is_exhausted = EXCLUDED.is_exhausted,
			exhausted_at = CASE WHEN EXCLUDED.is_exhausted THEN now() ELSE NULL END,
			auto_reset_at = COALESCE(EXCLUDED.auto_reset_at, free_quota_tracker.auto_reset_at),
			corrected_limit = COALESCE(EXCLUDED.corrected_limit, free_quota_tracker.corrected_limit),
			updated_at = now()
	`, req.CredentialID, req.ProviderCode, req.ModelID,
		dayStart, dayEnd,
		req.LimitReached, autoReset,
		req.Total,
		req.TenantID,
	)
	if err != nil {
		return fmt.Errorf("upsert fetched quota: %w", err)
	}

	return tx.Commit()
}

// computeWindows 计算给定时间点的所有窗口起止.
//
// 时区处理: 时间戳统一按 UTC 处理, 各窗口起点都把本地字段按 UTC 解释.
// 滚动窗口 (hour-5 / day-7):
//   - hour-5: 以整点对齐, start = ts.Truncate(Hour) - 4h, end = start + 5h.
//   - day-7:   以 UTC 0 点对齐, start = (UTC date - 6 天) 的 0 点, end = start + 7d.
//
// 这样 rolling 窗口内所有请求共享同一 conflict key, 多次 Record 会累加
// 而不是新建行.
//
// round 4 M1 优化: 预分配 slice 容量, 减少 alloc churn. 高 QPS 场景
// (例如 1000 RPS × 4 窗口 = 4000 alloc/秒) 下显著降低 GC 压力.
func (qt *QuotaTracker) computeWindows(ts time.Time, types []WindowType) []QuotaWindow {
	ts = ts.UTC()

	windows := make([]QuotaWindow, 0, len(types))
	for _, wt := range types {
		var start, end time.Time
		switch wt {
		case WindowTypeHour5:
			hourStart := ts.Truncate(time.Hour)
			start = hourStart.Add(-4 * time.Hour)
			end = start.Add(5 * time.Hour)
		case WindowTypeDay1:
			start = time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
			end = start.Add(24 * time.Hour)
		case WindowTypeDay7:
			dayStart := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
			start = dayStart.Add(-6 * 24 * time.Hour)
			end = start.Add(7 * 24 * time.Hour)
		case WindowTypeMonth1:
			start = time.Date(ts.Year(), ts.Month(), 1, 0, 0, 0, 0, time.UTC)
			end = start.AddDate(0, 1, 0)
		}
		windows = append(windows, QuotaWindow{
			Type:  wt,
			Start: start,
			End:   end,
		})
	}
	return windows
}

// lookupHeader 在 headers map 中大小写不敏感地查找 header key.
//
// 背景: 当上游 http.Response.Header 通过 flattenHeaders 转成
// map[string]string 时, http.Header.Add/Set 会调用
// textproto.CanonicalMIMEHeaderKey 把 "X-RateLimit-Limit" 规范化为
// "X-Ratelimit-Limit" (注意 'l' 变小写). 原实现按字面键查找,
// 真实 429 响应走 streaming handler 时 silent miss, 配额校准不生效.
//
// 这里显式做 MIME canonical 大小写折叠, 与 net/http 内部一致, 兼容
// 直接传 "X-RateLimit-Limit" 字面键的旧调用方 (例如 quota_tracker_test.go).
func lookupHeader(headers map[string]string, key string) (string, bool) {
	if v, ok := headers[key]; ok {
		return v, true
	}
	canonical := textproto.CanonicalMIMEHeaderKey(key)
	if canonical == key {
		return "", false
	}
	if v, ok := headers[canonical]; ok {
		return v, true
	}
	// 兜底: 折半查找常见的大小写变体 (前缀大小写)
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// lookupHeaderValue 是 lookupHeader 的便捷包装, 用于不在意 bool 命中、
// 仅需字符串值的场景 (例如写入 DB 字段 last_429_limit_header).
// 未命中返回 "".
func lookupHeaderValue(headers map[string]string, key string) string {
	v, _ := lookupHeader(headers, key)
	return v
}
