// Package probelane 是「探测流量物理隔离车道」的运行时开关与判定入口。
//
// 方案见 docs/03-design/04-data-design/probe-traffic-physical-isolation-plan-2026-11-02.md。
//
// # 行为
//
// PhysicalEnabled() 默认 false（env LLM_GATEWAY_PROBE_LANE_PHYSICAL 未设或非 1/true/on）。
// 为真时，写路径必须把探测流量送进探测族表/缓存，且不得写入业务
// session_turns / usage_ledger_hot / api_keys / live:main。
//
// 判定「是不是探测」仍只认 internal/probekind —— 本包不维护第二份名单。
package probelane

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// EnvPhysical 是探测物理隔离车道的环境变量名。
const EnvPhysical = "LLM_GATEWAY_PROBE_LANE_PHYSICAL"

// 测试可覆盖：非 -1 时覆盖 env 读数（1=开，0=关，-1=跟 env）。
var physicalOverride atomic.Int32

func init() {
	physicalOverride.Store(-1)
}

// SetPhysicalEnabledForTest 仅供测试覆盖开关；调用方必须 defer Reset。
func SetPhysicalEnabledForTest(on bool) {
	if on {
		physicalOverride.Store(1)
	} else {
		physicalOverride.Store(0)
	}
}

// ResetPhysicalOverrideForTest 恢复跟 env。
func ResetPhysicalOverrideForTest() {
	physicalOverride.Store(-1)
}

// PhysicalEnabled 报告探测物理隔离车道是否开启。
func PhysicalEnabled() bool {
	switch physicalOverride.Load() {
	case 1:
		return true
	case 0:
		return false
	}
	v := strings.TrimSpace(strings.ToLower(os.Getenv(EnvPhysical)))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

// IsProbeEntry 用 probekind 的**完整**判定（含 origin_stage）。
//
// ⚠ 读侧（dashboard pill / 侧表 is_probe）用完整判定；**写侧不**用它 ——
// 见 ShouldIsolate 的注释。
func IsProbeEntry(originStage, taskType string, qualityFlags []string, originActor string) bool {
	return probekind.IsProbe(originStage, taskType, qualityFlags, originActor)
}

// ShouldIsolate 在物理车道开启且条目为探测时为真 —— 写路径据此分叉。
//
// ★ 2026-11-02 口径对齐（审计发现）：本函数原先用 probekind.IsProbe
// （**完整**判定，含 origin_stage），而 stats 端两臂（Go 臂
// domains/stats.IsProbeTraffic 与 SQL 臂 bg.ProbeTrafficExclusionPredicateView）
// 都只用 **markers-only** 判定（task_type / quality_flags / origin_actor）。
//
// ⇒ 后果（隔离开启时）：一行 stage=探测但**无 marker** 的行，
// 写侧把它分到探测族表（不进业务表），Go 臂却判它「非探测」⇒
// 它被当**业务流量**累加进 request_stats_minute；而 SQL 臂从业务视图
// 根本读不到这行（它没进业务表）⇒ **两臂对同一个 ON CONFLICT 键
// (bucket, tenant_id, provider_id, canonical_id) 给出不同答案**，
// 且 Go 臂的累加值会把探测流量永久混进业务看板 KPI。
//
// ⇒ 修法：**写侧与 stats 两臂同口径**（markers-only）。
//   不改 stats 端 —— SQL 臂的 113 列视图**读不到 origin_stage**，
//   给它加 stage 臂会制造**新的**两臂不一致（见 probekind.IsProbeMarkers
//   的注释：两臂必须用同一套标记才能对同一行给出一致判定）。
//
// ★ 为什么 markers-only 不会漏掉真实探测流量：
//   internal/probekind.Writers 是「哪个 actor 写哪种 stage」的规范表，
//   其**每一个 actor**都被 IsProbeMarkers 的 actor 白名单覆盖
//   （ProbeActors 由 Writers 派生）⇒ 真实探测写入者必然留下 marker
//   ⇒ markers-only 判定覆盖全部真实写入者。
//   完整判定多认出的「stage-only」形态只出现在 origin_stage 残留而
//   actor/task_type/quality_flags 全丢的退化行上——那种行本就不该
//   被算成业务流量，但它同样不该进探测族表（两臂认不出它 ⇒
//   它留在业务表里由 stats 正常处理，行为与车道关闭时一致）。
//
// ⇒ 判据（可证伪）：车道开启时，进探测族表的行集合 == stats 两臂
//   判为探测的行的集合。三处判定同源于 probekind，无第二份名单。
func ShouldIsolate(originStage, taskType string, qualityFlags []string, originActor string) bool {
	if !PhysicalEnabled() {
		return false
	}
	return probekind.IsProbeMarkers(taskType, qualityFlags, originActor)
}

// RedisSuffixProbe 给 live-stream Redis 键加探测前缀（拼在原 suffix 前）。
// 例：原 "main" → "probe:main"；原 "status:success" → "probe:status:success"。
func RedisSuffixProbe(suffix string) string {
	suffix = strings.TrimPrefix(suffix, "probe:")
	if suffix == "" {
		return "probe:main"
	}
	return "probe:" + suffix
}
