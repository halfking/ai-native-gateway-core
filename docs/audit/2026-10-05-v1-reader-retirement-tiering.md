# v1 读方消费面分档（116 个文件）—— 待你拍板

> 生成时间：2026-10-06　起点提交：`f654124c1`（= `origin/main`）
> 门：`admin/request_logs_retirement_tiering_test.go`（5 道，常驻）
> ⚠ **2026-10-06 更新（§9.265）**：总体已从 **115 变 116**、③ 档从 **27 变 28** ——
> §9.264 新增的 `cmd/gateway/v1_write_liveness.go` 是**生产文件**且含两处
> `FROM request_logs…`，`TestRequestLogsReadInventoryIsComplete` 正确地把它抓了出来
> （字面量总体 107 → **108**）。本文所有数已同步；两处算式现在写的是 **108 + 22 − 14 = 116**。
> 背景：`docs/audit/2026-10-04-session-migration-decision-sheet.md`（D32 段 §9.259 / §9.260）

**本表只出表，不改指任何读方。** ② 档的迁移动作是属主的决定，不是本文档的。

---

## 零、一句话版

退役读方的总体是 **116 个文件，不是 114**（§9.265 又加了 1 个）；按消费面分档为
**① 随 v1 退役 4 · ② 必须迁 76 · ③ 按设计继续读 v1 28 · ④ 待拍板 8**。
其中 **39 个读 v1 臂视图的文件对拼接点工具完全不可见**（§9.260 登记的是 3 个）。

---

## 一、总体：116，不是 114

§9.259 写下的「并集 114 文件」，公式与同一段的那份手写清单**用的是两个不同的总体**：

| 位置 | 用的总体 | 成员 |
|---|---|---|
| 公式 `107 + 21 − 14 = 114` | 拼接点工具的 `v1 ∪ v1臂` 桶（**21**） | 含 `admin/dashboard_board_queries.go`，**不含** `bg/auto_route_settle_sql.go` |
| 「7 个不在字面量表里」清单 | 另一个总体 | 含 `bg/auto_route_settle_sql.go`，**不含** `admin/dashboard_board_queries.go` |

两个总体**各 7 个**、总数差 1，**没有任何一道门会红**。

真因：`admin/dashboard_board_queries.go` 直到 **§9.260 把 composer 建的视图补进推导源**
才变成 v1 读方（`admin/board_time_range.go:136-138` 返回
`request_logs_with_current_month_without_customer_id AS r`），
而 §9.260 只更新了「处数 37→38 / 文件 20→21」，**没回头重算 §9.259 的并集**。

**实测（`f654124c1`；§9.265 后字面量那一行为 108）：**

| 总体 | 规模 | 来源 |
|---|---:|---|
| 字面量读方 | **108 文件 / 256 调用点** | `requestLogsReadInventory`（门输出实测） |
| 间接读方 | **22 文件** | 拼接点工具 21 + §9.258 表里 3 处手验所在的 `bg/auto_route_settle_sql.go` |
| 交集 | **14** | — |
| ★ **并集** | **116** | 108 + 22 − 14 |

只在间接总体、不在字面量总体的 **8 个**（§9.259 手写的是 7 个）：
`admin/dashboard_board_queries.go` · `admin/session_compare.go` · `admin/session_export.go` ·
`admin/usage_credits.go` · `bg/auto_route_settle_sql.go` · `maas/consumption_detail.go` ·
`maas/credit_buckets.go` · `maas/usage.go`

⇒ **退役排期按 116 个文件。** 门 `TestV1ReaderTieringDomainIsBothPopulations` 把
两个总体各自的成员钉住，并对 108 / 22 / 14 / 116 四个数逐个断言 ——
**并集算式与域实测不相等时，它会明确打印「★ 并集算式 116 ≠ 域实测 115」**。

---

## 二、消费面分档

判据：**v1 退役后，这个读点的消费者还在不在。**

- **① 随 v1 一起退役** —— 读 v1 的唯一存在理由就是 v1 自身（生命周期 DDL、一次性迁移/校验工具）
- **② 必须迁** —— 消费者在 v1 退役后仍存在（对外 API / 对外计费 / 聚合 / 会话读取）
- **③ 按设计继续读 v1** —— 消费者**就是**「观测 v1 / 凭证 / 节点」本身，没有外部契约依赖它
- **④ 待拍板** —— 判据不唯一，疑点写在「理由」列

> 「机制」列：`字面量` = 在 `requestLogsReadInventory` 里；`间接` = 走切换层/结构体字段；
> `读v1臂视图` = 读到 §9.260 那 4 个视图之一；`★工具盲区` = 见第四节。

### ① 随 v1 一起退役（4）

| 文件 | 机制 | 理由 |
|---|---|---|
| `bg/lite_retention_worker.go` | 字面量 | lite 模式行级 TTL：v1 那条 DELETE 腿随 v1 退役；**文件本身不退役**（同函数还清 sessions/session_turns） |
| `cmd/tools/backfill_session_bodies/main.go` | 间接 | 一次性 bodies 回填工具：源就是 v1 bodies（matryoshka），V2 上线前置，跑完即退役 |
| `cmd/tools/validate_sessions_v2/loader.go` | 字面量 | 一次性 v1↔v2 对账工具：LoadV1Turns 读 request_logs_bodies，工具跑完即退役（cmd/tools/validate_sessions_v2） |
| `db/db.go` | 字面量/DDL | v1 生命周期 DDL：12 处 CREATE/ALTER/DROP request_logs*，读的不是数据而是 v1 这张表本身（db/db.go） |
### ② 必须迁（76）

| 文件 | 机制 | 理由 |
|---|---|---|
| `admin/analytics.go` | 字面量 | 对外 API：session-analytics 端点，响应直接含 v1 统计 |
| `admin/attachments_routes.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：附件归属校验，退役后仍要判归属 |
| `admin/attempt_quality_api.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：尝试质量指标（读 v1 臂视图，§9.260 补进来的） |
| `admin/auto_route.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：auto-route 决策/关联 |
| `admin/auto_title_generator.go` | 间接/读v1臂视图 | 对外功能：会话标题生成读 v1 首条成功轮次 |
| `admin/body_resolver.go` | 字面量/读v1臂视图/★工具盲区 | 对外功能：请求体解析（bodies），退役后详情页仍要显示 |
| `admin/compression_sessions.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：压缩会话端点 |
| `admin/compression_stats.go` | 间接/读v1臂视图 | 对外 API：压缩统计端点 |
| `admin/credential_success_rate.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：凭证成功率。★ 注意：它同时是监控面，但走 HTTP 响应，退役后端点仍需有数 |
| `admin/dashboard_board_queries.go` | 间接 | 对外 API：看板首屏聚合（读 composer 建的 v1 臂视图） |
| `admin/data_lifecycle.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API + **对 v1 有写**：数据生命周期清理/预览，退役后清理对象要改 |
| `admin/data_lifecycle_attachments.go` | 字面量 | 对外 API：附件清理 |
| `admin/data_lifecycle_blobs.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API + 对 v1 有写：blob 清理 |
| `admin/data_lifecycle_metrics.go` | 字面量 | 对外 API：生命周期指标 |
| `admin/logs.go` | 间接/读v1臂视图 | 对外 API：日志列表/详情/统计（读 v1 基表） |
| `admin/logs_summary.go` | 间接/读v1臂视图 | 对外功能：会话日志摘要（bodies 读方） |
| `admin/memora_handlers.go` | 间接/读v1臂视图 | 对外 API：memora 会话/上下文端点 |
| `admin/model_status.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：模型状态 |
| `admin/no_topic_session.go` | 间接/读v1臂视图 | 对外 API：无主题会话端点 |
| `admin/provider_models.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：provider 下的模型与用量（§9.49 动态列盲区形态） |
| `admin/providers.go` | 字面量 | 对外 API：provider 列表 |
| `admin/request_trace.go` | 字面量 | 对外 API：请求链路 |
| `admin/routing.go` | 字面量 | 对外 API：路由管理（含诊断路由） |
| `admin/session_analytics_breakdown.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：会话分析分组 |
| `admin/session_analytics_timeseries.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：会话分析时序 |
| `admin/session_bodies_batch.go` | 字面量/读v1臂视图/★工具盲区 | 对外功能：bodies 批量读（经切换层 SessionBodiesSourceSQL） |
| `admin/session_compare.go` | 间接 | 对外 API：会话对比（间接读方，§9.259 点名的 7 个之一） |
| `admin/session_detail_v2.go` | 字面量 | 对外 API：会话详情 v2 |
| `admin/session_export.go` | 间接 | 对外 API：会话导出（★ FROM 读会话族 LEFT JOIN bodies，退役总体是 session_turns 不是 v1） |
| `admin/session_extract.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：会话抽取 |
| `admin/session_management_api.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：会话管理 |
| `admin/session_online.go` | 字面量/读v1臂视图 | 对外 API：在线会话时间线 |
| `admin/session_sanitize_matches.go` | 间接/读v1臂视图 | 对外功能：脱敏匹配（bodies 读方） |
| `admin/session_summary_v2.go` | 字面量/读v1臂视图/★工具盲区 | 对外功能：会话摘要 v2（含 v1 回退查询） |
| `admin/session_tenant.go` | 字面量 | 对外功能：会话租户判定 |
| `admin/session_timeline_query.go` | 字面量/读v1臂视图/★工具盲区 | 对外功能：会话时间线查询 |
| `admin/session_title.go` | 间接/读v1臂视图 | 对外功能：任务标题生成 |
| `admin/session_turns_tree.go` | 字面量/读v1臂视图 | 对外 API：会话轮次树 |
| `admin/session_turns_unified.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：统一轮次读端 |
| `admin/swim_lane_init.go` | 字面量 | 对外 API：看板泳道初始化 |
| `admin/telemetry.go` | 字面量 | 对外 API + **对 v1 有写**：`/api/telemetry/request-log` 是 v1 写入端点之一 |
| `admin/tenants.go` | 间接/读v1臂视图 | 对外 API：租户列表与统计 |
| `admin/unified_detail.go` | 字面量/读v1臂视图/★工具盲区 | 对外功能：统一详情（bodies 读方） |
| `admin/usage.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：用量（**含 `/api/maas/usage/summary` 对外计费面**） |
| `admin/usage_credits.go` | 间接 | 对外 API + 切换层：积分用量，requestLogsFromClause 切换层所在地 |
| `admin/usage_enhanced.go` | 字面量/读v1臂视图 | 对外 API：用量增强（planCostTrend 切换层） |
| `admin/usage_trend_series.go` | 字面量/读v1臂视图/★工具盲区 | 对外 API：用量趋势时序（usageTrendSource 切换层；★ §9.260 点名的 3 个字面量盲区之一） |
| `admin/work_types.go` | 字面量 | 对外 API：工作类型统计 |
| `bg/auto_index_refresher.go` | 字面量 | 后台：自动建索引刷新器（按 v1 表规模决策，退役后要改判对象） |
| `bg/auto_route_affinity_worker.go` | 字面量 | 后台：任务→模型亲和度学习（从已结算选择学，读 v1） |
| `bg/auto_route_settle_sql.go` | 间接 | ★ 后台结算：`settleSourceFor` 写门开＝v1_hot、写门关＝session_turns_hot，同一段 SQL 两族切换 |
| `bg/shared_pick.go` | 字面量/读v1臂视图/★工具盲区 | 后台：探针选型共享逻辑（PickProbeModelForCredential） |
| `bg/stats_minute_rollup.go` | 字面量/读v1臂视图/★工具盲区 | 后台：分钟级聚合 **写入 request_stats_minute**（聚合表退役后仍要算，读源必须改） |
| `bg/stats_minute_rollup_retire.go` | 字面量/读v1臂视图/★工具盲区 | 后台：删除聚合表里视图不再产出的键 —— **NOT EXISTS 守卫读 v1 臂视图**，退役后守卫要改指 |
| `cmd/gateway/main_v3_wiring.go` | 字面量 | 进程装配：V3 接线引用 v1 读点 |
| `cmd/gateway/waterfall_by_request.go` | 字面量 | 对外功能：按请求的 waterfall |
| `cmd/gateway/waterfall_db.go` | 字面量 | 对外功能：waterfall DB 侧 |
| `discovery/discovery.go` | 字面量 | 对外功能：模型自动发现 |
| `domains/analysis/request_summary.go` | 字面量 | 分析域：请求摘要产出 |
| `domains/attachments/handler.go` | 字面量/读v1臂视图/★工具盲区 | 对外功能：附件按请求列出（ListByRequest） |
| `domains/credentialstate/popularity_tracker.go` | 字面量 | 后台：模型热度统计 |
| `domains/hooks/goal/history_store.go` | 字面量 | 对外功能：目标历史按会话取（FetchBySession） |
| `domains/hooks/observability/telemetry/client.go` | 字面量 | ★ 对 v1 有写（UPDATE）+ 读：telemetry 落库 |
| `domains/sessionforensics/export.go` | 间接/读v1臂视图 | 对外功能：会话取证导出 |
| `domains/sessionsummary/summarizer.go` | 间接/读v1臂视图 | 对外功能：会话摘要（GetMessagesSince 等） |
| `domains/sessionsummary/system_prompt_prefix.go` | 间接/读v1臂视图 | 对外功能：系统提示前缀 |
| `domains/streaming/model_alternatives.go` | 字面量 | 对外功能：模型备选（读 v1_hot 热度） |
| `internal/quality/minute_aggregator.go` | 字面量 | 后台：分钟质量聚合 |
| `internal/summarystore/store.go` | 字面量 | 对外功能：会话摘要持久化 |
| `internal/trace/trace.go` | 字面量 | ★ 对 v1 有写 + 读：链路追踪 FlushToPG |
| `maas/consumption_detail.go` | 间接 | ★ 对外计费 API：消费明细 |
| `maas/credit_buckets.go` | 间接 | ★ 对外计费 API：积分桶 |
| `maas/usage.go` | 间接 | ★ 对外计费 API：requestLogsSource 切换层所在地，退役后计费读数不能少 |
| `storage/sqlite/request_log_store.go` | 字面量 | ★ 对 v1 有写 + 读：lite 模式 SQLite 存储（WriteRequest/GetRequest） |
| `tests/session_audit/cmd/audit-test/main.go` | 字面量 | 测试工具（build tag tools）：会话审计 |
| `tests/test_popularity_tracker.go` | 字面量 | 测试：热度统计 |
### ③ 按设计继续读 v1（28）

这一档是本表**最需要属主确认**的一档：它的判据是「这个读的消费者是不是观测本身」。
若属主认为这些观测面在 v1 退役后**没有存在意义**，整档 28 个都要按 ① 处理。

| 文件 | 机制 | 理由 |
|---|---|---|
| `admin/auto_route_correlations.go` | 字面量/读v1臂视图/★工具盲区 | auto-route 关联分析：观测面 |
| `admin/credential_monitor.go` | 字面量/读v1臂视图/★工具盲区 | 凭证监控摘要（summaryCache + buildMonitorSummarySQL）：观测面，读 v1 是被观测对象 |
| `admin/credential_monitor_heatmap.go` | 字面量/读v1臂视图/★工具盲区 | 凭证健康热力图：同上，运维观测面 |
| `admin/diagnostics_credential.go` | 字面量 | 凭证诊断端点 handleCredentialDiagnostic：诊断面 |
| `admin/model_routing_diagnostic.go` | 字面量/读v1臂视图/★工具盲区 | 模型路由诊断端点：诊断面 |
| `admin/probe_history.go` | 字面量/读v1臂视图/★工具盲区 | probe-run 历史端点：这个读的**目的**就是看 v1 探针跑过什么 |
| `admin/provider_diagnose.go` | 字面量 | provider 诊断（从 provider_probe.go 拆出）：诊断面 |
| `admin/quality_correlations.go` | 字面量 | 请求级质量关联分析：观测面 |
| `admin/route_incidents.go` | 字面量/读v1臂视图/★工具盲区 | 路由事故（route_incidents）：观测面 |
| `admin/top_problems.go` | 字面量/读v1臂视图/★工具盲区 | top-problems 报告：观测面 |
| `autoroute/recommend_v2.go` | 字面量 | 推荐索引（读 v1 纠错分）：自检面 |
| `bg/candidate_failure_monitor.go` | 字面量/读v1臂视图/★工具盲区 | 候选失败监控：探针面 |
| `bg/credential_recovery.go` | 字面量 | 凭证恢复（探针驱动）：观测→动作，探针面 |
| `bg/credential_selfcheck.go` | 字面量 | 凭证自检：读 v1 是被自检对象 |
| `bg/daily_probe_audit.go` | 字面量/读v1臂视图/★工具盲区 | 每日探针审计：探针面 |
| `bg/integrity_fingerprint_drift.go` | 字面量/读v1臂视图/★工具盲区 | 指纹漂移：探针面 |
| `bg/integrity_fingerprint_probe.go` | 字面量 | 指纹探针：探针面 |
| `bg/ledger_reconciliation.go` | 字面量 | 账本对账（usage_ledger↔credit_ledger）：对账面 |
| `bg/model_probe.go` | 字面量 | 模型探针：探针面 |
| `bg/model_tier.go` | 字面量 | 常用模型分级「用于自检」：自检面 |
| `bg/passive_probe_listener.go` | 间接/读v1臂视图 | 被动观测监听器（Layer 5 passive observer）：观测面 |
| `bg/today_success_probe.go` | 字面量 | 当日成功探针：探针面 |
| `cmd/gateway/dual_read_validator.go` | 字面量 | 双读校验器：存在的意义就是校验 v1 与 v2 读数一致 |
| `db/probe_views_unified.go` | 字面量/读v1臂视图/★工具盲区 | 节点状态派生 SSOT（SQL 片段）：探针/节点面 |
| `domains/providerprofile/adapters.go` | 字面量 | provider 网络探针/请求分析适配器：探针面 |
| `domains/routeincident/store.go` | 字面量/读v1臂视图/★工具盲区 | 路由事故 24h 时间线 store：观测面 |
| `domains/streaming/anomaly_harvester.go` | 字面量 | 异常收集器：观测面 |
### ④ 待拍板（8）

**这 8 个是本表明确不猜的部分。** 每个都写清了「两种判据各自成立的理由」，
请逐条给方向；门把它们钉住，改判要同时改门（不会静默换档）。

| 文件 | 机制 | 理由（两种判据各自为什么成立） |
|---|---|---|
| `admin/auto_route_outcome_freshness.go` | 字面量 | auto-route 结果新鲜度 queryOutcomeFreshness：是结算正确性门（②）还是自检（③），取决于它卡不卡结算 |
| `admin/live_stream_sse.go` | 字面量/读v1臂视图/★工具盲区 | SSE 实时流 replay/terminalStatusesFromDB：退役后是否继续供流式详情，需属主定 |
| `cmd/compression-bench/main.go` | 字面量 | scenario_driver / traffic-replay / compression-bench：压测与回放工具，读 v1 形态随工具定位 |
| `cmd/gateway/output_compliance_control.go` | 字面量 | output 出口合规控制：读 v1 判合规，合规门在退役后是否还有对象，需属主定 |
| `cmd/scenario_driver/main.go` | 字面量 | scenario_driver / traffic-replay / compression-bench：压测与回放工具，读 v1 形态随工具定位 |
| `cmd/traffic-replay/main.go` | 字面量 | scenario_driver / traffic-replay / compression-bench：压测与回放工具，读 v1 形态随工具定位 |
| `domains/analysis/optimizer.go` | 字面量 | 优化建议器 loadStats 读 v1 统计：建议是「观测」还是「面向用户的产出」，两侧都讲得通 |
| `internal/collector/gateway_adapters.go` | 字面量/读v1臂视图/★工具盲区 | collector 的 PG 读适配器：Snapshot/DatabaseSizeMB 读 v1，但是给通用 collector 用还是给探针用，需属主定 |

---

## 三、③ 档的判据，以及我做过的反向检查

**正向**：这 28 个文件的读点，其消费者是探针 / 自检 / 巡检 / 事故 / 对账。
判据不是文件名里有没有 `probe` —— 那只是线索。

**反向检查（本轮实际做了，结果记在这里）**：我先按「诊断性质」挑了一批
**名字里没有 probe** 的候选逐个读，确认它们属于同一族：
`admin/credential_monitor.go`（凭证监控摘要）· `admin/credential_monitor_heatmap.go` ·
`admin/diagnostics_credential.go` · `admin/model_routing_diagnostic.go` ·
`admin/provider_diagnose.go`（从 `provider_probe.go` 拆出）· `admin/quality_correlations.go` ·
`admin/route_incidents.go` + `domains/routeincident/store.go` · `bg/model_tier.go`
（注释写「用于自检分级」）· `autoroute/recommend_v2.go` · `domains/streaming/anomaly_harvester.go` ·
`bg/ledger_reconciliation.go` · `domains/providerprofile/adapters.go` ·
`cmd/gateway/dual_read_validator.go`（存在意义就是校验 v1 与 v2 读数一致）。

⇒ 只靠 `probe` 词表会漏掉其中 12 个。门里 ③ 档是**逐个点名**的，不是计数。

⚠ **两个 ③ 档成员我给的是「按设计继续读 v1」，但它们同时也是对外 HTTP 响应**，
判据在两侧都成立，属主可能更倾向 ②：
- `admin/credential_success_rate.go`（我放在 ②，因为端点退役后仍需有数）
- `admin/top_problems.go` / `admin/route_incidents.go`（报告页面，退役后观测对象消失）

---

## 四、工具盲区：39 个，不是 3 个

`cmd/tools/sql_source_indirection_audit` 只枚举 `+` 拼接点。
**关系名是字面量的 `FROM request_logs_with_current_month …` 不产生任何站点** ——
它不是「被判成 canonical」，是**连站点都没有**。

| | 文件数 |
|---|---:|
| 116 个读 v1 的文件里，读到 v1 臂视图的 | **55** |
| 其中在拼接点工具输出里**完全不出现**的 | **39** |
| 拼接点工具输出覆盖的文件 | 21 |

⇒ 工具收尾那行「★ 退役读方清单 = v1 基表 12 处 + v1 臂视图 26 处 = 38 处 / 21 个文件」
**不是退役读方清单** —— 116 个里它只覆盖 21 个。
§9.260 点名的 3 个（`attempt_quality_api` · `usage_trend_series` · `auto_route_correlations`）
只是这 39 个里的三个。

覆盖它们的是本表的 116 文件域，门 `TestV1ArmLiteralBlindSpotIsPinnedAndTiered`
把 39 个**从源码树双向重算**并与声明清单比对（只做单向检查的话，
删掉一个条目门照样绿，而「清单变小」正是少了一个有风险读方的方向）。

**v1 臂视图集合与运行时真库对账**（`llm-gateway-pg`，`pg_class`，`relkind='v'`）：

| 视图 | 运行时引用的关系 |
|---|---|
| `request_logs_bodies_with_current_month` | `request_logs_bodies`, `request_logs_bodies_hot` |
| `request_logs_with_current_month` | `request_logs`, `request_logs_hot`, `session_turns(_hot)`, `session_turn_details(_hot)` |
| `request_logs_with_current_month_without_customer_id` | `request_logs`, `request_logs_hot` |
| `request_logs_with_current_month_without_request_class_due_at` | `request_logs`, `request_logs_hot` |

4 个**全部**含 v1 臂，与仓库推导源（`sql/objects/views/` + composer）一致。
后两个**没有仓库 DDL 文件**（composer 运行时建），漏掉它们的方向是把有风险的报成安全。

---

## 五、机制桶 1 / 6 / 97 / 10：**未能复现，且它不是稳定事实**

§9.259 登记的机制桶是 `E_DDL 1 · C_读写 6 · A_只读 97 · Z_切换层 10 = 114`。
**这四个数在仓库里没有定义来源**（无代码、无门、无文档判据），本轮两次推导得到不同结果：

| 推导方式 | E_DDL | C_读写 | A_只读 | Z_切换层 | 合计 |
|---|---:|---:|---:|---:|---:|
| 朴素文本匹配 | 2 | 8 | 105 | — | 115 |
| 剥 Go/SQL 注释后 | 1 | **13** | 96 | **5** | 115 |
| §9.259 登记 | 1 | **6** | 97 | **10** | **114** |

> ⚠ **本表三个「合计」不是消费面域，别拿它和 116 对账**（§9.265 补记）：
> 机制桶按**机制角色**切，消费面域按**文件**切，两者的分母不是一个总体
> —— 同一个文件可落进机制桶也可落进消费面档，反之亦然。
> 上表的 114/115 是 §9.259–§9.262 当时的探针快照，**§9.265 的 115 → 116 不同步到这张表**。

差异集中在 `C_读写` 与 `Z_切换层`：

- `C_读写`：我用「`INSERT/UPDATE/DELETE` 后 400 字符窗口内出现 v1 关系名」，
  `discovery/discovery.go` 的 `INSERT INTO models_canonical` 与
  `domains/streaming/anomaly_harvester.go` 的 `DELETE FROM response_format_anomalies`
  **都被误判成 v1 写**（它们附近的 v1 出现是**读**）⇒ 6 被放大到 13。
- `Z_切换层`：判据必须是「函数**返回值**只有一个关系名」，
  而不是「函数体里出现过 v1 关系名」——后者会把 59 个返回整条 SQL 的 handler 全算进来
  （本轮第一次尝试就是这样，59 个 vs 5 个）。

⇒ **结论：`1/6/97/10` 是一组判据依赖的数，不是可复现的事实，且没有任何门钉住判据。**
若属主要保留机制维度，需要**先写下判据、再让门断言成员**；
本轮**没有**产出第二组机制数，因为那只会把一个不可复现的数换成另一个。

**唯一可核的机制事实**：对 v1 关系有 `CREATE/ALTER/DROP TABLE` 的只有 **1 个文件** ——
`db/db.go`（12 处）。这一条两次推导一致。

---

## 六、要属主拍的板

1. **③ 档 28 个**：整体留在「按设计继续读 v1」，还是随 v1 一起退役？
   若整体退役，探针/自检/巡检面要另找观测对象 —— 这是产品决定。
2. **④ 档 8 个**：逐条给方向（每条的两种判据都写在表里）。
3. **② 档 76 个**：是否认可「对外 API / 对外计费 / 聚合 / 会话读取」这四类消费者
   必须迁。其中 **`maas/` 3 个 + `admin/usage.go` 的 `/api/maas/usage/summary`
   是对外计费面**，退役前后计费读数不能少。
4. **`admin/credential_success_rate.go` 等 3 个既是观测又是 HTTP 响应的**：归 ② 还是 ③。
5. **机制维度要不要保留**：若要，请一并给出判据（见第五节）。

---

## 七、门（5 道，常驻）

| 门 | 断言什么 | 变异检验 |
|---|---|---|
| `TestV1ReaderTieringDomainIsBothPopulations` | 域 == 字面量 ∪ 间接（双向）；108/22/14/116 四个数 + 并集算式 | 删一个分档条目 → 红 |
| `TestV1ReaderTieringEveryFileProvenToReadV1` | 每个文件在当前源码里取得到 v1 读点证据；**带 4 组对照**（注释、字面量、拼接、会话族） | 剥注释退化 / 删运行时视图名 → 红 |
| `TestV1ReaderTiersAssertMembers` | ①③④ **逐个点名**双向断言；每条 Reason 非空 | 把 `probe_history` ③→② → 红 |
| `TestV1ArmViewSetMatchesRuntimeTruth` | composer 真的 `CREATE VIEW` 那两个无 DDL 文件的视图 | — |
| `TestV1ArmLiteralBlindSpotIsPinnedAndTiered` | 39 个盲区**从源码树双向重算**；每个都有档位 | 删一个盲区条目 → 红 |

7 个变异全部按预期让对应门变红（M1 精确打印出「并集算式 116 ≠ 域实测 115」（2026-10-06 rebase 后**重测**确认））。

⚠ **本轮我自己的探针错了三次，都被门抓住**（记录在此，因为它们是可复现的坑）：

1. `` 在 `request_logs_bodies_hot` 的 `_` 前**不成立**（`_` 是词字符）
   ⇒ 首次对账时 4 个 v1 臂视图全部报「不含 v1 基表」。Go 用 RE2，**不支持 lookahead**，
   所以只能「捕获完整标识符 + 集合判定」，不能把表名写进正则。
2. 剥注释器把 `'` 当引号 ⇒ SQL 常量 `IN ('success','failure')` 里的单引号
   被当字符串开始，半条 SQL 当注释删掉。
3. 剥注释器把 Go 原始字符串当**不透明整体** ⇒ 字符串**内**的 SQL 注释活了下来，
   `domains/streaming/model_alternatives.go:230` 的
   `-- request_logs_with_current_month is a UNION of …` 被当成真读点，
   而它真正的读点是 `:244` 的 `FROM request_logs_hot`（基表，不是臂视图）。
   **那个文件正是 `request_logs_reader_population_test.go` 举的反例。**
   ⇒ 改成两遍：先剥 Go 注释（保留字符串），再剥 SQL 注释。

---

## 八、限度与未验项

- **本表没有改指任何读方，也没有跑任何回填**，② 档的迁移动作全部待属主决定。
- **③ 档的 28 个是「按当前语义判的」**：`domains/streaming/anomaly_harvester.go` 与
  `domains/analysis/optimizer.go` 的「观测 vs 面向用户产出」边界最模糊，
  后者已放进 ④，前者按「异常收集＝观测」放在 ③，**若你认为不对请指出**。
- **`bg/stats_minute_rollup_retire.go` 的 ② 归类有一处需要留意**：它 `DELETE FROM request_stats_minute`
  而 `NOT EXISTS` 守卫读 v1 臂视图 —— **聚合表退役后仍要算，守卫必须改指**，
  它不是「随 v1 退役」。
- **生产 252 未做任何查询**，本轮全部数据来自本地 `llm-gateway-pg` 与源码树。
- **`cmd/tools/sql_source_indirection_audit` 本身的收尾行仍写着「退役读方清单」**，
  本轮**没有改它的输出文案**（那会动另一个工具的契约）。正确做法是让它显式声明
  「本工具只覆盖拼接点那一半」，或直接去掉那个措辞。**这一条留给属主定。**

---

## 九、门基线（2026-10-06 实跑，**带 HEAD**）

⚠ **报基线必须带 HEAD sha。** 本轮同一个 admin 全量门在一天内出现过三个不同的数，
**每一次在当时那棵树上都准确**，不可比的是它们之间：

| HEAD | admin FAIL | db FAIL | 变化 |
|---|---:|---:|---|
| `62e866ccc` | **6** | 3 | — |
| `496a27314` | **7** | 3 | `TestDegradePayloadsCarryMarker`：zcode `d58a3c504` 改 `bg/routing_health_checks.go` 1078 行，带进 2 个未登记的降级站点 |
| `b0bd6616c` | **6** | 3 | 上一条被 `b56f454cf` 登记豁免理由后转绿 |
| `3e9893045` | **7** | **2** | admin 那条**换个文件又红了**（见下）；db 的 `TestRetirementBlockedByUnrunBackfills` 转绿 |

⚠ **22 小时内 admin 走过 6 → 7 → 6 → 7，而 db 是 3 → 2。**
`TestDegradePayloadsCarryMarker` 这一条尤其能说明问题：它红的**不是同一个原因两次**。
`b56f454cf` 登记的豁免**至今仍在文件里**（`grep -c routing_health_checks.go admin/degrade_marker_test.go` = 1），
而 `3e9893045` 上它报的是**另一个文件**：

```
1 个降级站点返回 200 + 空载荷却没有 degraded 标记：bg/modality_verification.go:492
```

即 zcode `d58a3c504`（modality 探针闸）+ `c6c78773e`（给 `bg/modality_verification.go`
**加了 131 行**新功能）带进来的新站点。⇒ **那不是「豁免失效」，是又来一个新的。**
本审计的 8 个提交**一次都没碰过** `admin/degrade_marker_test.go` 或 `bg/modality_verification.go`
（逐个 `git show --name-only` 核过）⇒ 7 条**全部非本轮引入**。

★ 我曾把 `62e866ccc` 上的 6 当成最终基线报出「零新增」，那在最终 HEAD 上不成立 ——
`git merge-base --is-ancestor d58a3c504 62e866ccc` ⇒ **否**，那个提交当时还没进树。
**base 之间不可比，且远端一天能往返两轮。**

**`3e9893045` 上的权威基线（逐条列名）**

- **admin FAIL 7**：`TestColumnarParentTwoSurfaceSetopShape_RealDB` ·
  `TestDegradePayloadsCarryMarker` · `TestReportRollup_HTTPContract`（含子测试
  `credential_/_key_视角…`）· `TestV1BodiesReadersAreAssessed`（**故意红**的基线门）·
  `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable` ·
  `TestSessionFinalSuccessBacklogIsClosed` · `TestProjectTasksSkipsNullTaskID`
- **db FAIL 2**：`TestRepointValueFidelity` · `TestSessionFamilyColumnAvailability_FillRates`
  （`TestRetirementBlockedByUnrunBackfills` 已转绿）
- 本审计的 5 道 §9.262 门 + 4 道 §9.265 门：`go vet ./admin/ rc=0`，
  分档表在远端树上仍为 **116 条 / ①4 ②76 ③28 ④8**、③ 名单 **28 项字面量 28 个唯一（无重复）**、
  两个相关文件都归 ③ 且 **Reason 非空**、门断言仍是 `108 / 22 / 14 / 116` ⇒
  **上一轮修的三处没有被后来的 115 个提交改回去。**

### ★ db 第 3 条的真正触发点（我上一轮定位不准，此处更正）

我上一轮说它是「纯数据漂移」。**漂移是原因，但门红在「登记 ↔ 实测的集合对账」上**，
触发点是 `db/retirement_column_exposure.go` 的 `RetirementUnservableColumns`
（该清单注释明写「Measured 2026-10-04 on the local real database」）。

⚠ **22 小时后（`3e9893045`）它已经变成**两列错位，且成因**不是漂移而是 schema 修复**。
`3e9893045` 就是 `fix(db): ensureWorkTypeSchema 消掉启动期 request_logs 上的 109 秒独占锁`，
它把两列的填充率真的顶上去了：

| 列 | 登记（2026-10-04 测） | `b0bd6616c` 实测 | **`3e9893045` 实测** | 分类变化 |
|---|---|---:|---:|---|
| `work_type` | 0.00% | 0.0011% | **0.0011%** | 仍 `goEmpty`（低于 0.005% 阈值）✅ 仍在登记里 |
| `client_protocol` | 0.00% | 0.01% | **0.26%** | 越过阈值 ⇒ 掉出 `goEmpty` |
| `is_final_success` | 0.00% | 0.0018% | **0.07%** | 越过阈值 ⇒ 掉出 `goEmpty` |

于是 `GO EMPTY ON THE SESSION SIDE` 从 **(2)** 变成 **(1): work_type**，
错位也从一列变两列，且仍是互为镜像：

```
unservable: registered but not measured: [client_protocol is_final_success]
degraded:   measured but not registered: [client_protocol is_final_success]
```

而 `GO EMPTY …` 那行是**同一测试的分类输出**，不是触发点。找「这个 FAIL 是什么引起的」
要落到**真正被断言的那一处**，不是同一份日志里最扎眼的那一行。

⚠ **这个红是陷阱，不要按它的提示去改登记**（门自身注释已警告）：
0.26% 虽高于 `effectivelyEmptyPP = 0.005%` 阈值而不再进 `goEmpty`，
但它意味着 **99.7% 的历史行仍为空**；`is_final_success` 的 0.07% 更是 **99.93% 为空**。
「退役后完全失去数据」这个实质声明**对这两列依然成立**。
照错误信息「按本次实测重算登记名单」去做，会把 `client_protocol` **和** `is_final_success`
从 `unservable` 悄悄降级成 `degraded`。**本轮不动这个文件**（不是我建的，
`d58a3c504` 与 `ensureWorkTypeSchema` 都没动它），处置留给属主。
⚠ 而且**不能只在「漂移」框架下决策** —— 这轮的变化**不是漂移，是 `ensureWorkTypeSchema`
把 schema 真的修好了**，且**回填若仍在继续，填充率还会继续涨**。
可选项：保留登记并给该测试加「schema 修复后重测」豁免 / 重测后重登记（**但要保住
「实质上 99%+ 为空」这个声明，不能因为越过 0.005% 就当它可服务**）/ 调整阈值 —— 三者取舍属主定。
