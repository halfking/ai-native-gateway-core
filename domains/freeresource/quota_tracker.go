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
)

// QuotaTracker 配额追踪器
type QuotaTracker struct {
	db *sql.DB
}

// NewQuotaTracker 创建配额追踪器
func NewQuotaTracker(db *sql.DB) *QuotaTracker {
	return &QuotaTracker{db: db}
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

	for _, w := range windows {
		successCount := 0
		errorCount := 0
		if req.Success {
			successCount = 1
		} else {
			errorCount = 1
		}

		_, err := tx.ExecContext(ctx, `
            INSERT INTO free_quota_tracker (
                credential_id, provider_code, model_id, window_type,
                window_start, window_end, request_count, token_count,
                success_count, error_count, tenant_id
            ) VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8, $9, $10)
            ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
            DO UPDATE SET
                request_count = free_quota_tracker.request_count + 1,
                token_count = free_quota_tracker.token_count + $7,
                success_count = free_quota_tracker.success_count + $8,
                error_count = free_quota_tracker.error_count + $9,
                updated_at = now()
        `, req.CredentialID, req.ProviderCode, req.ModelID, w.Type,
			w.Start, w.End, req.TokenCount,
			successCount, errorCount, req.TenantID)

		if err != nil {
			return fmt.Errorf("upsert quota tracker: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit quota record: %w", err)
	}
	return nil
}

// CorrectFromHeaders 从429响应头校准配额限制.
//
// 行为变更 (2026-08-07): 旧实现仅 UPDATE 当前已存在的 day-1 行, 首次 429
// 没有任何追踪记录时静默丢失校准. 新实现通过 UPSERT 写入 day-1 窗口, 即使
// 之前没有 Record 调用, 也能在收到 429 的同时建立追踪行, 后续 Preflight
// 即可正确识别 is_exhausted.
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
		resetAt,
		retryAfterSec, limitHeader,
		limit,
		req.TenantID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
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
// 用于 Retry-After 的 "30" / "30s" / " 60 " 形式, 避免 HTTP-date 被截断误判.
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

// computeWindows 计算给定时间点的所有窗口起止.
//
// 时区处理: 时间戳统一按 UTC 处理, 各窗口起点都把本地字段按 UTC 解释.
// 滚动窗口 (hour-5 / day-7):
//   - hour-5: 以整点对齐, start = ts.Truncate(Hour) - 4h, end = start + 5h.
//   - day-7:   以 UTC 0 点对齐, start = (UTC date - 6 天) 的 0 点, end = start + 7d.
//
// 这样 rolling 窗口内所有请求共享同一 conflict key, 多次 Record 会累加
// 而不是新建行.
func (qt *QuotaTracker) computeWindows(ts time.Time, types []WindowType) []QuotaWindow {
	ts = ts.UTC()

	var windows []QuotaWindow
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
