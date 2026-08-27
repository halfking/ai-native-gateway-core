package streaming

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// v6 G-Ⅱ: X-Gw-Due-At 解析（定时请求）。
func TestParseDispatchDueAt(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	cases := []struct {
		name string
		raw  string
		want time.Time // zero = immediate
	}{
		{"absent", "", time.Time{}},
		{"rfc3339", future.Format(time.RFC3339), future},
		{"rfc3339_nano", future.Format(time.RFC3339Nano), future},
		{"unix_seconds", formatUnixSeconds(future), future},
		{"unix_millis", formatUnixMillis(future), future},
		{"past_rfc3339", time.Now().Add(-time.Hour).Format(time.RFC3339), time.Time{}},
		{"garbage", "next-tuesday-please", time.Time{}},
		{"zero_epoch", "0", time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if tc.raw != "" {
				r.Header.Set("X-Gw-Due-At", tc.raw)
			}
			got := parseDispatchDueAt(r)
			if tc.want.IsZero() {
				if !got.IsZero() {
					t.Fatalf("want immediate, got %v", got)
				}
				return
			}
			if got.IsZero() {
				t.Fatalf("want %v, got immediate", tc.want)
			}
			d := got.Sub(tc.want)
			if d < -time.Second || d > time.Second {
				t.Fatalf("want ~%v, got %v", tc.want, got)
			}
		})
	}
}

func TestScheduledDispatchEnabledDefault(t *testing.T) {
	if !scheduledDispatchEnabled() {
		t.Fatalf("scheduled dispatch should default to enabled")
	}
}

func formatUnixSeconds(ts time.Time) string { return fmt.Sprintf("%d", ts.Unix()) }

func formatUnixMillis(ts time.Time) string { return fmt.Sprintf("%d", ts.UnixMilli()) }

// V6-W1.6 R8 (migration 608): X-Gw-Due-At 解析结果必须可靠地落到 logCtx，
// 供首行与完成态 UPDATE 写 request_class/due_at。
func TestApplyRequestClassToLogCtx(t *testing.T) {
	lc := &RequestLogContext{}

	// 未定时 → immediate，DueAt 清零。
	applyRequestClassToLogCtx(lc, time.Time{})
	if lc.RequestClass != requestClassImmediate || !lc.DueAt.IsZero() {
		t.Fatalf("immediate stamp wrong: %+v", lc)
	}

	// 定时 → scheduled + DueAt。
	due := time.Now().Add(time.Hour)
	applyRequestClassToLogCtx(lc, due)
	if lc.RequestClass != requestClassScheduled || !lc.DueAt.Equal(due) {
		t.Fatalf("scheduled stamp wrong: %+v", lc)
	}

	// nil logCtx 安全；指针导出器行为。
	applyRequestClassToLogCtx(nil, due) // must not panic
	if p := requestClassPtr(lc); p == nil || *p != requestClassScheduled {
		t.Fatalf("requestClassPtr = %v", p)
	}
	if d := requestDueAtPtr(lc); d == nil || !d.Equal(due) {
		t.Fatalf("requestDueAtPtr = %v", d)
	}
	// 空 logCtx / immediate → nil 指针（落库走列默认）。
	if p := requestClassPtr(&RequestLogContext{}); p != nil {
		t.Fatalf("empty ctx class ptr = %v, want nil", p)
	}
	if d := requestDueAtPtr(&RequestLogContext{}); d != nil {
		t.Fatalf("empty ctx due ptr = %v, want nil", d)
	}
}

// 常量与 dispatch 包镜像一致（E14 口径）。
func TestRequestClassMirrorsDispatch(t *testing.T) {
	if requestClassImmediate != dispatch.RequestClassImmediate ||
		requestClassScheduled != dispatch.RequestClassScheduled {
		t.Fatalf("streaming/dispatch class constants drifted")
	}
}
