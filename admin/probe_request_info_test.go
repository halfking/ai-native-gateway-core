// admin/probe_request_info_test.go — unit tests for extractProbeInfo.
//
// These tests lock in the contract between bg.ActiveProbeEmitter
// (which writes probe rows) and admin.extractProbeInfo (which surfaces
// them as LiveRequest.IsProbe / ProbeOrigin / ProbeAttempt).
package admin

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func ptrStr(s string) *string { return &s }

func TestExtractProbeInfo_NilEntry(t *testing.T) {
	got := extractProbeInfo(nil)
	if got.IsProbe {
		t.Errorf("nil entry should not be a probe, got %+v", got)
	}
}

func TestExtractProbeInfo_NotAProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType: ptrStr("normal_request"),
	}
	got := extractProbeInfo(entry)
	if got.IsProbe {
		t.Errorf("normal_request should not be a probe, got %+v", got)
	}
}

func TestExtractProbeInfo_NilTaskType(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	got := extractProbeInfo(entry)
	if got.IsProbe {
		t.Errorf("nil TaskType should not be a probe, got %+v", got)
	}
}

func TestExtractProbeInfo_DirectProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("probe_triggered should be detected as probe")
	}
	if got.ProbeOrigin != "direct" {
		t.Errorf("ProbeOrigin = %q, want direct", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_GatewayProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_gateway"),
	}
	got := extractProbeInfo(entry)
	if got.ProbeOrigin != "gateway" {
		t.Errorf("ProbeOrigin = %q, want gateway", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_ScheduledProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_scheduled"),
	}
	got := extractProbeInfo(entry)
	if got.ProbeOrigin != "scheduled" {
		t.Errorf("ProbeOrigin = %q, want scheduled", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_AttemptFromAutoDecision(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		AutoDecision:   ptrStr(`{"probe_attempt":3,"probe_status":"failed"}`),
	}
	got := extractProbeInfo(entry)
	if got.ProbeAttempt != 3 {
		t.Errorf("ProbeAttempt = %d, want 3 (from auto_decision JSON)", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_AttemptFromQualityFlags(t *testing.T) {
	// Older rows (written before auto_decision was added) encode attempt
	// in quality_flags. extractProbeInfo must fall back to that.
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		QualityFlags:   []string{"probe", "direct", "attempt_2"},
	}
	got := extractProbeInfo(entry)
	if got.ProbeAttempt != 2 {
		t.Errorf("ProbeAttempt = %d, want 2 (from quality_flags fallback)", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_AttemptPreference(t *testing.T) {
	// When both auto_decision and quality_flags carry an attempt, the
	// auto_decision value wins (it's the typed source of truth).
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		AutoDecision:   ptrStr(`{"probe_attempt":4}`),
		QualityFlags:   []string{"probe", "direct", "attempt_1"},
	}
	got := extractProbeInfo(entry)
	if got.ProbeAttempt != 4 {
		t.Errorf("ProbeAttempt = %d, want 4 (auto_decision should win over quality_flags)", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_BadAutoDecisionJSON(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		AutoDecision:   ptrStr(`{not valid json`),
	}
	got := extractProbeInfo(entry)
	// Should fall back to attempt=0 without panicking.
	if got.ProbeAttempt != 0 {
		t.Errorf("ProbeAttempt = %d, want 0 after JSON parse failure", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_DefaultOriginWhenTaskTypeChosenNil(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType: ptrStr("probe_triggered"),
		// TaskTypeChosen intentionally nil
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("probe_triggered should still be detected as probe")
	}
	if got.ProbeOrigin != "direct" {
		t.Errorf("ProbeOrigin = %q, want direct (default)", got.ProbeOrigin)
	}
}

// 2026-07-17 (eb176eb6): NodeProbeWorker probeGateway 写入 origin_stage="node_probe"
// 而不动 task_type。extractProbeInfo 必须独立识别这条路径。
// audit: 该 case 与 self_check/system_health 同属"定期后台检查"，归类为 scheduled
// (原 eb176eb6 错误归类为 gateway)。
func TestExtractProbeInfo_NodeProbeOriginStage(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("node_probe"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("origin_stage='node_probe' must be detected as probe (NodeProbeWorker)")
	}
	if got.ProbeOrigin != "scheduled" {
		t.Errorf("ProbeOrigin = %q, want scheduled (audit: 与 self_check/system_health 同组)", got.ProbeOrigin)
	}
}

// 2026-07-17 (audit): 完整覆盖 isProbeOriginStage 列出的所有合法 origin_stage 取值。
// 这些都是后台 worker 写入的探测 row，dashboard 必须打 🛡️ Probe pill，
// 否则运维会把后台探测与真实业务请求混淆并误判 QPS / 成功率。
func TestExtractProbeInfo_OriginStageSelfCheck(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("self_check"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("origin_stage='self_check' must be detected as probe")
	}
	if got.ProbeOrigin != "scheduled" {
		t.Errorf("ProbeOrigin = %q, want scheduled", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_OriginStageSystemHealth(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("system_health"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("origin_stage='system_health' must be detected as probe")
	}
	if got.ProbeOrigin != "scheduled" {
		t.Errorf("ProbeOrigin = %q, want scheduled", got.ProbeOrigin)
	}
}

// 探测变体 (model_probe / passive_probe / probe_v2) 都是被动/调度
// 探测，归类为 scheduled 是因为 UI 仅暴露三个 pill。
func TestExtractProbeInfo_OriginStageModelProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("model_probe"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("origin_stage='model_probe' must be detected as probe")
	}
	if got.ProbeOrigin != "scheduled" {
		t.Errorf("ProbeOrigin = %q, want scheduled", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_OriginStagePassiveProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("passive_probe"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("origin_stage='passive_probe' must be detected as probe")
	}
}

func TestExtractProbeInfo_OriginStageManual(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("manual"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("origin_stage='manual' must be detected as probe")
	}
	if got.ProbeOrigin != "direct" {
		t.Errorf("ProbeOrigin = %q, want direct (manual 手动触发)", got.ProbeOrigin)
	}
}

// 关键反例：business 是合法 origin_stage 但不是探测请求。
func TestExtractProbeInfo_OriginStageBusinessNotProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		OriginStage: ptrStr("business"),
	}
	got := extractProbeInfo(entry)
	if got.IsProbe {
		t.Error("origin_stage='business' must NOT be detected as probe (反例)")
	}
}

// 兜底路径：仅有 quality_flags 包含 "probe" 时也应识别为探测。
func TestExtractProbeInfo_QualityFlagsOnly(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		QualityFlags: []string{"probe", "direct", "attempt_1"},
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("quality_flags containing 'probe' should be detected as probe (backfill 兜底)")
	}
	if got.ProbeAttempt != 1 {
		t.Errorf("ProbeAttempt = %d, want 1 (from quality_flags)", got.ProbeAttempt)
	}
}

// TaskType + OriginStage 同时存在时 TaskTypeChosen 优先覆盖 origin。
// 这是 ActiveProbeWorker 写入路径的预期行为，因为 task_type
// 才是真正的"探测意图"载体。
func TestExtractProbeInfo_TaskTypeChosenWinsOverOriginStage(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_gateway"),
		OriginStage:    ptrStr("self_check"), // 默认会推为 scheduled
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("must be detected as probe")
	}
	if got.ProbeOrigin != "gateway" {
		t.Errorf("ProbeOrigin = %q, want gateway (TaskTypeChosen should override)", got.ProbeOrigin)
	}
}

// task_type=probe_triggered 时即使 OriginStage=business 也仍是探测。
// 这保护了 ActiveProbeWorker 误把 OriginStage 漏给业务中间件的场景。
func TestExtractProbeInfo_TaskTypeOnlyBusinessOrigin(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:    ptrStr("probe_triggered"),
		OriginStage: ptrStr("business"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("task_type='probe_triggered' must still be detected as probe regardless of OriginStage")
	}
}
