package freeresource

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
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

// Record 记录一次请求的配额消耗
func (qt *QuotaTracker) Record(ctx context.Context, req RecordRequest) error {
	// 1. 确定窗口起止时间
	windows := qt.computeWindows(req.Timestamp, req.WindowTypes)

	// 2. 批量 UPSERT
	for _, w := range windows {
		successCount := 0
		errorCount := 0
		if req.Success {
			successCount = 1
		} else {
			errorCount = 1
		}

		_, err := qt.db.ExecContext(ctx, `
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

	return nil
}

// CorrectFromHeaders 从429响应头校准配额限制
func (qt *QuotaTracker) CorrectFromHeaders(ctx context.Context, req CorrectionRequest) error {
	var retryAfterSec int
	var resetAt time.Time
	var limit int64

	// 1. 解析 Retry-After
	if ra, ok := req.Headers["Retry-After"]; ok && ra != "" {
		// 尝试解析为秒数
		if sec, err := strconv.Atoi(ra); err == nil {
			retryAfterSec = sec
			resetAt = time.Now().Add(time.Duration(sec) * time.Second)
		} else {
			// 尝试解析为 HTTP-date
			if t, err := http.ParseTime(ra); err == nil {
				resetAt = t
				retryAfterSec = int(time.Until(t).Seconds())
			}
		}
	}

	// 2. 解析 X-RateLimit-Reset（优先级更高）
	if reset, ok := req.Headers["X-RateLimit-Reset"]; ok && reset != "" {
		if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
			resetAt = time.Unix(ts, 0)
		}
	}

	// 3. 解析 X-RateLimit-Limit
	if lim, ok := req.Headers["X-RateLimit-Limit"]; ok && lim != "" {
		limit, _ = strconv.ParseInt(lim, 10, 64)
	}

	// 4. 更新数据库
	_, err := qt.db.ExecContext(ctx, `
        UPDATE free_quota_tracker
        SET is_exhausted = TRUE,
            exhausted_at = now(),
            auto_reset_at = $1,
            last_429_at = now(),
            last_429_reset_after = $2,
            last_429_limit_header = $3,
            corrected_limit = CASE WHEN $4 > 0 THEN $4 ELSE corrected_limit END,
            updated_at = now()
        WHERE credential_id = $5
          AND provider_code = $6
          AND model_id = $7
          AND window_type = $8
          AND window_start <= now()
          AND window_end >= now()
          AND tenant_id = $9
    `, resetAt, retryAfterSec, req.Headers["X-RateLimit-Limit"], limit,
		req.CredentialID, req.ProviderCode, req.ModelID, WindowTypeDay1, req.TenantID)

	return err
}

// Preflight 配额预检 - 返回是否可用
func (qt *QuotaTracker) Preflight(ctx context.Context, req PreflightRequest) (bool, error) {
	var limit, used int64
	var exhausted bool
	var resetAt sql.NullTime

	err := qt.db.QueryRowContext(ctx, `
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
    `, req.DefaultLimit, req.CredentialID, req.ProviderCode, req.ModelID,
		req.WindowType, req.TenantID).Scan(&limit, &used, &exhausted, &resetAt)

	if err == sql.ErrNoRows {
		return true, nil // 无追踪记录，允许使用
	}
	if err != nil {
		return false, err
	}

	// 检查是否已过重置时间
	if exhausted && resetAt.Valid && time.Now().After(resetAt.Time) {
		// 自动解除耗尽状态
		_, _ = qt.db.ExecContext(ctx, `
            UPDATE free_quota_tracker
            SET is_exhausted = FALSE, exhausted_at = NULL
            WHERE credential_id = $1 AND provider_code = $2 AND model_id = $3
              AND window_type = $4 AND tenant_id = $5
        `, req.CredentialID, req.ProviderCode, req.ModelID, req.WindowType, req.TenantID)
		return true, nil
	}

	if exhausted {
		return false, nil
	}

	// 检查剩余配额百分比
	if limit > 0 {
		remaining := float64(limit-used) / float64(limit)
		return remaining >= req.MinRemainingPct, nil
	}

	return true, nil
}

// computeWindows 计算给定时间点的所有窗口起止
func (qt *QuotaTracker) computeWindows(ts time.Time, types []WindowType) []QuotaWindow {
	var windows []QuotaWindow

	for _, wt := range types {
		var start, end time.Time

		switch wt {
		case WindowTypeHour5:
			// 滚动 5 小时窗口
			start = ts.Add(-5 * time.Hour)
			end = ts

		case WindowTypeDay1:
			// UTC 日历日
			start = time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
			end = start.Add(24 * time.Hour)

		case WindowTypeDay7:
			// 滚动 7 日
			start = ts.Add(-7 * 24 * time.Hour)
			end = ts

		case WindowTypeMonth1:
			// UTC 日历月
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
