package streaming

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // same alias as the production file under test
)

// ★ 2026-11-17（节点状态漂移审计 D-new）：网关的限流闸有**两道**——请求数闸
// （RPM）与 token 预算闸（TPM）——两者的 error_kind 同为 "rate_limit_exceeded"。
// 修之前，明细码一律被压成 "gw_rpm_exceeded" ⇒ 运维在库里分不出
// 「这把 key 请求太多」与「这把 key 单个请求太大」。
//
// ★ 211 真库实测（11-14~11-16，489 笔 gw_rpm_exceeded）：被拒时刻该 key 的
// 滚动 60 秒请求数中位数 4~14、**最小 2**，而这两把 key 的 rate_limit_rpm=120
// ⇒ 撞的**不是**请求数闸（真因是 token 预算闸）。
//
// ★ 本文件钉的是**行为**，不是实现：
//   · 两道闸 ⇒ 两个不同的明细码；
//   · error_kind / request_status **不动**（会话腿口径零变化）；
//   · 未接线时（旧行为）仍产出 gw_rpm_exceeded（负控对照）。

func TestRateLimitDetailCode_SeparatesTheTwoGates(t *testing.T) {
	cases := []struct {
		name    string
		outcome rateLimitOutcome
		want    string
	}{
		{
			name:    "token budget gate is its own detail code",
			outcome: rateLimitOutcome{Blocked: true, Reason: "tpm_limit"},
			want:    "gw_tpm_exceeded",
		},
		{
			name:    "request count gate keeps the legacy code",
			outcome: rateLimitOutcome{Blocked: true, Reason: "rpm_limit"},
			want:    "gw_rpm_exceeded",
		},
		{
			name:    "queue full is a request rate problem, not a token one",
			outcome: rateLimitOutcome{Blocked: true, Reason: "queue_full"},
			want:    "gw_rpm_exceeded",
		},
		{
			name:    "queue budget exceeded keeps the request rate code",
			outcome: rateLimitOutcome{Blocked: true, Reason: "queue_budget_exceeded"},
			want:    "gw_rpm_exceeded",
		},
		{
			name:    "empty reason (defensive) keeps the legacy code",
			outcome: rateLimitOutcome{Blocked: true},
			want:    "gw_rpm_exceeded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitDetailCode(tc.outcome); got != tc.want {
				t.Fatalf("rateLimitDetailCode(reason=%q) = %q, want %q", tc.outcome.Reason, got, tc.want)
			}
		})
	}
}

// TestFailureDetailOverride_ReachesTheEmittedRow pins the plumbing: the
// override must land in failure_detail_code while error_kind stays the
// rate-limit kind (the session-leg request_status projection depends on it).
func TestFailureDetailOverride_ReachesTheEmittedRow(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ctx := ch.NewRequestLogContext(httptest.NewRequest("POST", "/v1/chat/completions", nil), "req-tpm-detail", time.Now())
	ctx.SetClientModel("glm-5.3-flash")
	ctx.SetFailureDetailOverride(rateLimitDetailCode(rateLimitOutcome{Blocked: true, Reason: "tpm_limit"}))

	entry := ctx.buildEntry("rate_limit_exceeded", "rate limit exceeded", nil, nil, telemetry.RequestStatusRateLimited)
	if entry == nil {
		t.Fatal("buildEntry returned nil")
	}
	if entry.FailureDetailCode == nil || *entry.FailureDetailCode != "gw_tpm_exceeded" {
		t.Fatalf("failure_detail_code = %v, want gw_tpm_exceeded", entry.FailureDetailCode)
	}
	if entry.ErrorKind == nil || *entry.ErrorKind != "rate_limit_exceeded" {
		t.Fatalf("error_kind = %v, want rate_limit_exceeded (must NOT change: the session-leg request_status projection reads it)", entry.ErrorKind)
	}
	if entry.RequestStatus == nil || *entry.RequestStatus != telemetry.RequestStatusRateLimited {
		t.Fatalf("request_status = %v, want rate_limited", entry.RequestStatus)
	}
}

// TestFailureDetailOverride_AbsentKeepsLegacyMapping is the in-file negative
// control: with no override the detail code must be the legacy gw_rpm_exceeded
// — i.e. the fix is a *wiring* change, not a change of the default vocabulary.
func TestFailureDetailOverride_AbsentKeepsLegacyMapping(t *testing.T) {
	ch := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ctx := ch.NewRequestLogContext(httptest.NewRequest("POST", "/v1/chat/completions", nil), "req-rpm-detail", time.Now())
	ctx.SetClientModel("glm-5.3-flash")

	entry := ctx.buildEntry("rate_limit_exceeded", "rate limit exceeded", nil, nil, telemetry.RequestStatusRateLimited)
	if entry == nil {
		t.Fatal("buildEntry returned nil")
	}
	if entry.FailureDetailCode == nil || *entry.FailureDetailCode != "gw_rpm_exceeded" {
		t.Fatalf("failure_detail_code = %v, want gw_rpm_exceeded (legacy default)", entry.FailureDetailCode)
	}
}
