package streaming

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
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
