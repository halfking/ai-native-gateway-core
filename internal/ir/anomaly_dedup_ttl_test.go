// anomaly_dedup_ttl_test.go — S2-F3/R60 回归钉桩：
//
//  1. process-wide dedup 带 TTL，过期后同 key 可再报（修复前
//     map[string]struct{} 永不过期，{"raw":...} format-anomaly 每进程
//     生命周期只发一次，观测面失效）；
//  2. 惰性清理把 map 大小压回阈值内；
//  3. {"raw":...} 上报点携带真实 request_id（irRequestID plumb）。
package ir

import (
	"testing"
	"time"
)

// withDedupTTL 临时替换 dedup TTL/阈值并在测试结束后还原。
func withDedupTTL(t *testing.T, ttl time.Duration, threshold int) {
	t.Helper()
	prevTTL, prevThreshold := anomalyDedupTTL, anomalyDedupSweepThreshold
	anomalyDedupTTL, anomalyDedupSweepThreshold = ttl, threshold
	t.Cleanup(func() {
		anomalyDedupTTL, anomalyDedupSweepThreshold = prevTTL, prevThreshold
	})
}

func TestReportAnomaly_DedupTTLExpires(t *testing.T) {
	withDedupTTL(t, 20*time.Millisecond, 4096)
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)

	ev := AnomalyEvent{
		RequestID:         "req-ttl-1",
		AnomalyType:       AnomalyProtocolLoss,
		Severity:          SeverityMedium,
		FieldPath:         "messages[*].tool_use.input",
		SourceProtocol:    ProtocolOpenAIChat,
		TargetProtocol:    ProtocolAnthropicMessages,
		Reason:            "loss",
		RawValueTruncated: true,
	}

	count := 0
	capture := func() {
		SetAnomalyReporter(func(AnomalyEvent) { count++ })
	}

	capture()
	if !ReportAnomaly(ev) {
		t.Fatal("first report must be accepted")
	}
	if !ReportAnomaly(ev) {
		t.Fatal("dedup suppressed event still returns true (recorded)")
	}
	if count != 1 {
		t.Fatalf("reports = %d, want 1 (TTL 窗口内同 key 抑制)", count)
	}

	// TTL 过期后同 key 可再报（本次上报同时刷新时间戳）。
	time.Sleep(30 * time.Millisecond)
	if !ReportAnomaly(ev) {
		t.Fatal("post-TTL report must be accepted")
	}
	if count != 2 {
		t.Fatalf("reports after TTL expiry = %d, want 2 (修复前永不过期 = 1)", count)
	}

	// 窗口内重复继续抑制（时间戳已刷新）。
	if !ReportAnomaly(ev) {
		t.Fatal("in-TTL report must be accepted (and suppressed)")
	}
	time.Sleep(5 * time.Millisecond)
	if !ReportAnomaly(ev) {
		t.Fatal("in-TTL report must be accepted (and suppressed)")
	}
	if count != 2 {
		t.Fatalf("reports after in-TTL repeats = %d, want 2 (时间戳被刷新，窗口内抑制)", count)
	}
}

func TestReportAnomaly_DedupSweepBoundsMap(t *testing.T) {
	withDedupTTL(t, time.Nanosecond, 4)
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)

	// 超阈值插入：阈值 4 + 全部 key 立即过期 ⇒ 下一次插入触发整表清扫，
	// map 被压回（理论上清空 + 当前 key = 1）。
	for i := 0; i < 64; i++ {
		ReportAnomaly(AnomalyEvent{
			RequestID:   string(rune('a' + i%26)) + time.Now().UTC().Format("150405.000000000") + "-" + time.Now().Format(time.RFC3339Nano) + string(rune(i)),
			AnomalyType: AnomalyUnknownField,
			FieldPath:   "sweep",
		})
	}
	time.Sleep(2 * time.Millisecond) // 让已插入的 key 全部过期
	ReportAnomaly(AnomalyEvent{RequestID: "sweep-trigger", AnomalyType: AnomalyUnknownField, FieldPath: "sweep"})
	if len(reporterDed) > anomalyDedupSweepThreshold {
		t.Fatalf("dedup map size = %d, want <= %d after sweep", len(reporterDed), anomalyDedupSweepThreshold)
	}
}

// S2-F3/R60：serialize_anthropic 的 format-anomaly 上报带真实 request_id；
// dedup key 含 RequestID ⇒ 同请求抑制、异请求各报一次。
func TestSerializeAnthropic_FormatAnomalyCarriesRequestID(t *testing.T) {
	withDedupTTL(t, time.Hour, 4096)
	prev := SetAnomalyReporter(func(AnomalyEvent) {})
	defer SetAnomalyReporter(prev)

	var events []AnomalyEvent
	SetAnomalyReporter(func(ev AnomalyEvent) { events = append(events, ev) })

	// 同一 request id 序列化两次（同 key）：窗口内第二次被 dedup。
	reqWithID := anthropicToolUseInputTestReq(`oops-not-json`)
	reqWithID.Metadata = &Metadata{RequestID: "req-r60-1"}
	if _, err := SerializeAnthropic(reqWithID); err != nil {
		t.Fatalf("SerializeAnthropic: %v", err)
	}
	if _, err := SerializeAnthropic(reqWithID); err != nil {
		t.Fatalf("SerializeAnthropic (repeat): %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (同 request_id 窗口内去重): %v", len(events), events)
	}
	if events[0].RequestID != "req-r60-1" {
		t.Fatalf("request_id = %q, want req-r60-1 (修复前恒为 unknown)", events[0].RequestID)
	}
	if events[0].Metadata["anomaly"] != "format" {
		t.Errorf("metadata.anomaly = %v, want format", events[0].Metadata["anomaly"])
	}

	// 不同 request id：dedup key 不同，各报一次。
	reqOther := anthropicToolUseInputTestReq(`oops-not-json`)
	reqOther.Metadata = &Metadata{RequestID: "req-r60-2"}
	if _, err := SerializeAnthropic(reqOther); err != nil {
		t.Fatalf("SerializeAnthropic (other request): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (异 request_id 不共享去重): %v", len(events), events)
	}
	if events[1].RequestID != "req-r60-2" {
		t.Errorf("second event request_id = %q, want req-r60-2", events[1].RequestID)
	}

	// 无 metadata（无请求上下文）：回落 "unknown"，不崩。
	if _, err := SerializeAnthropic(anthropicToolUseInputTestReq(`oops-not-json`)); err != nil {
		t.Fatalf("SerializeAnthropic (no metadata): %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %v", len(events), events)
	}
	if events[2].RequestID != "unknown" {
		t.Errorf("fallback request_id = %q, want unknown", events[2].RequestID)
	}
}

// irRequestID 的 nil 安全（serializer 在 panic 路径可能传 nil req）。
func TestIRRequestID_NilSafety(t *testing.T) {
	if got := irRequestID(nil); got != "unknown" {
		t.Errorf("irRequestID(nil) = %q, want unknown", got)
	}
	if got := irRequestID(&InternalRequest{}); got != "unknown" {
		t.Errorf("irRequestID(no metadata) = %q, want unknown", got)
	}
	if got := irRequestID(&InternalRequest{Metadata: &Metadata{}}); got != "unknown" {
		t.Errorf("irRequestID(empty metadata) = %q, want unknown", got)
	}
	if got := irRequestID(&InternalRequest{Metadata: &Metadata{RequestID: "req-xyz"}}); got != "req-xyz" {
		t.Errorf("irRequestID(stamped) = %q, want req-xyz", got)
	}
}
