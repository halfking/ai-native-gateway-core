package executors

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestDispatchErrMapping locks the contract that dispatch pipeline errors are
// wrapped into *ExecuteError so the handler's Exhausted branch emits 503 (not
// 502) and goal-retry recognises the kind.
func TestDispatchErrMapping(t *testing.T) {
	cases := []struct {
		name    string
		in      error
		wantExh bool
		wantKnd errorsx.ErrorKind
	}{
		{"no-route", dispatch.ErrNoRoute, true, errorsx.KindConcurrent},
		{"overflow", dispatch.ErrOverflow, true, errorsx.KindConcurrent},
		{"deadline", context.DeadlineExceeded, true, errorsx.KindTimeout},
		{"forward-sentinel", errDispatchCircuitOpen, true, errorsx.KindTransient},
		{"generic", errors.New("boom"), true, errorsx.KindTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ee := dispatchErrToExecuteError(c.in)
			if !ee.Exhausted {
				t.Fatalf("Exhausted must be true for %q", c.name)
			}
			if ee.LastKind != c.wantKnd {
				t.Fatalf("kind = %q, want %q", ee.LastKind, c.wantKnd)
			}
			if !errors.Is(ee.LastErr, c.in) {
				t.Fatalf("LastErr does not wrap input: got %v", ee.LastErr)
			}
		})
	}
}

func TestRecordDispatchOutcomesPreservesTerminalSuccess(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowDoubleWrite = true
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)

	executor := &Executor{URSMv2: mgr}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Request-Id", "dispatch-terminal")
	params := &ExecParams{R: req, TenantID: "tenant"}
	candidate := provider.Candidate{CredentialID: 7, ProviderID: 3, RawModel: "model", StandardizedName: "model"}

	executor.recordDispatchOutcomes(params, []dispatchRequestOutcome{
		{candidate: candidate, errorKind: errorsx.KindUpstreamDown},
		{candidate: candidate, success: true, latencyMs: 12},
	})

	key := "ursm:v2:node:tenant:7:model"
	values, err := rdb.HMGet(context.Background(), key, "failure_count", "success_count", "fail_streak", "last_err").Result()
	if err != nil {
		t.Fatalf("read v2 outcome state: %v", err)
	}
	if values[0] != "1" || values[1] != "1" || values[2] != "0" || values[3] != "" {
		t.Fatalf("terminal success lost to intermediate failure: %#v", values)
	}
}

func TestRecordDispatchOutcomesUsesDetachedContextAndSkipsClientBug(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = ursmv2api.ModeShadow
	cfg.ShadowDoubleWrite = true
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	t.Cleanup(mgr.Close)

	executor := &Executor{URSMv2: mgr}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(requestCtx)
	req.Header.Set("X-Request-Id", "dispatch-detached")
	params := &ExecParams{R: req, TenantID: "tenant"}
	candidate := provider.Candidate{CredentialID: 7, ProviderID: 3, RawModel: "model", StandardizedName: "model"}

	executor.recordDispatchOutcomes(params, []dispatchRequestOutcome{
		{candidate: candidate, errorKind: errorsx.KindCanceled},
		{candidate: candidate, success: true},
	})

	count, err := rdb.HGet(context.Background(), "ursm:v2:node:tenant:7:model", "success_count").Result()
	if err != nil {
		t.Fatalf("read detached success: %v", err)
	}
	if count != "1" {
		t.Fatalf("detached success_count = %q, want 1", count)
	}
}

func TestCopyQueueTimestampsToError(t *testing.T) {
	qr := dispatch.NewQueuedRequest("r1", "t", "m", context.Background(), nil)
	t1 := time.Now().Add(-100 * time.Millisecond)
	t6 := time.Now().Add(-10 * time.Millisecond)
	t9 := time.Now()
	qr.T1_TotalEnqueuedAt = &t1
	qr.T6_CredDequeuedAt = &t6
	qr.T9_ResponseEndAt = &t9

	ee := dispatchErrToExecuteError(dispatch.ErrNoRoute)
	copyQueueTimestampsToError(ee, qr)
	if ee.T0ArrivedAt == nil {
		t.Fatal("T0ArrivedAt missing")
	}
	if ee.T1TotalEnqueuedAt == nil || !ee.T1TotalEnqueuedAt.Equal(t1) {
		t.Fatalf("T1=%v", ee.T1TotalEnqueuedAt)
	}
	if ee.T6CredDequeuedAt == nil || !ee.T6CredDequeuedAt.Equal(t6) {
		t.Fatalf("T6=%v", ee.T6CredDequeuedAt)
	}
	if ee.T9ResponseEndAt == nil || !ee.T9ResponseEndAt.Equal(t9) {
		t.Fatalf("T9=%v", ee.T9ResponseEndAt)
	}
}
