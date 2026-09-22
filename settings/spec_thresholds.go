package settings

// 路由/探测/指纹槽阈值常量表（Wave 2 任务一，2026-09-22 设计差距审计 C5/C8/C9）。
//
// 背景：节点失败 streak、Disabled 冷却、sticky 失败阈值、fp slot 归还 TTL、
// 探测 worker 并发此前散落为各包 const，运维只能改代码发版。本文件把它们
// 集中为 system_settings 平台级键（默认值=代码原值，行为零漂移），并提供
// 带防御性 clamp 的读取 helper（env 注入越界值时运行时仍被夹回安全区间）。
//
// 读取语义：
//   - 热路径读者（每次路由决策/每次请求落账）走 CachedPlatformInt：管理端
//     写入经 store_db.go InvalidatePlatformValue 立即失效，其余读者 ≤5s 生效；
//   - 启动期读者（探测 worker 池大小）走 GetPlatformInt：goroutine 池在
//     Start 时固定，热更无法改变池大小，spec 如实标 HotReload=false。
const (
	KeyNodeFailStreakLimit         = "routing.node_fail_streak_limit"
	KeyNodeDisabledCooldownSeconds = "routing.node_disabled_cooldown_seconds"
	KeyStickyFailureThreshold      = "routing.sticky_failure_threshold"
	KeyFpSlotTTLSeconds            = "disguise.fp_slot_ttl_seconds"
	KeyErrorProbeWorkers           = "error_probe.workers"
)

// 默认值 = 集中前的各包 const（单一事实来源，Spec 定义与访问器 fallback 共用）。
const (
	// DefaultNodeFailStreakLimit 节点连续失败熔断阈值（原 credentialfpslot.nodeFailStreakLimit=3）。
	DefaultNodeFailStreakLimit = 3
	// DefaultNodeDisabledCooldownSeconds 节点 Disabled 冷却秒数（原 nodeDisabledCooldownSec=300）。
	DefaultNodeDisabledCooldownSeconds = 300
	// DefaultStickyFailureThreshold sticky 连续失败踢出阈值（原 executors/sticky.go AUDIT-1 用户语义=2）。
	DefaultStickyFailureThreshold = 2
	// DefaultFpSlotTTLSeconds 指纹槽无请求自动归还 TTL（原 slotTTLSeconds=1800）。
	DefaultFpSlotTTLSeconds = 1800
	// DefaultErrorProbeWorkers 错误触发主动探测 worker 并发（设计 §5.6 "执行并发 ≤5"）。
	DefaultErrorProbeWorkers = 5
	// MaxErrorProbeWorkers 探测并发硬上限。
	MaxErrorProbeWorkers = 5
)

// ThresholdSpecs returns the centralized routing/probe/fp-slot threshold specs.
func ThresholdSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeyNodeFailStreakLimit,
			EnvName:         "LLM_GATEWAY_ROUTING_NODE_FAIL_STREAK_LIMIT",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRouting,
			Min:             floatPtr(1),
			Max:             floatPtr(10),
			Default:         DefaultNodeFailStreakLimit,
			Description:     "节点连续失败熔断阈值",
			DescriptionLong: "同一（凭据,模型）节点在滑动窗口内连续失败达到该次数即置 Disabled 进入冷却。原为 credentialfpslot 包内常量 3，Wave 2 集中化后可热更（热路径读者 ≤5s 生效，管理端写入立即生效）。",
			Unit:            "次",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             KeyNodeDisabledCooldownSeconds,
			EnvName:         "LLM_GATEWAY_ROUTING_NODE_DISABLED_COOLDOWN_SECONDS",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRouting,
			Min:             floatPtr(30),
			Max:             floatPtr(7200),
			Default:         DefaultNodeDisabledCooldownSeconds,
			Description:     "节点 Disabled 冷却时长（秒）",
			DescriptionLong: "节点被置 Disabled 后的冷却秒数，冷却到期由 IsUsable 自动恢复进路由池。原为 credentialfpslot 包内常量 300（5 分钟），Wave 2 集中化后可热更。",
			Unit:            "秒",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             KeyStickyFailureThreshold,
			EnvName:         "LLM_GATEWAY_ROUTING_STICKY_FAILURE_THRESHOLD",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRouting,
			Min:             floatPtr(1),
			Max:             floatPtr(10),
			Default:         DefaultStickyFailureThreshold,
			Description:     "Sticky 连续失败踢出阈值",
			DescriptionLong: "同一 sticky 绑定连续失败达到该次数即删除绑定，下次请求重新走路由选择（两次失败间隔 >10s 时计数重置）。原为 executors/sticky.go AUDIT-1 用户语义默认 2，Wave 2 集中化后可热更；调用方显式传参时以传参为准。",
			Unit:            "次",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             KeyFpSlotTTLSeconds,
			EnvName:         "LLM_GATEWAY_DISGUISE_FP_SLOT_TTL_SECONDS",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategorySecurity,
			Min:             floatPtr(300),
			Max:             floatPtr(86400),
			Default:         DefaultFpSlotTTLSeconds,
			Description:     "指纹槽自动归还 TTL（秒）",
			DescriptionLong: "指纹槽最后一次请求后经过该时长即由 Redis 过期自动归还，新客户端可立即获取。原为 credentialfpslot 包内常量 1800（30 分钟），Wave 2 集中化后可热更；后台 reclaim 的 idle 阈值跟随此值（管理端显式配置 disguise.reclaim_idle_seconds 时以该配置为准）。",
			Unit:            "秒",
			DangerLevel:     Warning,
			HotReload:       true,
		},
		{
			Key:             KeyErrorProbeWorkers,
			EnvName:         "LLM_GATEWAY_ERROR_PROBE_WORKERS",
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryErrorProbe,
			Min:             floatPtr(1),
			Max:             floatPtr(float64(MaxErrorProbeWorkers)),
			Default:         DefaultErrorProbeWorkers,
			Description:     "错误触发主动探测 worker 并发",
			DescriptionLong: "错误触发的主动探测队列执行并发（设计 §5.6：执行并发 ≤5）。原 main.go 硬编码 1、仅 LLM_GATEWAY_ERROR_PROBE_WORKERS 环境变量可调；接入 settings 后优先级为 settings_kv > 环境变量 > 默认 5。worker 池在进程启动时固定，修改后需重启网关生效。",
			Unit:            "个",
			DangerLevel:     Warning,
			HotReload:       false,
		},
	}
}

// NodeFailStreakLimit returns the node fail-streak circuit threshold.
// Cached read: ≤5s reload window on the routing hot path.
func NodeFailStreakLimit() int {
	return clampThresholdInt(CachedPlatformInt(KeyNodeFailStreakLimit, DefaultNodeFailStreakLimit), 1, 10)
}

// NodeDisabledCooldownSeconds returns the node Disabled cooldown in seconds.
// Cached read: ≤5s reload window on the routing hot path.
func NodeDisabledCooldownSeconds() int {
	return clampThresholdInt(CachedPlatformInt(KeyNodeDisabledCooldownSeconds, DefaultNodeDisabledCooldownSeconds), 30, 7200)
}

// StickyFailureThreshold returns the sticky consecutive-failure eviction
// threshold used as the default when a RecordFailure caller passes no
// explicit threshold.
func StickyFailureThreshold() int {
	return clampThresholdInt(CachedPlatformInt(KeyStickyFailureThreshold, DefaultStickyFailureThreshold), 1, 10)
}

// FpSlotTTLSeconds returns the fingerprint-slot auto-release TTL in seconds.
// Cached read: slot TTL is stamped on every acquire/Lua call.
func FpSlotTTLSeconds() int {
	return clampThresholdInt(CachedPlatformInt(KeyFpSlotTTLSeconds, DefaultFpSlotTTLSeconds), 300, 86400)
}

// ErrorProbeWorkers returns the error-triggered active-probe worker pool
// size. Startup-only read (goroutine pool is fixed at Start).
func ErrorProbeWorkers() int {
	return clampThresholdInt(GetPlatformInt(KeyErrorProbeWorkers, DefaultErrorProbeWorkers), 1, MaxErrorProbeWorkers)
}

// clampThresholdInt defends against out-of-range values arriving through
// env or a hand-edited settings_kv row (Spec.Min/Max only gate admin PUTs).
func clampThresholdInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
