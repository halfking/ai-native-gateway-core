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
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/probekind"
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
// 2026-10-23 审计：判定逻辑收敛到 internal/probekind（唯一真值源）。
// 本函数此前自带一份手写清单（8 个 stage + 5 个 alias），与
// domains/streaming/context_attrs.go 的 isProbeOriginStage、
// middleware/origin_mw.go 的 isValidOriginStage、
// domains/stats/minute_entry.go 的 IsProbeTraffic 各写一份 ⇒ 必然漂移。
//
// 现在的判定 = probekind.IsProbe：stage 路径 ∪ markers 路径
// （task_type / quality_flags / origin_actor）。
//
// ★ 删掉的 5 个 alias 分支（direct / gateway / scheduled /
//
//	probe_gateway / probe_scheduled 作为 origin_stage）：
//	它们从来写不进 DB —— middleware/origin_mw.go 的 isValidOriginStage
//	闸门（origin_mw.go:354-359）在中间件层就把它们挡掉，DB CHECK
//	（migration 341）也不认 ⇒ 是死防御代码。
//	⚠ 但 "probe_gateway" / "probe_scheduled" 作为 **task_type_chosen**
//	  是合法的（bg/active_probe_emitter.go:373 写 "probe_" + origin），
//	  那条路径由下面路径 1 单独处理，未受影响。
func extractProbeInfo(entry *telemetry.RequestLogEntry) probeRequestInfo {
	if entry == nil {
		return probeRequestInfo{}
	}

	// 路径1: task_type='probe_triggered' (legacy ActiveProbeWorker path)
	isProbeFromTaskType := probekind.IsProbeTaskType(derefStr(entry.TaskType))

	// 路径2: origin_stage 是已知探测阶段（与 isProbeOriginStage 同源）。
	origin := "direct" // 默认值，下文按优先级覆盖
	stage := strings.TrimSpace(derefStr(entry.OriginStage))
	isProbeFromOriginStage := probekind.IsProbeStage(stage)
	if isProbeFromOriginStage {
		origin = probeOriginForStage(stage)
	}

	// 路径3: quality_flags 包含 'probe' (向后兼容老数据)
	isProbeFromQualityFlags := probekind.IsProbeQualityFlags(entry.QualityFlags)

	// 路径4: origin_actor 是已知探测 actor（origin_stage 丢失时的兜底）。
	// 2026-10-23 新增 —— 此前 dashboard 只看 stage，stage 一丢就漏标；
	// 而 stats 端早就看 actor 了 ⇒ 两端口径不一致。
	isProbeFromActor := probekind.IsProbeActor(derefStr(entry.OriginActor))

	if !isProbeFromTaskType && !isProbeFromOriginStage && !isProbeFromQualityFlags && !isProbeFromActor {
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
	if attempt == 0 {
		attempt = probekind.ProbeAttemptFromQualityFlags(entry.QualityFlags)
	}

	return probeRequestInfo{
		IsProbe:      true,
		ProbeOrigin:  origin,
		ProbeAttempt: attempt,
	}
}

// probeOriginForStage 把探测 origin_stage 映射成 UI 的三个 pill 语义
// （direct / gateway / scheduled）。
//
// 2026-10-23：这是 dashboard **唯一**保留的 stage→origin 映射表。
// 旧实现在同一个 switch 里混着「合法 stage」与「永不落库的 alias」两套
// 语义（direct/gateway/scheduled/probe_gateway/probe_scheduled），读的人
// 无法分辨哪个真会落库 ⇒ 本函数把两者彻底分开：合法 stage 走这里，
// alias 分支已删除。
func probeOriginForStage(stage string) string {
	switch stage {
	case "self_check", "node_probe", "system_health":
		// 定期后台健康检查 / 后台探针
		return "scheduled"
	case "probe_direct":
		return "direct"
	case "probe_v2", "passive_probe", "model_probe":
		// 这三类都是 origin 维度探测变体，但 UI 仅暴露
		// direct / gateway / scheduled 三个 pill，复用
		// scheduled 作为最接近的语义归类。
		return "scheduled"
	case "manual":
		return "direct" // 手动触发归为 direct 探测
	default:
		return "direct"
	}
}
