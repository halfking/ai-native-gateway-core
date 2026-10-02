package settings

// 峰谷倍率时段表设置键（Wave 3 B1，2026-09-22 设计差距审计）。
//
// 配置同源：Go（maas.ResolveRateMultiplier）与 SQL（迁移 736 的
// maas_resolve_rate_multiplier()）都读本键取档，规则两侧一致由
// maas/rate_periods_test.go 钉住。enabled=false 时两侧都返回 1.0
// （默认关闭 = 行为零漂移，商务在管理端配置时段后显式开启）。
const KeyMaasRatePeriods = "maas.rate_periods"

// DefaultMaasRatePeriodsJSON 商务示例种子：早/晚双高峰 3x、深夜低谷 0.7x，
// 其余时间 1x；时区 Asia/Shanghai（集群默认 TZ，与 473 分区边界事故的
// 定式一致：一律显式时区，不依赖服务器本地时区）。enabled=false 落库
// 前不生效。
const DefaultMaasRatePeriodsJSON = `{"enabled": false, "timezone": "Asia/Shanghai", "periods": [{"name": "peak_am", "start": "08:00", "end": "12:00", "multiplier": 3.0}, {"name": "peak_pm", "start": "17:00", "end": "21:00", "multiplier": 3.0}, {"name": "offpeak_night", "start": "23:00", "end": "07:00", "multiplier": 0.7}]}`

// MaasRatePeriodSpecs returns the peak/off-peak rate period spec.
func MaasRatePeriodSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeyMaasRatePeriods,
			EnvName:         EnvNameAuto(KeyMaasRatePeriods),
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategoryRateLimit,
			Default:         DefaultMaasRatePeriodsJSON,
			Description:     "峰谷计费时段表",
			DescriptionLong: "JSON：{\"enabled\":bool, \"timezone\":\"Asia/Shanghai\", \"periods\":[{name,start,end,multiplier}]}。半开区间 [start,end)，start>end 视为跨午夜（如 23:00-07:00）；未命中时段 1x；enabled=false 全部 1x。倍率乘在计费总额上后按 1M 向上取整，usage_ledger.rate_multiplier / request_logs.credits_rate_multiplier 落本次倍率供对账。Go 与 SQL（maas_resolve_rate_multiplier）同源取档。",
			Unit:            "",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
	}
}
