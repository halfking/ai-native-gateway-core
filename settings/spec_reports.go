package settings

// ReportRollupSpecs 对账报表每日聚合的调度设置（2026-09-25 对账报表落地轮）。
//
// reports.daily_rollup.hour：每日聚合的 UTC 钟点（0-23，默认 2 = 凌晨
// 02:00 UTC，跟随容器时区的本地凌晨语义见 bg/report_rollup_worker.go）。
// HotReload：worker 每轮调度前重读，改小时无需重启。设计文档原稿定为
// cron 字符串（reports.daily_rollup.cron），落地改为单整数小时——仓库无
// cron 解析依赖，且既有的每日 worker（bg/feedback_analyzer.go RunHour）
// 均为该模式，不为一项设置引入解析面。
func ReportRollupSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "reports.daily_rollup.hour",
			Type:        TypeInt,
			Scope:       ScopePlatform,
			Category:    CategoryGeneral,
			Default:     2,
			Description: "对帐报表每日聚合时间（UTC 小时）",
			DescriptionLong: "每日该 UTC 钟点聚合前一日的用量/成本/错误分类写入报表快照表 " +
				"report_snapshots（供应商与内部双口径）。0-23；启动时也会先补跑昨日一次。",
			Unit:        "时(UTC)",
			DangerLevel: Safe,
			HotReload:   true,
			Min:         floatPtr(0),
			Max:         floatPtr(23),
		},
	}
}
