// admin/probe_request_info.go — derive probe metadata from a
// telemetry.RequestLogEntry for the live-stream swim lane.
//
// 2026-07-13: bg.ActiveProbeWorker writes probe rows with:
//
//	task_type       = "probe_triggered"
//	task_type_chosen = "probe_direct"
//	quality_flags    = ["probe","direct",...]
//	auto_decision    = JSON with "probe_attempt": N
//
// 2026-07-17 (commit eb176eb6 on remote): added OriginStage=="node_probe"
// detection as a second probe path (covers NodeProbeWorker probeGateway
// writes, which set origin_stage on every probe row but do not touch
// task_type).
//
// 2026-07-17 (audit, this commit): the eb176eb6 fix only handled
// OriginStage=="node_probe" — but the database CHECK constraint and
// isProbeOriginStage in domains/streaming/context_attrs.go accept
// 8 distinct probe origin_stage values. If any of them leaked into the
// live-stream dashboard as a non-probe row, operators would mistake
// the originating background probe (self_check / node_probe /
// system_health, etc.) for a normal user request and read success-
// rates / QPS off by including them in the business denominator.
//
// LiveRequestFromTelemetry calls extractProbeInfo to populate
// IsProbe / ProbeOrigin / ProbeAttempt on the live-stream row.
package admin

import (
	"encoding/json"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// probeRequestInfo is the small subset of probe metadata the live-stream
// dashboard needs to render the 🛡️ Probe pill + the "仅探测" filter.
type probeRequestInfo struct {
	IsProbe      bool
	ProbeOrigin  string
	ProbeAttempt int
}

// extractProbeInfo reads a telemetry.RequestLogEntry and returns the
// probe metadata to surface on the live-stream row. Returns the zero
// value when the entry is not a probe row, so callers can blindly
// assign the result without nil checks.
//
// 2026-07-17 (audit): 当前实现覆盖三条探测请求识别路径：
//
//	1. task_type='probe_triggered'        (legacy ActiveProbeWorker)
//	2. origin_stage 是已知探测阶段           (NodeProbeWorker / SelfCheck /
//	                                         SystemHealth / ProbeV2 等)
//	3. quality_flags 包含 'probe'          (backfill / 老数据兜底)
//
// single source of truth: origin_stage 列表必须与
// domains/streaming/context_attrs.go 的 isProbeOriginStage 保持同步，
// 否则 dashboard 与 context_attrs 侧表 is_probe 列会漂移（参见
// PR eb176eb6 评审）。
func extractProbeInfo(entry *telemetry.RequestLogEntry) probeRequestInfo {
	if entry == nil {
		return probeRequestInfo{}
	}

	// 路径1: task_type='probe_triggered' (legacy ActiveProbeWorker path)
	isProbeFromTaskType := entry.TaskType != nil && *entry.TaskType == "probe_triggered"

	// 路径2: origin_stage 是已知探测阶段（与 isProbeOriginStage 同源）。
	//
	// 注意：当前 case 列表必须与 domains/streaming/context_attrs.go 的
	// isProbeOriginStage 函数完全一致。新增合法 origin_stage 值时，
	// 两处必须同时更新。这是单一真值源：context_attrs.is_probe 列的
	// 写入路径与 dashboard 的实时渲染都依赖同一份清单。
	isProbeFromOriginStage := false
	origin := "direct" // 默认值，下文按优先级覆盖
	if entry.OriginStage != nil {
		switch *entry.OriginStage {
		case "self_check", "node_probe", "system_health":
			isProbeFromOriginStage = true
			origin = "scheduled" // 定期后台健康检查
		case "probe_direct", "direct":
			isProbeFromOriginStage = true
			origin = "direct"
		case "probe_gateway", "gateway":
			isProbeFromOriginStage = true
			origin = "gateway"
		case "probe_scheduled", "scheduled":
			isProbeFromOriginStage = true
			origin = "scheduled"
		case "probe_v2", "passive_probe", "model_probe":
			isProbeFromOriginStage = true
			// 这三类都是 origin 维度探测变体，但 UI 仅暴露
			// direct / gateway / scheduled 三个 pill，复用
			// scheduled 作为最接近的语义归类。
			origin = "scheduled"
		case "manual":
			isProbeFromOriginStage = true
			origin = "direct" // 手动触发归为 direct 探测
		}
	}

	// 路径3: quality_flags 包含 'probe' (向后兼容老数据)
	isProbeFromQualityFlags := false
	if entry.QualityFlags != nil {
		for _, f := range entry.QualityFlags {
			if f == "probe" {
				isProbeFromQualityFlags = true
				break
			}
		}
	}

	if !isProbeFromTaskType && !isProbeFromOriginStage && !isProbeFromQualityFlags {
		return probeRequestInfo{}
	}

	// 来源(ProbeOrigin) 优先级：
	//   task_type_chosen  >  OriginStage 派生  >  默认 "direct"
	//
	// 仅在来自 path 1 的探测中允许 TaskTypeChosen 覆盖；从 OriginStage
	// 派生出来的 origin 已是 final，避免被 task_type_chosen 错误覆盖。
	if isProbeFromTaskType && entry.TaskTypeChosen != nil {
		switch *entry.TaskTypeChosen {
		case "probe_direct":
			origin = "direct"
		case "probe_gateway":
			origin = "gateway"
		case "probe_scheduled":
			origin = "scheduled"
		}
	}

	attempt := 0
	// Attempt 1: parse from AutoDecision JSON (preferred; typed)
	if entry.AutoDecision != nil && *entry.AutoDecision != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(*entry.AutoDecision), &meta); err == nil {
			if v, ok := meta["probe_attempt"].(float64); ok {
				attempt = int(v)
			} else if v, ok := meta["probe_attempt"].(int); ok {
				attempt = v
			}
		}
	}
	// Attempt 2: parse from quality_flags "attempt_N" (fallback for
	// older rows written before the auto_decision JSONB was added).
	if attempt == 0 && entry.QualityFlags != nil {
		for _, f := range entry.QualityFlags {
			if len(f) > 8 && f[:8] == "attempt_" {
				if n, err := strconv.Atoi(f[8:]); err == nil && n > attempt {
					attempt = n
				}
			}
		}
	}

	return probeRequestInfo{
		IsProbe:      true,
		ProbeOrigin:  origin,
		ProbeAttempt: attempt,
	}
}
