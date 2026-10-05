//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// v1ReaderRetirementTiering —— 读 v1 的生产文件 × **消费面分档**（审计 §9.262）。
//
// # 它和已有那张表不是一回事
//
// `requestLogsReadInventory`（107 文件 / 254 调用点）答的是「谁在文本上写了
// `from request_logs…`」；`cmd/tools/sql_source_indirection_audit` 答的是
// 「哪些读点的关系名是 Go 标识符拼进去的」。**两张表是两个总体，不是一张表的两半**
// —— 这正是 §9.259 那句「114」踩的坑，下面 TestV1ReaderTieringDomainIsBothPopulations
// 把两个总体各自的成员钉住，就是为了不让下一次再把它们当成一个数。
//
// 本表答第三个问题：**v1 退役后，这个读点的消费者还在不在**。四档：
//
//	① 随 v1 一起退役   —— 读 v1 的唯一存在理由就是 v1 自身（生命周期 DDL、一次性迁移工具）
//	② 必须迁           —— 消费者在 v1 退役后仍存在（对外 API / 计费 / 聚合 / 会话读取）
//	③ 按设计继续读 v1  —— 消费者就是「观测 v1/凭证/节点」本身，**没有外部契约依赖它**
//	④ 待拍板           —— 判据不唯一，把具体疑点写进 Reason
//
// # 为什么不自动分档
//
// `requestLogsReadInventory` 的文件头已经记过一次教训：自动分 A/B/C/D 判错 5 个。
// 本表的分档判据在**消费面**（不是语句窗口），但仍然是**人判 + 门核**：
// 每条都带一句可核的 Reason，门逐条断言 Reason 非空、并断言每个文件确实读 v1。
//
// # 它只出表，不改指
//
// 本表**不修改任何读方**。② 档的迁移动作是属主的决定，不是本门的。
type retirementTieringEntry struct {
	Tier   string
	Reason string
}

var v1ReaderRetirementTiering = map[string]retirementTieringEntry{
	"admin/analytics.go":                              {Tier: "②", Reason: "对外 API：session-analytics 端点，响应直接含 v1 统计"},
	"admin/attachments_routes.go":                     {Tier: "②", Reason: "对外 API：附件归属校验，退役后仍要判归属"},
	"admin/attempt_quality_api.go":                    {Tier: "②", Reason: "对外 API：尝试质量指标（读 v1 臂视图，§9.260 补进来的）"},
	"admin/auto_route.go":                             {Tier: "②", Reason: "对外 API：auto-route 决策/关联"},
	"admin/auto_route_correlations.go":                {Tier: "③", Reason: "auto-route 关联分析：观测面"},
	"admin/auto_route_outcome_freshness.go":           {Tier: "④", Reason: "auto-route 结果新鲜度 queryOutcomeFreshness：是结算正确性门（②）还是自检（③），取决于它卡不卡结算"},
	"admin/auto_title_generator.go":                   {Tier: "②", Reason: "对外功能：会话标题生成读 v1 首条成功轮次"},
	"admin/body_resolver.go":                          {Tier: "②", Reason: "对外功能：请求体解析（bodies），退役后详情页仍要显示"},
	"admin/compression_sessions.go":                   {Tier: "②", Reason: "对外 API：压缩会话端点"},
	"admin/compression_stats.go":                      {Tier: "②", Reason: "对外 API：压缩统计端点"},
	"admin/credential_monitor.go":                     {Tier: "③", Reason: "凭证监控摘要（summaryCache + buildMonitorSummarySQL）：观测面，读 v1 是被观测对象"},
	"admin/credential_monitor_heatmap.go":             {Tier: "③", Reason: "凭证健康热力图：同上，运维观测面"},
	"admin/credential_success_rate.go":                {Tier: "②", Reason: "对外 API：凭证成功率。★ 注意：它同时是监控面，但走 HTTP 响应，退役后端点仍需有数"},
	"admin/dashboard_board_queries.go":                {Tier: "②", Reason: "对外 API：看板首屏聚合（读 composer 建的 v1 臂视图）"},
	"admin/data_lifecycle.go":                         {Tier: "②", Reason: "对外 API + **对 v1 有写**：数据生命周期清理/预览，退役后清理对象要改"},
	"admin/data_lifecycle_attachments.go":             {Tier: "②", Reason: "对外 API：附件清理"},
	"admin/data_lifecycle_blobs.go":                   {Tier: "②", Reason: "对外 API + 对 v1 有写：blob 清理"},
	"admin/data_lifecycle_metrics.go":                 {Tier: "②", Reason: "对外 API：生命周期指标"},
	"admin/diagnostics_credential.go":                 {Tier: "③", Reason: "凭证诊断端点 handleCredentialDiagnostic：诊断面"},
	"admin/live_stream_sse.go":                        {Tier: "④", Reason: "SSE 实时流 replay/terminalStatusesFromDB：退役后是否继续供流式详情，需属主定"},
	"admin/logs.go":                                   {Tier: "②", Reason: "对外 API：日志列表/详情/统计（读 v1 基表）"},
	"admin/logs_summary.go":                           {Tier: "②", Reason: "对外功能：会话日志摘要（bodies 读方）"},
	"admin/memora_handlers.go":                        {Tier: "②", Reason: "对外 API：memora 会话/上下文端点"},
	"admin/model_routing_diagnostic.go":               {Tier: "③", Reason: "模型路由诊断端点：诊断面"},
	"admin/model_status.go":                           {Tier: "②", Reason: "对外 API：模型状态"},
	"admin/no_topic_session.go":                       {Tier: "②", Reason: "对外 API：无主题会话端点"},
	"admin/probe_history.go":                          {Tier: "③", Reason: "probe-run 历史端点：这个读的**目的**就是看 v1 探针跑过什么"},
	"admin/provider_diagnose.go":                      {Tier: "③", Reason: "provider 诊断（从 provider_probe.go 拆出）：诊断面"},
	"admin/provider_models.go":                        {Tier: "②", Reason: "对外 API：provider 下的模型与用量（§9.49 动态列盲区形态）"},
	"admin/providers.go":                              {Tier: "②", Reason: "对外 API：provider 列表"},
	"admin/quality_correlations.go":                   {Tier: "③", Reason: "请求级质量关联分析：观测面"},
	"admin/request_trace.go":                          {Tier: "②", Reason: "对外 API：请求链路"},
	"admin/route_incidents.go":                        {Tier: "③", Reason: "路由事故（route_incidents）：观测面"},
	"admin/routing.go":                                {Tier: "②", Reason: "对外 API：路由管理（含诊断路由）"},
	"admin/session_analytics_breakdown.go":            {Tier: "②", Reason: "对外 API：会话分析分组"},
	"admin/session_analytics_timeseries.go":           {Tier: "②", Reason: "对外 API：会话分析时序"},
	"admin/session_bodies_batch.go":                   {Tier: "②", Reason: "对外功能：bodies 批量读（经切换层 SessionBodiesSourceSQL）"},
	"admin/session_compare.go":                        {Tier: "②", Reason: "对外 API：会话对比（间接读方，§9.259 点名的 7 个之一）"},
	"admin/session_detail_v2.go":                      {Tier: "②", Reason: "对外 API：会话详情 v2"},
	"admin/session_export.go":                         {Tier: "②", Reason: "对外 API：会话导出（★ FROM 读会话族 LEFT JOIN bodies，退役总体是 session_turns 不是 v1）"},
	"admin/session_extract.go":                        {Tier: "②", Reason: "对外 API：会话抽取"},
	"admin/session_management_api.go":                 {Tier: "②", Reason: "对外 API：会话管理"},
	"admin/session_online.go":                         {Tier: "②", Reason: "对外 API：在线会话时间线"},
	"admin/session_sanitize_matches.go":               {Tier: "②", Reason: "对外功能：脱敏匹配（bodies 读方）"},
	"admin/session_summary_v2.go":                     {Tier: "②", Reason: "对外功能：会话摘要 v2（含 v1 回退查询）"},
	"admin/session_tenant.go":                         {Tier: "②", Reason: "对外功能：会话租户判定"},
	"admin/session_timeline_query.go":                 {Tier: "②", Reason: "对外功能：会话时间线查询"},
	"admin/session_title.go":                          {Tier: "②", Reason: "对外功能：任务标题生成"},
	"admin/session_turns_tree.go":                     {Tier: "②", Reason: "对外 API：会话轮次树"},
	"admin/session_turns_unified.go":                  {Tier: "②", Reason: "对外 API：统一轮次读端"},
	"admin/swim_lane_init.go":                         {Tier: "②", Reason: "对外 API：看板泳道初始化"},
	"admin/telemetry.go":                              {Tier: "②", Reason: "对外 API + **对 v1 有写**：`/api/telemetry/request-log` 是 v1 写入端点之一"},
	"admin/tenants.go":                                {Tier: "②", Reason: "对外 API：租户列表与统计"},
	"admin/top_problems.go":                           {Tier: "③", Reason: "top-problems 报告：观测面"},
	"admin/unified_detail.go":                         {Tier: "②", Reason: "对外功能：统一详情（bodies 读方）"},
	"admin/usage.go":                                  {Tier: "②", Reason: "对外 API：用量（**含 `/api/maas/usage/summary` 对外计费面**）"},
	"admin/usage_credits.go":                          {Tier: "②", Reason: "对外 API + 切换层：积分用量，requestLogsFromClause 切换层所在地"},
	"admin/usage_enhanced.go":                         {Tier: "②", Reason: "对外 API：用量增强（planCostTrend 切换层）"},
	"admin/usage_trend_series.go":                     {Tier: "②", Reason: "对外 API：用量趋势时序（usageTrendSource 切换层；★ §9.260 点名的 3 个字面量盲区之一）"},
	"admin/work_types.go":                             {Tier: "②", Reason: "对外 API：工作类型统计"},
	"autoroute/recommend_v2.go":                       {Tier: "③", Reason: "推荐索引（读 v1 纠错分）：自检面"},
	"bg/auto_index_refresher.go":                      {Tier: "②", Reason: "后台：自动建索引刷新器（按 v1 表规模决策，退役后要改判对象）"},
	"bg/auto_route_affinity_worker.go":                {Tier: "②", Reason: "后台：任务→模型亲和度学习（从已结算选择学，读 v1）"},
	"bg/auto_route_settle_sql.go":                     {Tier: "②", Reason: "★ 后台结算：`settleSourceFor` 写门开＝v1_hot、写门关＝session_turns_hot，同一段 SQL 两族切换"},
	"bg/candidate_failure_monitor.go":                 {Tier: "③", Reason: "候选失败监控：探针面"},
	"bg/credential_recovery.go":                       {Tier: "③", Reason: "凭证恢复（探针驱动）：观测→动作，探针面"},
	"bg/credential_selfcheck.go":                      {Tier: "③", Reason: "凭证自检：读 v1 是被自检对象"},
	"bg/daily_probe_audit.go":                         {Tier: "③", Reason: "每日探针审计：探针面"},
	"bg/integrity_fingerprint_drift.go":               {Tier: "③", Reason: "指纹漂移：探针面"},
	"bg/integrity_fingerprint_probe.go":               {Tier: "③", Reason: "指纹探针：探针面"},
	"bg/ledger_reconciliation.go":                     {Tier: "③", Reason: "账本对账（usage_ledger↔credit_ledger）：对账面"},
	"bg/lite_retention_worker.go":                     {Tier: "①", Reason: "lite 模式行级 TTL：v1 那条 DELETE 腿随 v1 退役；**文件本身不退役**（同函数还清 sessions/session_turns）"},
	"bg/model_probe.go":                               {Tier: "③", Reason: "模型探针：探针面"},
	"bg/model_tier.go":                                {Tier: "③", Reason: "常用模型分级「用于自检」：自检面"},
	"bg/passive_probe_listener.go":                    {Tier: "③", Reason: "被动观测监听器（Layer 5 passive observer）：观测面"},
	"bg/shared_pick.go":                               {Tier: "②", Reason: "后台：探针选型共享逻辑（PickProbeModelForCredential）"},
	"bg/stats_minute_rollup.go":                       {Tier: "②", Reason: "后台：分钟级聚合 **写入 request_stats_minute**（聚合表退役后仍要算，读源必须改）"},
	"bg/stats_minute_rollup_retire.go":                {Tier: "②", Reason: "后台：删除聚合表里视图不再产出的键 —— **NOT EXISTS 守卫读 v1 臂视图**，退役后守卫要改指"},
	"bg/today_success_probe.go":                       {Tier: "③", Reason: "当日成功探针：探针面"},
	"cmd/compression-bench/main.go":                   {Tier: "④", Reason: "scenario_driver / traffic-replay / compression-bench：压测与回放工具，读 v1 形态随工具定位"},
	"cmd/gateway/dual_read_validator.go":              {Tier: "③", Reason: "双读校验器：存在的意义就是校验 v1 与 v2 读数一致"},
	"cmd/gateway/main_v3_wiring.go":                   {Tier: "②", Reason: "进程装配：V3 接线引用 v1 读点"},
	"cmd/gateway/output_compliance_control.go":        {Tier: "④", Reason: "output 出口合规控制：读 v1 判合规，合规门在退役后是否还有对象，需属主定"},
	"cmd/gateway/waterfall_by_request.go":             {Tier: "②", Reason: "对外功能：按请求的 waterfall"},
	"cmd/gateway/waterfall_db.go":                     {Tier: "②", Reason: "对外功能：waterfall DB 侧"},
	"cmd/scenario_driver/main.go":                     {Tier: "④", Reason: "scenario_driver / traffic-replay / compression-bench：压测与回放工具，读 v1 形态随工具定位"},
	"cmd/tools/backfill_session_bodies/main.go":       {Tier: "①", Reason: "一次性 bodies 回填工具：源就是 v1 bodies（matryoshka），V2 上线前置，跑完即退役"},
	"cmd/tools/validate_sessions_v2/loader.go":        {Tier: "①", Reason: "一次性 v1↔v2 对账工具：LoadV1Turns 读 request_logs_bodies，工具跑完即退役（cmd/tools/validate_sessions_v2）"},
	"cmd/traffic-replay/main.go":                      {Tier: "④", Reason: "scenario_driver / traffic-replay / compression-bench：压测与回放工具，读 v1 形态随工具定位"},
	"db/db.go":                                        {Tier: "①", Reason: "v1 生命周期 DDL：12 处 CREATE/ALTER/DROP request_logs*，读的不是数据而是 v1 这张表本身（db/db.go）"},
	"db/probe_views_unified.go":                       {Tier: "③", Reason: "节点状态派生 SSOT（SQL 片段）：探针/节点面"},
	"discovery/discovery.go":                          {Tier: "②", Reason: "对外功能：模型自动发现"},
	"domains/analysis/optimizer.go":                   {Tier: "④", Reason: "优化建议器 loadStats 读 v1 统计：建议是「观测」还是「面向用户的产出」，两侧都讲得通"},
	"domains/analysis/request_summary.go":             {Tier: "②", Reason: "分析域：请求摘要产出"},
	"domains/attachments/handler.go":                  {Tier: "②", Reason: "对外功能：附件按请求列出（ListByRequest）"},
	"domains/credentialstate/popularity_tracker.go":   {Tier: "②", Reason: "后台：模型热度统计"},
	"domains/hooks/goal/history_store.go":             {Tier: "②", Reason: "对外功能：目标历史按会话取（FetchBySession）"},
	"domains/hooks/observability/telemetry/client.go": {Tier: "②", Reason: "★ 对 v1 有写（UPDATE）+ 读：telemetry 落库"},
	"domains/providerprofile/adapters.go":             {Tier: "③", Reason: "provider 网络探针/请求分析适配器：探针面"},
	"domains/routeincident/store.go":                  {Tier: "③", Reason: "路由事故 24h 时间线 store：观测面"},
	"domains/sessionforensics/export.go":              {Tier: "②", Reason: "对外功能：会话取证导出"},
	"domains/sessionsummary/summarizer.go":            {Tier: "②", Reason: "对外功能：会话摘要（GetMessagesSince 等）"},
	"domains/sessionsummary/system_prompt_prefix.go":  {Tier: "②", Reason: "对外功能：系统提示前缀"},
	"domains/streaming/anomaly_harvester.go":          {Tier: "③", Reason: "异常收集器：观测面"},
	"domains/streaming/model_alternatives.go":         {Tier: "②", Reason: "对外功能：模型备选（读 v1_hot 热度）"},
	"internal/collector/gateway_adapters.go":          {Tier: "④", Reason: "collector 的 PG 读适配器：Snapshot/DatabaseSizeMB 读 v1，但是给通用 collector 用还是给探针用，需属主定"},
	"internal/quality/minute_aggregator.go":           {Tier: "②", Reason: "后台：分钟质量聚合"},
	"internal/summarystore/store.go":                  {Tier: "②", Reason: "对外功能：会话摘要持久化"},
	"internal/trace/trace.go":                         {Tier: "②", Reason: "★ 对 v1 有写 + 读：链路追踪 FlushToPG"},
	"maas/consumption_detail.go":                      {Tier: "②", Reason: "★ 对外计费 API：消费明细"},
	"maas/credit_buckets.go":                          {Tier: "②", Reason: "★ 对外计费 API：积分桶"},
	"maas/usage.go":                                   {Tier: "②", Reason: "★ 对外计费 API：requestLogsSource 切换层所在地，退役后计费读数不能少"},
	"storage/sqlite/request_log_store.go":             {Tier: "②", Reason: "★ 对 v1 有写 + 读：lite 模式 SQLite 存储（WriteRequest/GetRequest）"},
	"tests/session_audit/cmd/audit-test/main.go":      {Tier: "②", Reason: "测试工具（build tag tools）：会话审计"},
	"tests/test_popularity_tracker.go":                {Tier: "②", Reason: "测试：热度统计"},
}

// v1ArmViewNames 是「体里含 v1 臂」的视图集合，**与运行时真库对过账**。
//
// 真库 `pg_class`（2026-10-06 实测，`llm-gateway-pg` / `llm_gateway`）里
// `relkind='v' AND relname LIKE 'request_logs%'` 只有这 4 个，
// 且 4 个**全部**引用 v1 基表：
//
//	request_logs_bodies_with_current_month                  → request_logs_bodies, request_logs_bodies_hot
//	request_logs_with_current_month                          → request_logs, request_logs_hot, session_turns(_hot), session_turn_details(_hot)
//	request_logs_with_current_month_without_customer_id      → request_logs, request_logs_hot
//	request_logs_with_current_month_without_request_class_due_at → request_logs, request_logs_hot
//
// ★ 「与运行时对账」不是形式：推导源是 `sql/objects/views/*.sql` +
// `db/request_logs_view_schema.go`，而后面那两个 `…_without_*` **没有仓库 DDL 文件**
// （composer 运行时建）。只看目录会漏掉它们，而漏掉的方向是
// **把读它们的文件判成 canonical（安全）**。
//
// ⚠ 本集合**只答「有没有 v1 臂」，不答「v1 臂占多大比例」**。
var v1ArmViewNamesRuntimeChecked = []string{
	"request_logs_bodies_with_current_month",
	"request_logs_with_current_month",
	"request_logs_with_current_month_without_customer_id",
	"request_logs_with_current_month_without_request_class_due_at",
}

// v1ArmLiteralBlindSpot 是「读 v1 臂视图、但关系名是字面量 ⇒ 拼接点工具完全看不见」的文件。
//
// ★ 规模比 §9.260 登记的 3 个大一个量级：**39 个**（当时点名的
// attempt_quality_api / usage_trend_series / auto_route_correlations 只是其中三个）。
//
// 后果要说准：`sql_source_indirection_audit` 的收尾行
// 「★ 退役读方清单 = v1 基表 12 处 + v1 臂视图 26 处 = 38 处 / 21 个文件」
// **不是退役读方清单** —— 115 个读 v1 的文件里它只覆盖 21 个。
// 那 39 个不是「被判成安全」，是**连站点都不产生**（工具只枚举 `+` 拼接点）。
//
// ⇒ 这 39 个由本门覆盖，而不是没人管：下面的
// TestV1ArmLiteralBlindSpotIsPinnedAndTiered 逐个点名。
var v1ArmLiteralBlindSpot = []string{
	"admin/attachments_routes.go",
	"admin/attempt_quality_api.go",
	"admin/auto_route.go",
	"admin/auto_route_correlations.go",
	"admin/body_resolver.go",
	"admin/compression_sessions.go",
	"admin/credential_monitor.go",
	"admin/credential_monitor_heatmap.go",
	"admin/credential_success_rate.go",
	"admin/data_lifecycle.go",
	"admin/data_lifecycle_blobs.go",
	"admin/live_stream_sse.go",
	"admin/model_routing_diagnostic.go",
	"admin/model_status.go",
	"admin/probe_history.go",
	"admin/provider_models.go",
	"admin/route_incidents.go",
	"admin/session_analytics_breakdown.go",
	"admin/session_analytics_timeseries.go",
	"admin/session_bodies_batch.go",
	"admin/session_extract.go",
	"admin/session_management_api.go",
	"admin/session_summary_v2.go",
	"admin/session_timeline_query.go",
	"admin/session_turns_unified.go",
	"admin/top_problems.go",
	"admin/unified_detail.go",
	"admin/usage.go",
	"admin/usage_trend_series.go",
	"bg/candidate_failure_monitor.go",
	"bg/daily_probe_audit.go",
	"bg/integrity_fingerprint_drift.go",
	"bg/shared_pick.go",
	"bg/stats_minute_rollup.go",
	"bg/stats_minute_rollup_retire.go",
	"db/probe_views_unified.go",
	"domains/attachments/handler.go",
	"domains/routeincident/store.go",
	"internal/collector/gateway_adapters.go",
}

// v1ReaderIndirectPopulation 是**第二个总体**：读 v1 但关系名不是 FROM 字面量的文件，
// 22 个 = 拼接点工具的 v1∪v1臂两个桶（21 个）+ §9.258 表格里那 3 处手验确认读
// v1 基表所在的文件（`bg/auto_route_settle_sql.go`，`src.TurnsTable`）。
//
// ★ **这道门存在的理由就是「两个总体必须分别钉住」**。
// §9.259 写「并集 114」时，公式里的 21 用的是工具桶（**不含**
// `bg/auto_route_settle_sql.go`），而同一段那份「7 个不在字面量表里」的手写清单
// **含**它、**不含** `admin/dashboard_board_queries.go` —— 后者直到 §9.260
// 把 composer 建的视图补进推导源才变成 v1 读方。两个总体各 7 个、总数差 1，
// 而**没有任何一道门会红**。
//
// 实测：字面量 107 ∩ 间接 22 = 14，并集 = **115**。
var v1ReaderIndirectPopulation = []string{
	"admin/auto_title_generator.go",
	"admin/compression_stats.go",
	"admin/dashboard_board_queries.go",
	"admin/logs.go",
	"admin/logs_summary.go",
	"admin/memora_handlers.go",
	"admin/no_topic_session.go",
	"admin/session_compare.go",
	"admin/session_export.go",
	"admin/session_sanitize_matches.go",
	"admin/session_title.go",
	"admin/tenants.go",
	"admin/usage_credits.go",
	"bg/auto_route_settle_sql.go",
	"bg/passive_probe_listener.go",
	"cmd/tools/backfill_session_bodies/main.go",
	"domains/sessionforensics/export.go",
	"domains/sessionsummary/summarizer.go",
	"domains/sessionsummary/system_prompt_prefix.go",
	"maas/consumption_detail.go",
	"maas/credit_buckets.go",
	"maas/usage.go",
}

// TestV1ReaderTieringDomainIsBothPopulations 把两个总体分别钉住。
//
// ★ 这道门存在的唯一理由：§9.259 写下「并集 114 文件」时，
// 公式里的「21」用的是工具的 v1∪arm 桶（**不含** bg/auto_route_settle_sql.go），
// 而同一段的「7 个不在字面量表里」那份手写清单**含**它、**不含**
// admin/dashboard_board_queries.go（§9.260 修好推导器之后它才变成 v1 读方）。
// 两个总体各 7 个、总数差 1，**没有任何一道门会红** —— 114 就这么写进了文档。
//
// 实测：域 = 107 ∪ 22 − 14 = **115**。
func TestV1ReaderTieringDomainIsBothPopulations(t *testing.T) {
	literal := map[string]bool{}
	for f := range requestLogsReadInventory {
		literal[f] = true
	}
	indirect := map[string]bool{}
	for _, f := range v1ReaderIndirectPopulation {
		if indirect[f] {
			t.Errorf("v1ReaderIndirectPopulation 里 %s 重复登记", f)
		}
		indirect[f] = true
	}
	domain := map[string]bool{}
	for f := range v1ReaderRetirementTiering {
		domain[f] = true
	}

	// ① 域 == 字面量 ∪ 间接（双向，少一个就红）
	var missingFromDomain, notInEither []string
	for f := range domain {
		if !literal[f] && !indirect[f] {
			notInEither = append(notInEither, f)
		}
	}
	for f := range literal {
		if !domain[f] {
			missingFromDomain = append(missingFromDomain, f)
		}
	}
	// 两个总体各查一遍，**去重**后再报 —— 同一文件在两遍里都缺时只报一次，
	// 否则报错里会出现同一个文件两行，读的人要再判一次哪个是真因。
	seenMissing := map[string]bool{}
	for _, f := range v1ReaderIndirectPopulation {
		if !domain[f] {
			seenMissing[f+"（间接总体但无分档）"] = true
		}
	}
	for f := range indirect {
		if !literal[f] && !domain[f] {
			seenMissing[f+"（间接总体但无分档）"] = true
		}
	}
	for m := range seenMissing {
		missingFromDomain = append(missingFromDomain, m)
	}
	sort.Strings(missingFromDomain)
	sort.Strings(notInEither)
	if len(missingFromDomain) > 0 {
		t.Errorf("有 %d 个读 v1 的文件没有分档（新增读方必须分档，不能靠「表还在」蒙过去）：%s",
			len(missingFromDomain), strings.Join(missingFromDomain, " "))
	}
	if len(notInEither) > 0 {
		t.Errorf("分档域里有 %d 个文件既不在字面量总体也不在间接总体（收录了从不读 v1 的文件）：%s",
			len(notInEither), strings.Join(notInEither, " "))
	}

	// ② 四个数必须与器眼一致。「114 → 115」就是死在这条上的：
	//    三个数各自都对，**只有并集**因为两个总体换了成员而错了 1。
	inter := 0
	for f := range indirect {
		if literal[f] {
			inter++
		}
	}
	union := len(domain)
	t.Logf("两个总体：字面量 %d + 间接 %d − 交集 %d = 并集 %d（域实测 %d）",
		len(literal), len(indirect), inter, len(literal)+len(indirect)-inter, union)
	for _, c := range []struct {
		name     string
		got, exp int
	}{
		{"字面量总体", len(literal), 107},
		{"间接总体", len(indirect), 22},
		{"交集", inter, 14},
		{"并集（域）", union, 115},
	} {
		if c.got != c.exp {
			t.Errorf("%s = %d，期望 %d", c.name, c.got, c.exp)
		}
	}
	if got, exp := len(literal)+len(indirect)-inter, union; got != exp {
		t.Errorf("★ 并集算式 %d ≠ 域实测 %d —— 「114」那类错误就发生在这里："+
			"三个数各自都对，但两个总体换了成员，并集差 1 且没有任何门会红",
			got, exp)
	}

	// ③ 间接总体里**不在**字面量总体的那几个，逐个列名。
	//    §9.259 手写的那份是 7 个（maas×3 / usage_credits / session_compare /
	//    session_export / auto_route_settle_sql），实测是 8 个 ——
	//    多的那个是 admin/dashboard_board_queries.go（§9.260 之后才变成 v1 读方）。
	var indirectOnly []string
	for f := range indirect {
		if !literal[f] {
			indirectOnly = append(indirectOnly, f)
		}
	}
	sort.Strings(indirectOnly)
	t.Logf("只在间接总体、不在字面量总体的 %d 个：%s", len(indirectOnly), strings.Join(indirectOnly, " "))
}

// TestV1ReaderTieringEveryFileProvenToReadV1 让分档表**自证**总体。
//
// 一张 115 行的手写表，最典型的腐烂形态是某个文件早就改指会话源了，
// 而它还留在表里被当成退役工作量。这里对每个文件独立取证：剥掉 Go 与 SQL 注释后，
// 它必须仍然含 v1 关系的 `FROM/JOIN` 字面量，或含一个 v1 关系名字面量。
//
// ★ 带阴性对照：判据在注释里写 `FROM request_logs_hot` 时必须给 0。
// 没有对照的话，「剥注释」写错了也会一直绿。
func TestV1ReaderTieringEveryFileProvenToReadV1(t *testing.T) {
	root := repoRootFromCaller(t)

	// 阴性 + 阳性对照（两条通路各一组）。没有对照，「剥注释没生效」
	// 与「正则写窄了」都会表现为全体假绿，而假绿看起来和通过一模一样。
	if l, _ := v1ReadEvidence("-- FROM request_logs_hot\n// FROM request_logs_bodies_hot\n"); l != 0 {
		t.Errorf("阴性对照失败：注释里的 FROM request_logs 被判成 %d 个读点，期望 0 —— "+
			"剥注释没生效，这道门会恒绿", l)
	}
	if l, sp := v1ReadEvidence("SELECT 1 FROM request_logs_with_current_month"); l == 0 || sp != 0 {
		t.Errorf("阳性对照失败：字面量 FROM request_logs_with_current_month → literal=%d spliced=%d，"+
			"期望 literal≥1 且 spliced=0（下面的逐文件取证会全体假绿）", l, sp)
	}
	if l, sp := v1ReadEvidence("SELECT 1 FROM ` + logsTable + ` WHERE 1=1"); sp == 0 || l != 0 {
		t.Errorf("拼接对照失败：FROM + logsTable + → literal=%d spliced=%d，期望 spliced≥1 且 literal=0", l, sp)
	}
	// ★ 这条对照来自 domains/streaming/model_alternatives.go —— 那个文件里
	// `-- request_logs_with_current_month …` 只在 SQL 注释中，真读点是
	// `FROM request_logs_hot`。剥注释若不钻进原始字符串，它会被误判成臂视图读方。
	if readsAnyV1ArmView("`\n-- request_logs_with_current_month is a UNION\nFROM request_logs_hot\n`") {
		t.Errorf("对照失败：原始字符串里的 SQL 注释被判成 v1 臂视图读点 —— " +
			"剥注释没有钻进字符串里（domains/streaming/model_alternatives.go 就是这个形状）")
	}
	if l, _ := v1ReadEvidence("SELECT 1 FROM session_turns_hot"); l != 0 {
		t.Errorf("会话族对照失败：FROM session_turns_hot 被判成 %d 个 v1 读点，期望 0 —— "+
			"这说明 v1 判据宽到会把会话族收进来", l)
	}

	indirect := map[string]bool{}
	for _, f := range v1ReaderIndirectPopulation {
		indirect[f] = true
	}

	var unproven, splicedNotInPopulation []string
	for f := range v1ReaderRetirementTiering {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			unproven = append(unproven, f+"（读不到文件："+err.Error()+"）")
			continue
		}
		literal, spliced := v1ReadEvidence(b)
		switch {
		case literal > 0:
			// 指名了 v1 —— 直接成立
		case spliced > 0 && indirect[f]:
			// 拼接形态 **且** 在第二个总体里：两个器眼互相印证。
			// 只满足拼接是不够的 —— 一个 `FROM ` + x + ` 也可能拼的是会话族。
		case spliced > 0:
			splicedNotInPopulation = append(splicedNotInPopulation, f)
		default:
			unproven = append(unproven, f)
		}
	}
	sort.Strings(unproven)
	sort.Strings(splicedNotInPopulation)
	if len(splicedNotInPopulation) > 0 {
		t.Errorf("以下 %d 个文件是「FROM 拼 Go 表达式」形态，但**不在** v1ReaderIndirectPopulation 里 —— "+
			"要么它拼的是会话族，要么第二个总体漏了它：%s",
			len(splicedNotInPopulation), strings.Join(splicedNotInPopulation, " "))
	}
	if len(unproven) > 0 {
		t.Errorf("以下 %d 个已分档文件在当前源码里**取不到任何 v1 读点证据**"+
			"（既不指名 v1 关系，也不是拼接形态；可能已改指会话源，或分档表收录了从不读 v1 的文件）：%s",
			len(unproven), strings.Join(unproven, " "))
	}
}

// TestV1ReaderTiersAssertMembers 断言**成员**，不只断言计数。
//
// 为什么必须是成员：一个计数可以被「一个退出、一个进入」换掉而不变。
// §9.173 的 ledger 门就是这么放行的，这里不重复那个错。
func TestV1ReaderTiersAssertMembers(t *testing.T) {
	byTier := map[string][]string{}
	for f, e := range v1ReaderRetirementTiering {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("%s 的分档 Reason 为空 —— 没有理由的分档就是猜", f)
		}
		byTier[e.Tier] = append(byTier[e.Tier], f)
	}
	for k := range byTier {
		sort.Strings(byTier[k])
	}
	t.Logf("消费面分档（域 %d 个文件）：①随v1退役 %d · ②必须迁 %d · ③按设计继续读v1 %d · ④待拍板 %d",
		len(v1ReaderRetirementTiering), len(byTier["①"]), len(byTier["②"]),
		len(byTier["③"]), len(byTier["④"]))

	// ① 逐个点名。这 4 个是「读 v1 的理由就是 v1 自己」，少一个就有一个 DDL/一次性工具
	// 在退役后还留在排期里。
	wantRetireWithV1 := []string{
		"bg/lite_retention_worker.go",
		"cmd/tools/backfill_session_bodies/main.go",
		"cmd/tools/validate_sessions_v2/loader.go",
		"db/db.go",
	}
	assertTierMembers(t, byTier, "①", wantRetireWithV1)

	// ③ 逐个点名。这一档是「按设计继续读 v1」，属主若要改判，必须在这里改，
	// 而不是让某个文件在某次改动里悄悄换档。
	wantKeepReadingV1 := []string{
		"admin/auto_route_correlations.go",
		"admin/credential_monitor.go",
		"admin/credential_monitor_heatmap.go",
		"admin/diagnostics_credential.go",
		"admin/model_routing_diagnostic.go",
		"admin/probe_history.go",
		"admin/provider_diagnose.go",
		"admin/quality_correlations.go",
		"admin/route_incidents.go",
		"admin/top_problems.go",
		"autoroute/recommend_v2.go",
		"bg/candidate_failure_monitor.go",
		"bg/credential_recovery.go",
		"bg/credential_selfcheck.go",
		"bg/daily_probe_audit.go",
		"bg/integrity_fingerprint_drift.go",
		"bg/integrity_fingerprint_probe.go",
		"bg/ledger_reconciliation.go",
		"bg/model_probe.go",
		"bg/model_tier.go",
		"bg/passive_probe_listener.go",
		"bg/today_success_probe.go",
		"cmd/gateway/dual_read_validator.go",
		"db/probe_views_unified.go",
		"domains/providerprofile/adapters.go",
		"domains/routeincident/store.go",
		"domains/streaming/anomaly_harvester.go",
	}
	assertTierMembers(t, byTier, "③", wantKeepReadingV1)

	// ④ 钉住：待拍板这一档只许缩小，不许静默增长。增长意味着有人遇到了新疑点
	// 却没登记 —— 那正是「114 写成 114」那类问题的温床。
	wantUndecided := []string{
		"admin/auto_route_outcome_freshness.go",
		"admin/live_stream_sse.go",
		"cmd/gateway/output_compliance_control.go",
		"cmd/compression-bench/main.go",
		"cmd/scenario_driver/main.go",
		"cmd/traffic-replay/main.go",
		"domains/analysis/optimizer.go",
		"internal/collector/gateway_adapters.go",
	}
	assertTierMembers(t, byTier, "④", wantUndecided)

	// ② 是补集，不点名 —— 但它的规模要与算式对得上，否则前三档被改也会无人察觉
	if got, want := len(byTier["②"]), len(v1ReaderRetirementTiering)-
		len(byTier["①"])-len(byTier["③"])-len(byTier["④"]); got != want {
		t.Errorf("② 档 %d 个，与补集算式 %d 个不符", got, want)
	}
	for _, f := range byTier["②"] {
		for _, g := range append(append([]string{}, byTier["①"]...), byTier["③"]...) {
			if f == g {
				t.Errorf("%s 同时出现在 ② 与 ①/③", f)
			}
		}
	}
}

// assertTierMembers 双向断言：期望集合里每个都在该档，且该档**没有多余**成员。
func assertTierMembers(t *testing.T, byTier map[string][]string, tier string, want []string) {
	t.Helper()
	have := map[string]bool{}
	for _, f := range byTier[tier] {
		have[f] = true
	}
	var missing, extra []string
	for _, f := range want {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	for _, f := range byTier[tier] {
		found := false
		for _, g := range want {
			if f == g {
				found = true
				break
			}
		}
		if !found {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("档位 %s 缺 %d 个成员（应在此档却不在）：%s", tier, len(missing), strings.Join(missing, " "))
	}
	if len(extra) > 0 {
		t.Errorf("档位 %s 多出 %d 个成员（在此档却不在期望集合里；改判要同时改门）：%s",
			tier, len(extra), strings.Join(extra, " "))
	}
}

// TestV1ArmViewSetMatchesRuntimeTruth 把 v1 臂视图集合钉在**运行时真库**对过账的 4 个上。
//
// 这不是抄一份名单：门要回答的是「这个集合会不会悄悄少一个」。
// §9.260 的实测就是少两个 —— 少的方向是把有风险的报成安全。
func TestV1ArmViewSetMatchesRuntimeTruth(t *testing.T) {
	// composer 里必须真的 CREATE VIEW 那两个没有 DDL 文件的视图
	b, err := readFileForEvidence(filepath.Join(repoRootFromCaller(t), "db", "request_logs_view_schema.go"))
	if err != nil {
		t.Fatalf("读 composer 失败：%v", err)
	}
	for _, name := range []string{
		"request_logs_with_current_month_without_customer_id",
		"request_logs_with_current_month_without_request_class_due_at",
	} {
		if !strings.Contains(b, "CREATE VIEW public."+name) {
			t.Errorf("composer 里没有 CREATE VIEW public.%s —— 它只存在于运行时，"+
				"推导源与运行时真库就对不上了（真库 pg_class 实测确有该视图）", name)
		}
	}
	t.Logf("v1 臂视图（与真库 pg_class 对账）%d 个：%s",
		len(v1ArmViewNamesRuntimeChecked), strings.Join(v1ArmViewNamesRuntimeChecked, " "))
}

// TestV1ArmLiteralBlindSpotIsPinnedAndTiered 把那 39 个「工具看不见」的文件钉住，
// 并证明它们**已被本门覆盖**（都在分档域里、都有档位）。
//
// 这道门是 §9.260 那个盲区的收口：盲区本身在工具里补不了
// （它只枚举拼接点），但可以在**总体**这一层补 ——
// 只要这些文件一个都不许漏出门。
func TestV1ArmLiteralBlindSpotIsPinnedAndTiered(t *testing.T) {
	root := repoRootFromCaller(t)

	// ★ 声明的清单必须与**从源码树重算**的结果双向相等。
	// 只做「列表里每个都还成立」是不够的：那是一条只许不变的单向检查，
	// 有人把一个条目删掉，门照样绿 —— 而清单变小的方向恰好是
	// 「少了一个有风险的读方」，也就是最坏的方向。
	declared := map[string]bool{}
	for _, f := range v1ArmLiteralBlindSpot {
		if declared[f] {
			t.Errorf("v1ArmLiteralBlindSpot 里 %s 重复登记", f)
		}
		declared[f] = true
	}

	recomputed := map[string]bool{}
	var badEvidence []string
	for f := range v1ReaderRetirementTiering {
		b, err := readFileForEvidence(filepath.Join(root, f))
		if err != nil {
			badEvidence = append(badEvidence, f+"（读不到文件）")
			continue
		}
		literal, spliced := v1ReadEvidence(b)
		// 盲区定义：读 v1 臂视图（literal 通路），且**没有**拼接 FROM
		// （有拼接就会被拼接点工具看见，那就不是盲区）。
		if readsAnyV1ArmView(b) && spliced == 0 {
			recomputed[f] = true
		}
		_ = literal
	}

	var missing, extra []string
	for f := range recomputed {
		if !declared[f] {
			missing = append(missing, f)
		}
	}
	for _, f := range v1ArmLiteralBlindSpot {
		if !recomputed[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(badEvidence)
	if len(missing) > 0 {
		t.Errorf("以下 %d 个文件现在满足盲区定义（读 v1 臂视图 + 无拼接 FROM）却不在 v1ArmLiteralBlindSpot 里：\n"+
			"  %s\n"+
			"—— 清单变小 = 少了一个有风险的读方，这是最坏的方向，必须显式登记",
			len(missing), strings.Join(missing, " "))
	}
	if len(extra) > 0 {
		t.Errorf("以下 %d 个文件已不再满足盲区定义（可能改指了、或改成了拼接形态因而工具能看见了）：%s",
			len(extra), strings.Join(extra, " "))
	}
	if len(badEvidence) > 0 {
		t.Errorf("读不到源文件：%s", strings.Join(badEvidence, " "))
	}

	// 每一个盲区读方都必须有档位 —— 盲区不是「没人管」的同义词。
	var untiered []string
	for f := range recomputed {
		if _, ok := v1ReaderRetirementTiering[f]; !ok {
			untiered = append(untiered, f)
		}
	}
	sort.Strings(untiered)
	if len(untiered) > 0 {
		t.Errorf("以下 %d 个盲区读方没有分档：%s", len(untiered), strings.Join(untiered, " "))
	}
	t.Logf("拼接点工具看不见的 v1 臂读方 %d 个 —— 工具收尾行「退役读方清单 21 个文件」"+
		"覆盖不到它们；覆盖它们的是 %d 个文件的分档域",
		len(recomputed), len(v1ReaderRetirementTiering))
}

// readFileForEvidence 读文件。取证只需要文本。
func readFileForEvidence(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// fromJoinV1RE 捕获 `FROM <关系名>` / `JOIN <关系名>` 后的**完整标识符**。
//
// ★ 为什么捕获再判、而不是把 v1 表名直接写进正则：
// Go 用的是 RE2，**不支持 lookahead**（`(?![a-z0-9_])` 编译不过），
// 而 `\b` 在这里**不可用** —— `_` 是词字符，所以 `request_logs_bodies_hot`
// 会在 `_bodies` 之后要求一个词边界，而下一个字符是 `_` ⇒ 永不成立
// ⇒ bodies 视图被漏判。首次对账时 4 个 v1 臂视图全部报「不含 v1 基表」，
// 就是这个坑。所以改成「捕获标识符 → 用集合判定」。
var fromJoinV1RE = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:public\.)?([a-z_][a-z0-9_]*)`)

// splicedFromRE 匹配「`FROM` 后面跟的是 Go 表达式」——即关系名是拼进去的。
//
// 这是**间接**读方的形态：文件自己的源码里**没有任何 v1 关系名**，
// 名字由另一个文件的切换层（或结构体字段）送进来。
var splicedFromRE = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:` + "`" + `|"|[a-zA-Z_]\w*(?:\.[a-zA-Z_]\w*)*\s*\+)`)

// quotedV1RelationRE 捕获字符串字面量的内容，用于切换层 / 结构体字段形态
// （`TurnsTable: "request_logs_hot"`、`return "request_logs_with_current_month"`）。
var quotedV1RelationRE = regexp.MustCompile(`"([^"\n]*)"`)

// v1RelationNames 是 v1 族关系的判定集合（**闭合**，不是通配）。
func isV1Relation(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "request_logs", "request_logs_hot", "request_logs_bodies",
		"request_logs_bodies_hot", "request_logs_archive":
		return true
	}
	for _, v := range v1ArmViewNamesRuntimeChecked {
		if strings.EqualFold(strings.TrimSpace(name), v) {
			return true
		}
	}
	return false
}

// firstRelationToken 取 `FROM request_logs_hot r` 里的第一个 token，
// 并剥掉 `AS` 别名。`FROM x, y` 这种逗号列表不在本判据内（v1 读点不会这样写）。
func firstRelationToken(rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 1 && strings.EqualFold(fields[1], "as") {
		return fields[0]
	}
	return fields[0]
}

// stripGoAndSQLComments 剥掉 Go 注释**与** SQL 注释，**两遍**。
//
// ★ 为什么必须两遍、且第二遍要钻进字符串里：
// 本轮两次都栽在这上面。
//
//	第 1 次：把 `'` 当引号 ⇒ SQL 常量 `IN ('success','failure')` 里的单引号
//	        被当字符串开始，一路吃到下一个 `'` ⇒ 半条 SQL 当注释删掉。
//	第 2 次（更隐蔽）：把 Go 原始字符串（反引号）当**不透明整体**跳过，
//	        于是字符串**内部**的 SQL 注释活了下来 ——
//	        `domains/streaming/model_alternatives.go` 的第 230 行
//	        `-- request_logs_with_current_month is a UNION of …` 被当成真读点，
//	        而它真正的读点是第 244 行的 `FROM request_logs_hot`（**基表**，不是臂视图）。
//	        那个文件正是 `request_logs_reader_population_test.go` 举的反例，
//	        原因一字不差。
//
// 做法：第 1 遍只剥 Go 注释（**保留**字符串内容）；第 2 遍对整体剥 SQL 注释 ——
// 此时字符串外的 `--` 已经是注释，Go 代码里不存在合法的 `--`，
// 所以这一遍不会误伤。
func stripGoAndSQLComments(src string) string {
	return stripSQLComments(stripGoCommentsKeepingStrings(src))
}

// stripGoCommentsKeepingStrings 剥 Go 的 `//` 与 `/* */`，**保留**字符串字面量内容。
//
// ⚠ 刻意不直接用 `session_view_dependency_risk_test.go` 里的 `stripGoComments`：
// 那是一条裸正则（`//[^\n]*`），**不看字符串** ⇒ 会把 SQL 里的
// `http://` 与原始字符串里的内容一起当注释删掉。本门要的是
// 「剥 Go 注释、但 SQL 字符串原样留下，好让第二遍去剥 SQL 注释」。
func stripGoCommentsKeepingStrings(src string) string {
	var b strings.Builder
	rs := []rune(src)
	n := len(rs)
	for i := 0; i < n; {
		c := rs[i]
		switch {
		case c == '"' || c == '`':
			q := c
			b.WriteRune(c)
			i++
			for i < n {
				if rs[i] == '\\' && q != '`' && i+1 < n {
					b.WriteRune(rs[i])
					b.WriteRune(rs[i+1])
					i += 2
					continue
				}
				b.WriteRune(rs[i])
				if rs[i] == q {
					i++
					break
				}
				i++
			}
		case c == '/' && i+1 < n && rs[i+1] == '/':
			for i < n && rs[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && rs[i+1] == '*':
			j := i + 2
			for j+1 < n && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			if j+1 < n {
				i = j + 2
			} else {
				i = n
			}
		default:
			b.WriteRune(c)
			i++
		}
	}
	return b.String()
}

// v1ReadEvidence 返回两条独立通路的计数：
//
//	literal —— 源码里**指名**了 v1 关系（FROM/JOIN 字面量，或只含 v1 关系名的字符串字面量）
//	spliced —— `FROM` 后面跟的是 Go 表达式（关系名从别的文件拼进来）
//
// ★ 为什么必须分成两条：这 115 个文件里有 6 个**自己源码里一个 v1 关系名都没有**
// （admin/dashboard_board_queries.go · session_compare.go · session_export.go ·
// bg/auto_route_settle_sql.go · maas/consumption_detail.go · maas/credit_buckets.go）——
// 名字全在切换层那一份文件里。合并成一个数会把它们判成「没读 v1」，
// 那就是**把有风险的报成安全**，与本门要防的方向相反。
func v1ReadEvidence(src string) (literal, spliced int) {
	stripped := stripGoAndSQLComments(src)
	for _, m := range fromJoinV1RE.FindAllStringSubmatch(stripped, -1) {
		if isV1Relation(firstRelationToken(m[1])) {
			literal++
		}
	}
	for _, m := range quotedV1RelationRE.FindAllStringSubmatch(stripped, -1) {
		if isV1Relation(firstRelationToken(m[1])) {
			literal++
		}
	}
	return literal, len(splicedFromRE.FindAllString(stripped, -1))
}

// readsAnyV1ArmView 判断源码里是否读到了 4 个 v1 臂视图之一。
func readsAnyV1ArmView(src string) bool {
	stripped := stripGoAndSQLComments(src)
	for _, name := range v1ArmViewNamesRuntimeChecked {
		if strings.Contains(stripped, name) {
			return true
		}
	}
	return false
}
