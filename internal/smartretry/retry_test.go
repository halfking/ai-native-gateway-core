package smartretry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/streamretry"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRetryAfterUsesPolicyClock(t *testing.T) {
	p := New(Config{Enabled: true, MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 10 * time.Second})
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	p.SetClock(func() time.Time { return now })
	err := &streamretry.HTTPError{
		StatusCode: 429,
		RetryAfter: now.Add(3 * time.Second).Format(http.TimeFormat),
		Err:        errors.New("busy"),
	}
	gotRetry, gotDelay := p.ShouldRetry(context.Background(), "provider", 0, err)
	if !gotRetry {
		t.Fatal("ShouldRetry() = false, want true")
	}
	if gotDelay != 3*time.Second {
		t.Fatalf("delay = %v, want 3s", gotDelay)
	}
}

func TestShouldRetryClassifiesErrors(t *testing.T) {
	p := New(Config{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond})

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"timeout", timeoutErr{}, true},
		{"server", &streamretry.HTTPError{StatusCode: 503, Err: errors.New("down")}, true},
		{"rate-limit", &streamretry.HTTPError{StatusCode: 429, Err: errors.New("busy")}, true},
		{"client", &streamretry.HTTPError{StatusCode: 401, Err: errors.New("bad key")}, false},
		{"cancel", context.Canceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := p.ShouldRetry(context.Background(), "p", 0, tc.err)
			if got != tc.want {
				t.Fatalf("retry=%v want %v", got, tc.want)
			}
		})
	}
}

func TestProviderStatsAdaptRetryLimit(t *testing.T) {
	p := New(Config{Enabled: true, MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second, HighTimeoutRate: .5})
	for i := 0; i < 3; i++ {
		p.RecordFailure("bad", timeoutErr{}, time.Second)
	}
	if got := p.Stats("bad"); got.TimeoutRate < .5 {
		t.Fatalf("timeout rate=%v", got.TimeoutRate)
	}
	if ok, _ := p.ShouldRetry(context.Background(), "bad", 1, timeoutErr{}); ok {
		t.Fatal("high timeout provider should stop after one retry")
	}
}

func TestRecordStatsConcurrent(t *testing.T) {
	p := NewDefault()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				p.RecordSuccess("p", time.Millisecond)
				p.RecordFailure("p", &streamretry.HTTPError{StatusCode: 503, Err: errors.New("x")}, time.Millisecond)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	stats := p.Stats("p")
	if stats.SuccessRate <= 0 || stats.AvgLatency <= 0 {
		t.Fatalf("bad stats: %+v", stats)
	}
}

var _ net.Error = timeoutErr{}
