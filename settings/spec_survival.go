package settings

// spec_survival.go — P0-2（2026-10-14，docs/research/2026-10-14-magpie对比
// 与优化方案.md §P0-2）：请求生存协调器（SR-06 in-connection recovery）的
// 交互等待预算热加载键。
//
// 背景（154 生产事故）：候选全灭后请求进入 wait_recovery 轮询，交互 deadline
// 默认 5 小时，客户端拿到 200+keepalive 后被无限挂着（p99=889s、max 85min）。
// 默认值已在 config 层分级下调到 240s；本键提供不改部署的热加载逃生舱，
// 语义与 config 的 LLM_GATEWAY_REQUEST_SURVIVAL_INTERACTIVE_DEADLINE_SECONDS
// 一致但优先级更高（settings_kv > config/env，参照 settings 注册表统一
// 优先级链 DB > env > default）。
//
// 归类 CategoryRetry（2026-09-22 Wave 3 B5③「网关重试与超时」聚合组）：
// survival 交互 deadline 正是该组的语义域，且避免新建前端类别注册面。

// KeySurvivalInteractiveDeadlineSeconds 覆盖生存协调器的交互 deadline。
// 0 = 未设置（回落 config 值——config 默认 240s）；生效区间 [10, 18000]。
const KeySurvivalInteractiveDeadlineSeconds = "survival.interactive_deadline_seconds"

// SurvivalSpecs returns the request-survival hot-reload controls.
func SurvivalSpecs() []*Spec {
	return []*Spec{
		{
			Key:      KeySurvivalInteractiveDeadlineSeconds,
			EnvName:  EnvNameAuto(KeySurvivalInteractiveDeadlineSeconds),
			Type:     TypeInt,
			Scope:    ScopePlatform,
			Category: CategoryRetry, // B5③ 网关重试与超时聚合组
			// 0 是合法哨兵（未设置→回落 config 值），故 Default 低于 Min；
			// 越界写入由管理端 Validate 拒绝，读取侧 SurvivalInteractive
			// DeadlineSeconds 的 clamp 只对 >0 值生效、永不把哨兵抬成 10。
			Default:         0,
			Min:             floatPtr(10),
			Max:             floatPtr(18000),
			Description:     "流式请求生存协调器的交互等待预算（秒），0=用部署配置值（默认 240）",
			DescriptionLong: "候选全灭后请求在连接内等待恢复的总预算。154 事故（p99=889s、max 85min 挂起）后默认从 5h 分级下调为 240s；本键热加载覆盖部署配置（settings_kv > config/env），0 表示未设置、回落 config 值。上限 18000s=5h 保持原合法区间，需要长等待的专用场景可按需调高。每次请求 Run 时热读取。",
			Unit:            "秒",
			DangerLevel:     Dangerous, // 改动立即影响所有流式请求的最大等待
			HotReload:       true,
		},
	}
}

// SurvivalInteractiveDeadlineSeconds 读取交互等待预算覆盖值。
// 返回 0 表示未设置（回落 config 值）；正值已被 clamp 到 [10, 18000]，
// 手改 settings_kv 的越界行不会把等待预算夹成危险值。
func SurvivalInteractiveDeadlineSeconds() int {
	v := GetPlatformInt(KeySurvivalInteractiveDeadlineSeconds, 0)
	if v <= 0 {
		return 0
	}
	return clampThresholdInt(v, 10, 18000)
}
