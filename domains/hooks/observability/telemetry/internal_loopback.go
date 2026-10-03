package telemetry

import "github.com/kaixuan/llm-gateway-go/internal/internaltraffic"

// IsInternalAutoEntry (2026-09-17 R34, lifted from internal/sessionv2mirror)
// reports whether a terminal entry is a gateway-internal auto loopback
// (auto-title / auto-summary / session-summary) rather than a business
// auto-route turn.
//
// Business auto-route requests also set IsAutoRequest, but carry TaskType and
// MUST be treated as real turns — sessionv2mirror keeps them in session_turns
// so the task dimension stays queryable, and the is_final_success claim
// (client.go) lets them win the session's final-success marker.
//
// Two gates must stay consistent by construction, which is why this function
// lives in the telemetry package next to RequestLogEntry instead of remaining
// private to the mirror:
//  1. sessionv2mirror excludes internal loopbacks from session_turns;
//  2. shouldClaimFinalSuccess must NOT let an excluded row claim
//     is_final_success — a claimed row without a mirrored turn permanently
//     inflates the GLOBAL_G2 reconciliation counter (a request_logs row with
//     is_final_success=TRUE but no session_turns row).
//
// ⚠ 2026-10-03（审计 §9.73）：三个 actor 名与两个 request_type 取值原先在本文件
// **硬编码**，`autoroute/shadow_actors.go` 与 `db/request_logs_view_schema.go`
// 各有一份同样的拷贝，三处之间没有依赖边。现在事实统一在
// `internal/internaltraffic`，本函数只保留**把结构体字段摊平成指针参数**这一层。
//
// 判定语义逐字未变（见 internaltraffic.ClassifyInternalLoopback 的注释，
// 特别是「非 nil 才参与该臂」这条不能省）。若要改判定，改那个包，
// 不要在这里另抄一份。
func IsInternalAutoEntry(entry *RequestLogEntry) bool {
	if entry == nil {
		return false
	}
	isAuto := entry.IsAutoRequest != nil && *entry.IsAutoRequest
	return internaltraffic.IsInternalLoopback(isAuto, entry.RequestType, entry.OriginActor, entry.TaskType)
}

// InternalLoopbackArm 是本包暴露的「命中了 4 臂里的哪一条」。
type InternalLoopbackArm = internaltraffic.InternalLoopbackArm

const (
	ArmNone        = internaltraffic.ArmNone
	ArmRequestType = internaltraffic.ArmRequestType
	ArmActor       = internaltraffic.ArmActor
	ArmTaskless    = internaltraffic.ArmTaskless
)

// ClassifyInternalAutoEntry 与 IsInternalAutoEntry 同判，但返回**命中的臂**。
//
// 为什么需要它：审计 §9.73.3 的 252 生产实测发现「actor 臂」与「taskless 兜底臂」
// 命中**完全相同的 3,200 行**（对称差 0/0/3200）⇒ 兜底臂当前零独立贡献。
// 而只提供 bool 的 API 让这个事实无处可记；有了臂，测试就能把它钉住，
// 将来某条臂真的开始独立命中时也会立刻可见（而不是悄悄改变覆盖面）。
func ClassifyInternalAutoEntry(entry *RequestLogEntry) InternalLoopbackArm {
	if entry == nil {
		return ArmNone
	}
	isAuto := entry.IsAutoRequest != nil && *entry.IsAutoRequest
	return internaltraffic.ClassifyInternalLoopback(isAuto, entry.RequestType, entry.OriginActor, entry.TaskType)
}
