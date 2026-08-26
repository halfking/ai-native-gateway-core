package streaming

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// 定时请求（v6 G-Ⅱ, docs/架构优化v6/08-dispatch-executor-loop.md §R2）：
// 客户端通过 X-Gw-Due-At 请求头声明期望执行时间。到达时间前请求停留在
// 调度管线的到期堆（"丢回待处理队列"），到期后由 promoter 重新进入 Tier-0
// 总队列执行。解析出的 DueAt 经 ExecParams.DispatchDueAt 传入 dispatch。

// scheduledDispatchEnabled guards X-Gw-Due-At parsing. Default ON; set
// LLM_GATEWAY_ENABLE_SCHEDULED_DISPATCH=0 to disable (header then ignored,
// every request immediate).
func scheduledDispatchEnabled() bool {
	v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ENABLE_SCHEDULED_DISPATCH"))
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return true
	}
	return b
}

// parseDispatchDueAt reads the X-Gw-Due-At header. Accepted formats:
//   - RFC3339 / RFC3339Nano ("2026-08-27T12:00:00+08:00")
//   - unix seconds  ("1798766400")
//   - unix millis   ("1798766400123")
//
// Zero time (immediate) is returned when the header is absent, disabled,
// unparsable, or already in the past. Scheduling too far ahead is NOT
// rejected here — dispatch.Submit enforces the horizon so the policy lives
// in one place.
func parseDispatchDueAt(r *http.Request) time.Time {
	if r == nil || !scheduledDispatchEnabled() {
		return time.Time{}
	}
	raw := strings.TrimSpace(r.Header.Get("X-Gw-Due-At"))
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return normalizeDueAt(t)
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
		switch {
		case n > 1e12: // millis
			return normalizeDueAt(time.UnixMilli(n))
		default: // seconds
			return normalizeDueAt(time.Unix(n, 0))
		}
	}
	return time.Time{}
}

// normalizeDueAt maps a past timestamp to "immediate" — a late scheduled
// request executes now rather than being dropped.
func normalizeDueAt(t time.Time) time.Time {
	if t.IsZero() || t.Before(time.Now()) {
		return time.Time{}
	}
	return t
}
