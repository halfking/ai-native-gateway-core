//go:build integration

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// 缺陷B 的 lite 面孪生判据。cmd/gateway/lite_telemetry_sink.go 的
// liteEntryStatusCode 与 internal/sessionv2mirror.statusCode 是同一条规则的
// 两份拷贝（同一批 entry、同一张 session_turns 表）。早期只改 mirror 侧时
// lite 面会把 401 继续落成 500 —— 所以两边都必须有判据，且下面的源码钉桩
// 保证它们不会再次分叉。
func TestLiteEntryStatusCodePrefersClientReceivedStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		entry  *telemetry.RequestLogEntry
		want   int
		reason string
	}{
		{
			name: "gateway_authored_rejection_keeps_real_code",
			entry: &telemetry.RequestLogEntry{
				Success: false, RequestStatus: strPtrLite(telemetry.RequestStatusFailure),
				ErrorKind: strPtrLite("invalid_key"), ClientStatusCode: intPtrLite(401),
			},
			want: 401, reason: "client received 401; upstream never existed",
		},
		{
			name: "no_client_status_falls_back_to_500",
			entry: &telemetry.RequestLogEntry{
				Success: false, RequestStatus: strPtrLite(telemetry.RequestStatusFailure),
				ErrorKind: strPtrLite("invalid_key"),
			},
			want: 500, reason: "500 is the 'no received info' fallback, not a claim",
		},
		{
			name: "relayed_upstream_status_still_wins_when_client_absent",
			entry: &telemetry.RequestLogEntry{
				Success: false, RequestStatus: strPtrLite(telemetry.RequestStatusFailure),
				ErrorKind: strPtrLite("rate_limit"), UpstreamStatusCode: intPtrLite(429),
			},
			want: 429, reason: "proxy-shaped failure relayed the upstream code verbatim",
		},
		{
			name:  "client_status_beats_relayed_upstream",
			entry: &telemetry.RequestLogEntry{Success: false, ErrorKind: strPtrLite("x"), UpstreamStatusCode: intPtrLite(502), ClientStatusCode: intPtrLite(429)},
			want:  429, reason: "what the client received outranks what the provider returned",
		},
		{
			name: "in_progress_stays_undecided",
			entry: &telemetry.RequestLogEntry{
				Success: false, RequestStatus: strPtrLite(telemetry.RequestStatusInProgress),
			},
			want: 0, reason: "a placeholder row is not a terminal outcome",
		},
		{
			name:  "success_defaults_to_200",
			entry: &telemetry.RequestLogEntry{Success: true, RequestStatus: strPtrLite(telemetry.RequestStatusSuccess)},
			want:  200, reason: "unchanged pre-existing fallback",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liteEntryStatusCode(tc.entry); got != tc.want {
				t.Fatalf("liteEntryStatusCode = %d, want %d (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

// 两份拷贝不得分叉：lite 面与镜像面必须都以 ClientStatusCode 为第一优先。
func TestLiteAndMirrorStatusCodeRulesStayInSync(t *testing.T) {
	lite, err := os.ReadFile("lite_telemetry_sink.go")
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := os.ReadFile("../../internal/sessionv2mirror/hook.go")
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string][]byte{"lite": lite, "mirror": mirror} {
		text := string(src)
		idx := strings.Index(text, "entry.ClientStatusCode != nil")
		if idx < 0 {
			t.Fatalf("%s no longer checks entry.ClientStatusCode — the received status lost its top priority", name)
		}
		upstreamIdx := strings.Index(text, "entry.UpstreamStatusCode != nil")
		if upstreamIdx < 0 || upstreamIdx < idx {
			t.Fatalf("%s consults UpstreamStatusCode at offset %d, not after ClientStatusCode at %d", name, upstreamIdx, idx)
		}
	}
}

// 缺陷B 遗留 3：turn 特征层那行的 status_code 与 turn 行同源同义，不能一个用
// 客户端实收状态、另一个只用上游状态 —— 同一个 entry 写出的两行会互相矛盾。
func TestLiteTurnDetailsUsesTheSameStatusRuleAsTheTurnRow(t *testing.T) {
	src, err := os.ReadFile("lite_telemetry_sink.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	// 特征层的 StatusCode 必须走同一个判定函数。
	if strings.Contains(text, "StatusCode:   intPtrValue(entry.UpstreamStatusCode)") {
		t.Fatal("the turn-details row still records UpstreamStatusCode; it must use " +
			"liteEntryStatusCode like the turn row written from the same entry")
	}
	if !strings.Contains(text, "StatusCode:   liteEntryStatusCode(entry)") {
		t.Fatal("the turn-details row no longer derives its status through liteEntryStatusCode")
	}
	// 两个判定点必须都存在，避免只改一处。
	if strings.Count(text, "liteEntryStatusCode(entry)") < 2 {
		t.Fatalf("expected both the turn row and the details row to call liteEntryStatusCode, found %d",
			strings.Count(text, "liteEntryStatusCode(entry)"))
	}
	// 行为断言：特征层拿到的就是这个函数的返回值。
	if got := liteEntryStatusCode(&telemetry.RequestLogEntry{
		Success: false, ErrorKind: strPtrLite("invalid_key"), ClientStatusCode: intPtrLite(401),
	}); got != 401 {
		t.Fatalf("details-row status = %d, want 401", got)
	}
}

func strPtrLite(s string) *string { return &s }

func intPtrLite(v int) *int { return &v }
