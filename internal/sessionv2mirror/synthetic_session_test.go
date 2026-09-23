// synthetic_session_test.go — 存储优化方案 v2 §3-D4 合成系统会话单测。
package sessionv2mirror

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

func TestSyntheticSessionID_ProbeKind(t *testing.T) {
	// 本机无会话流量的实况形态：origin_stage=node_probe /
	// origin_actor=node-probe-worker / task_type=probe_triggered。
	at := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	entry := &telemetry.RequestLogEntry{
		RequestID:    "req-probe-1",
		OriginStage:  strPtr("node_probe"),
		OriginActor:  strPtr("node-probe-worker"),
		TaskType:     strPtr("probe_triggered"),
		CredentialID: func() *int { v := 123; return &v }(),
		EventAt:      &at,
	}
	got := SyntheticSessionID(entry)
	// plan §3-D4 形态：sys:{kind}:{cred}:日
	if want := "sys:probe:cred123:20260914"; got != want {
		t.Fatalf("SyntheticSessionID = %q, want %q", got, want)
	}
}

func TestSyntheticSessionID_KindAndScopeFallbacks(t *testing.T) {
	at := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		entry *telemetry.RequestLogEntry
		want  string
	}{
		{
			"provider scope fallback",
			&telemetry.RequestLogEntry{ProviderID: func() *int { v := 9; return &v }(), EventAt: &at},
			"sys:anon:prov9:20260914",
		},
		{
			"gw scope fallback",
			&telemetry.RequestLogEntry{EventAt: &at},
			"sys:anon:gw:20260914",
		},
		{
			"internal loopback kind",
			&telemetry.RequestLogEntry{
				IsAutoRequest: func() *bool { v := true; return &v }(),
				RequestType:   strPtr("title_gen"),
				EventAt:       &at,
			},
			"sys:internal:gw:20260914",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SyntheticSessionID(tc.entry); got != tc.want {
				t.Fatalf("SyntheticSessionID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSyntheticSessionID_NilEntry(t *testing.T) {
	if got := SyntheticSessionID(nil); got != "" {
		t.Fatalf("nil entry must yield empty id, got %q", got)
	}
}

// R60 S2-F4 谓词钉桩：探针合成判据 = 无会话头 ∧ 探针标记；两条件缺一即否。
func TestIsProbeSyntheticSession(t *testing.T) {
	cases := []struct {
		name  string
		entry *telemetry.RequestLogEntry
		want  bool
	}{
		{"nil entry", nil, false},
		{"no-session probe (origin_stage)", &telemetry.RequestLogEntry{OriginStage: strPtr("node_probe")}, true},
		{"no-session probe (origin_actor)", &telemetry.RequestLogEntry{OriginActor: strPtr("node-probe-worker")}, true},
		{"no-session probe (task_type)", &telemetry.RequestLogEntry{TaskType: strPtr("probe_triggered")}, true},
		// 判据边界（与 syntheticKindOf 的 probe 定义严格一致：字段值须含
		// "probe"）：self_check / system_health worker 不含该词 ⇒ 归
		// sys:anon/sys:internal 类，仍走 D4 合成，不在本跳过类内。
		{"no-session self_check (kind≠probe, kept)", &telemetry.RequestLogEntry{OriginStage: strPtr("self_check")}, false},
		{"probe markers but real session header", &telemetry.RequestLogEntry{GwSessionID: strPtr("gw_real"), OriginStage: strPtr("node_probe")}, false},
		{"no-session anon", &telemetry.RequestLogEntry{}, false},
		{"no-session internal loopback", &telemetry.RequestLogEntry{IsAutoRequest: func() *bool { v := true; return &v }(), RequestType: strPtr("title_gen")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProbeSyntheticSession(tc.entry); got != tc.want {
				t.Fatalf("IsProbeSyntheticSession = %v, want %v", got, tc.want)
			}
		})
	}
}

// D4 主路径：无 GwSessionID 的终态非探针条目（anon kind）→ 合成系统会话写
// 链，SessionID 带合成 id、ClientType='system'、dims 不维护。
func TestPersistHook_SynthesizesSystemSessionForNoSessionTraffic(t *testing.T) {
	var got *v2.ProcessedRequest
	dimsCalled := 0
	writer := captureWriterV2{fn: func(req *v2.ProcessedRequest) { got = req }}
	dim := dimWriterFunc(func(context.Context, *telemetry.RequestLogEntry) error {
		dimsCalled++
		return nil
	})
	hook := PersistHook(writer, dim)
	at := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	withShadowFlags(t, func() {
		hook(&telemetry.RequestLogEntry{
			RequestID:    "req-anon-term",
			Success:      true,
			CredentialID: func() *int { v := 7; return &v }(),
			EventAt:      &at,
		})
	})
	if got == nil {
		t.Fatal("no-session terminal entry did not reach V2 writer (D4 synthesis missing)")
	}
	if got.SessionID != "sys:anon:cred7:20260914" {
		t.Fatalf("synthetic SessionID = %q, want sys:anon:cred7:20260914", got.SessionID)
	}
	if got.ClientType != "system" {
		t.Fatalf("synthetic ClientType = %q, want system", got.ClientType)
	}
	if dimsCalled != 0 {
		t.Fatalf("synthetic session must not touch session_dim, dims called %d times", dimsCalled)
	}
}

// R51 教训落地（R60 S2-F4）：无会话头的探针终态条目不进 mirror——写链、
// session_dim 皆不触。探针事实留在 request_logs/v1 面（154 复审 §四.5：
// 该类合成会话曾贡献 ~115/min 的 mirror 失败噪声）。
func TestPersistHook_SkipsProbeSyntheticSession(t *testing.T) {
	called := 0
	dimsCalled := 0
	writer := captureWriterV2{fn: func(*v2.ProcessedRequest) { called++ }}
	dim := dimWriterFunc(func(context.Context, *telemetry.RequestLogEntry) error {
		dimsCalled++
		return nil
	})
	hook := PersistHook(writer, dim)
	withShadowFlags(t, func() {
		// 本机无会话探针流量的实况形态（原 D4 合成测试夹具）。
		hook(&telemetry.RequestLogEntry{
			RequestID:    "req-probe-term",
			Success:      true,
			OriginStage:  strPtr("node_probe"),
			TaskType:     strPtr("probe_triggered"),
			CredentialID: func() *int { v := 7; return &v }(),
		})
		hook(&telemetry.RequestLogEntry{
			RequestID:   "req-probe-fail",
			Success:     false,
			RequestStatus: strPtr(telemetry.RequestStatusFailure),
			OriginActor: strPtr("node-probe-worker"),
		})
	})
	if called != 0 {
		t.Fatalf("probe synthetic entries reached V2 writer %d times, want 0", called)
	}
	if dimsCalled != 0 {
		t.Fatalf("probe synthetic entries touched session_dim %d times, want 0", dimsCalled)
	}
}

// 跳过判据不误伤：探针标记但挂真实会话头的条目（有 GwSessionID）照常入链。
func TestPersistHook_ProbeWithRealSessionStillMirrored(t *testing.T) {
	var got *v2.ProcessedRequest
	writer := captureWriterV2{fn: func(req *v2.ProcessedRequest) { got = req }}
	hook := PersistHook(writer)
	withShadowFlags(t, func() {
		hook(&telemetry.RequestLogEntry{
			RequestID:    "req-probe-with-session",
			Success:      true,
			GwSessionID:  strPtr("gw_real_session"),
			OriginStage:  strPtr("node_probe"),
			TaskType:     strPtr("probe_triggered"),
			CredentialID: func() *int { v := 7; return &v }(),
		})
	})
	if got == nil {
		t.Fatal("session-headed probe entry must NOT be skipped (synthetic-only criterion)")
	}
	if got.SessionID != "gw_real_session" {
		t.Fatalf("SessionID = %q, want gw_real_session", got.SessionID)
	}
}

// 非终态无会话条目（in_progress 占位）仍不入链——合成路径共享终态闸门。
func TestPersistHook_SyntheticSkipsInProgress(t *testing.T) {
	called := 0
	writer := captureWriterV2{fn: func(*v2.ProcessedRequest) { called++ }}
	hook := PersistHook(writer)
	withShadowFlags(t, func() {
		hook(&telemetry.RequestLogEntry{
			RequestID:     "req-probe-in-progress",
			Success:       false,
			RequestStatus: strPtr(telemetry.RequestStatusInProgress),
			OriginStage:   strPtr("node_probe"),
		})
	})
	if called != 0 {
		t.Fatalf("in-progress no-session entry reached V2 writer %d times, want 0", called)
	}
}

// 无会话的内部回环（title/summary 无会话头变体）保留进合成会话（计费完整
// 性，D7 前提），kind=internal。
func TestPersistHook_SyntheticKeepsInternalLoopbacks(t *testing.T) {
	var got *v2.ProcessedRequest
	writer := captureWriterV2{fn: func(req *v2.ProcessedRequest) { got = req }}
	hook := PersistHook(writer)
	withShadowFlags(t, func() {
		hook(&telemetry.RequestLogEntry{
			RequestID:     "req-internal-nosess",
			Success:       true,
			IsAutoRequest: func() *bool { v := true; return &v }(),
			RequestType:   strPtr("summary"),
			OriginActor:   strPtr("auto-summary-generator"),
		})
	})
	if got == nil {
		t.Fatal("internal no-session entry did not reach V2 writer")
	}
	if !strings.HasPrefix(got.SessionID, "sys:internal:") {
		t.Fatalf("internal synthetic SessionID = %q, want sys:internal:*", got.SessionID)
	}
	if got.ClientType != "system" {
		t.Fatalf("internal synthetic ClientType = %q, want system", got.ClientType)
	}
}

// dimWriterFunc 让 D4 测试无需依赖 session_dim.go 的具体实现。
type dimWriterFunc func(ctx context.Context, entry *telemetry.RequestLogEntry) error

func (f dimWriterFunc) UpsertSessionDim(ctx context.Context, entry *telemetry.RequestLogEntry) error {
	return f(ctx, entry)
}
