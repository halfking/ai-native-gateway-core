package settings

// ProjectAttributionSpecs — 项目归属（LLM 推断）平台级主开关。
//
// 2026-08-20：项目归属（acc_projects → project_dim →
// session_project_attribution）是新链路，先以平台级 bool 主开关的形式
// 暴露。默认 false，明确拒绝"偷偷开启"——运维需要在确认 ACC 同步链
// 路、resolver、CloseHook 都已就绪后手动开启。
//
// 安全不变量：
//   - 推断结果写 session_project_attribution，不会写 session_dim.project_id，
//     因此即使误推断也不会污染计费口径。
//   - 仅在会话关闭时触发（不是每个请求），放大倍数 = 1。
//   - 规则层失败 / inherit 失败都降级为 "no project"，不写库。
//
// 热更新：本轮默认关闭主开关 + HotReload=false。原因：开启时同时要构造
// ProjectResolver + 挂 CloseHook + 配 ACC 同步 worker，三件事都在启动
// 时一次性确定；运行中切换会留出 "flag=true 但 resolver=nil" 的不一致
// 窗口。下轮可以再加 Reload 路径。
func ProjectAttributionSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "project_attribution.enabled",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategoryAttribution,
			Default:         false,
			Description:     "项目归属（LLM 推断）平台级主开关",
			DescriptionLong: "开启后会话关闭事件会触发项目归属推断（rule → inherit → 可选 LLM）；结果写入 session_project_attribution，不会写入 session_dim.project_id（不影响计费口径）。需要 ACC 同步链路（admin.SyncProjectsFromACCForBG）和 PG 连接池同时就绪。默认关闭，启动时一次性读取，运行时不热更新。",
			Unit:            "开关",
			DangerLevel:     Dangerous,
			HotReload:       false,
			Observability:   "/api/admin/project_attribution/stats",
		},
	}
}
