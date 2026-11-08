package settings

// spec_rate_policy.go — 2026-11-08 token 桶 W1（节奏校准冷却）的读数旋钮。
//
// 背景：docs/audit/2026-11-07-token桶双层路由优化方案.md §三 T1 —— 429 绑定级
// 冷却从「固定 3min 累计升级梯」（R186 A2）改为「节奏校准」：
//
//	冷却下限 = max(Retry-After, 窗口重灌估计)；升级**只罚节奏违规**
//	（两次 429 间隔 < 窗口估计且连击 ≥ 阈值才进 3min<<n 梯）。
//
// 四策略仿真的依据（方案档 §1.4 S1-S3）：A2 梯把 3/min 免费桶的正常性 429
// 也升到 30min 封顶 ⇒ 池饥饿（211 上 2710 例 no_candidate 的同构形态）；
// 节奏校准后亏空域 client_ok 62.8%→80.0%、免费桶 cool_time 减半。
//
// 回退：window_refill_seconds=0 = 关闭节奏校准、逐字节回到 A2 行为
//（ Observe 的遗留路径）——既有 A2 测试族全部按 0 注入保持原断言。

const (
	// KeyRateWindowRefillSeconds 是 vendor 限流窗口的重灌估计（秒）。
	// 0 = 关闭节奏校准（逐字节回 A2：3min 基准 + 无条件升级）。
	KeyRateWindowRefillSeconds = "routing.rate.window_refill_seconds"

	// KeyRatePaceViolationThreshold 是节奏违规的升级起点（连续快速 429 次数）。
	KeyRatePaceViolationThreshold = "routing.rate.pace_violation_threshold"
)

// RatePolicySpecs returns the 429 pace-calibrated cooling controls.
func RatePolicySpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeyRateWindowRefillSeconds,
			EnvName:         EnvNameAuto(KeyRateWindowRefillSeconds),
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRetry,
			Default:         60,
			Min:             floatPtr(0),
			Max:             floatPtr(600),
			Description:     "429 窗口重灌估计（秒），0=回退旧 A2 累计升级",
			DescriptionLong: "节奏校准冷却的窗口下限：无 Retry-After 的 429 至少冷却这么久（vendor 窗口重灌的时间），且两次 429 间隔 ≥ 此值视为节奏正常（计数归零、不升级）。0 = 关闭节奏校准，逐字节回到 A2 行为（3min 基准 + 无条件累计升级，封顶 30min）。Retry-After 永远优先（信任上限 1h 不变）。",
			Unit:            "秒",
			DangerLevel:     Dangerous, // 调小会放大对被限流上游的重试压力
			HotReload:       true,
		},
		{
			Key:             KeyRatePaceViolationThreshold,
			EnvName:         EnvNameAuto(KeyRatePaceViolationThreshold),
			Type:            TypeInt,
			Scope:           ScopePlatform,
			Category:        CategoryRetry,
			Default:         3,
			Min:             floatPtr(2),
			Max:             floatPtr(10),
			Description:     "节奏违规升级起点（连续快速 429 次数）",
			DescriptionLong: "两次 429 间隔 < 窗口重灌估计（快速连击）达到此次数后才开始进 3min<<n 升级梯（封顶 30min）；之前的连击只按窗口时长冷却。默认 3 = 前两次快速连击不升级。",
			Unit:            "次",
			DangerLevel:     Safe,
			HotReload:       true,
		},
	}
}

// RateWindowRefillSeconds 读取窗口重灌估计（秒）。
// 返回 0 表示关闭节奏校准（遗留 A2 路径）；正值已被 clamp 到 [0, 600]。
func RateWindowRefillSeconds() int {
	return clampThresholdInt(GetPlatformInt(KeyRateWindowRefillSeconds, 60), 0, 600)
}

// RatePaceViolationThreshold 读取节奏违规升级起点。
// 已被 clamp 到 [2, 10]；调用方对 <2 再兜底（防 0/负值让梯子在第 2 次就触发）。
func RatePaceViolationThreshold() int {
	return clampThresholdInt(GetPlatformInt(KeyRatePaceViolationThreshold, 3), 2, 10)
}
