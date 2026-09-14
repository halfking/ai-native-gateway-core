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
		RequestID:   "req-probe-1",
		OriginStage: strPtr("node_probe"),
		OriginActor: strPtr("node-probe-worker"),
		TaskType:    strPtr("probe_triggered"),
		CredentialID: func() *int { v := 123; return &v }(),
		EventAt:     &at,
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

// D4 主路径：无 GwSessionID 的终态探针条目 → 合成系统会话写链，
// SessionID 带合成 id、ClientType='system'、dims 不维护。
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
			RequestID:   "req-probe-term",
			Success:     true,
			OriginStage: strPtr("node_probe"),
			TaskType:    strPtr("probe_triggered"),
			CredentialID: func() *int { v := 7; return &v }(),
			EventAt:     &at,
		})
	})
	if got == nil {
		t.Fatal("no-session terminal entry did not reach V2 writer (D4 synthesis missing)")
	}
	if got.SessionID != "sys:probe:cred7:20260914" {
		t.Fatalf("synthetic SessionID = %q, want sys:probe:cred7:20260914", got.SessionID)
	}
	if got.ClientType != "system" {
		t.Fatalf("synthetic ClientType = %q, want system", got.ClientType)
	}
	if got.TaskType != "probe_triggered" {
		t.Fatalf("synthetic TaskType = %q, want probe_triggered (plan D4: task_type 沿用现值)", got.TaskType)
	}
	if dimsCalled != 0 {
		t.Fatalf("synthetic session must not touch session_dim, dims called %d times", dimsCalled)
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
