# 2026-10-15 —— r1033：横幅检查基准改 session_turns + 统计读源全面核查

> 用户裁决：横幅「流量数据已停更」的检查验证要以 **session_turns** 为准，
> 弃用 request_logs；并全面检查统计数据确实来自 session_turns。

## 一、统计读源盘点（生产 252 共享 PG + 代码双重实证）

| 总览页组件 | 数据源 | 停写后状态 |
|---|---|---|
| 看板主链（总请求/成功率/趋势/饼图） | request_stats_minute ← bg/stats_minute_rollup 扫 canonical 视图 | **活**（视图族自 S4 起由 session 分支供数：pg_get_viewdef 实证 `request_logs_with_current_month` 含 session_turns；生产 13:50 bucket 仍在推进、近 2h 1092 请求） |
| 看板 fallback（分钟缺覆盖时） | `request_logs_with_current_month_without_customer_id` | **冻**（纯 v1 视图：hot UNION ALL 母表，pg_get_viewdef 实证无 session 分支）→ 本轮换 session 族原生源 |
| 看板错误钻取 + 「总积分消耗」默认租户估算（≤7d 臂） | `requestLogsFromClause` → 物理 `_request_logs_hot` | **冻** → 本轮换 session 族热腿 |
| StatsDrawer 模型/API Key 热榜 | usage_ledger（停写后仍无条件写） | 活 |
| 队列视图 top-models / online | session_turns hot / session_summaries（f1b027645） | 活 |
| 会话统计 tab | session 族（by design） | 活 |
| 横幅检查 | `storage.request_logs_write_enabled`（v1 停写门） | 恒显「已停更」——而页面数字实际是活的 ⇒ **横幅在说谎** → 本轮重写 |

关键事实：**旧横幅的前提已不成立**。它说「本页数字只反映停写之前」，但看板
数字经视图 session 分支持续更新（停写两日后 minute bucket 仍每分钟推进）。
一条常驻的、描述不复存在冻结的横幅，比没有横幅更糟。

### 登记不动（非总览页读面，后续轮次）

- `admin/usage_trend_series.go:407,588`（用量趋势 detail 档，读纯 v1 视图，停写后冻结）。
- `maas.requestLogsSource`（租户计费展示面 ≤7d 臂读物理热表）——计费侧读数另行处理。
- `admin/auto_route.go:158`（auto_route 自有 S4 感知的源切换裁决，见 bg/auto_route_settle_source.go）。

## 二、横幅判据重写（admin/v1_freeze_notice.go → admin/data_horizon.go）

新问题只剩一个：**网关仍在接收业务请求时，session_turns 是否在记录新数据？**

两个信号对齐（单看任一个都不能判）：
- `session_turns_hot` MAX(ts)——会话族系统记录（ts 索引尾扫，毫秒级）；
- hub 业务流量时间戳（`lastBusinessActivity`，广播循环按 `!IsProbe` 守卫——
  探针/auto 请求不进会话族，计入会造成「只有探针在跑」窗口的误报）。

三态（继承 §9.79.8 纪律）：
- **live**：session 数据新鲜，或网关空闲（无业务流量信号）——不告示；
- **stale**：业务流量在 10 分钟窗内 + session MAX(ts) 落后流量时刻 >5 分钟
  （或热表为空）⇒ 断流，告示；
- **unknown**：session_turns 读不上来（DB 故障/nil DB/表缺失）——不说 live
  也不说 stale。

旧机制退役：`storage.request_logs_write_enabled` 从判据中彻底移除；
中央 JSON 出口的 `X-LLM-Gateway-V1-Data-Frozen` 头撤下（新判据查 DB，不能
挂每个响应）；头改为 `/api/admin/data-horizon` 端点自身的
`X-LLM-Gateway-Data-Horizon: stale|unknown`。

前端同步更名：`v1DataHorizon.ts → dataHorizon.ts`、
`useV1DataHorizon → useDataHorizon`（4 态：unknown/healthy/stale/failed）、
`V1DataFrozenBanner → DataHorizonBanner`（stale=警示色+后端三句 effect/
silence/hint；failed/unknown=危险色+failedHint）、i18n `app.v1DataFrozen.* →
app.dataHorizon.*` ×8 语种。

## 三、看板 fallback 换源

- `boardRequestLogsFromClause()`：`request_logs_with_current_month_without_customer_id`
  → `db.SessionFamilyTurnsSourceSQL()`（session_turns hot∪parent，115 列 v1
  形状投影）。fallback 全部查询引用的列（含探针谓词 quality_flags/task_type/
  origin_actor 与 credits 表达式的 credits_charged/credits_rate_multiplier）
  都在 canonicalColumnOrderV2 里，按名供给；credits_rate_multiplier 在会话腿
  为 NULL ⇒ 估算倍率回落 1.0（与 canonical 视图会话分支同口径）。
- `requestLogsFromClause(days)`（admin/usage_credits.go）：≤7d 臂物理热表 →
  `SessionFamilyTurnsHotSourceSQL()`；>7d 臂保持 canonical 视图（session
  分支 + v1 历史存量，覆盖停写前窗口）。

## 四、判据与变异验证

后端 `admin/data_horizon_gate_test.go`（重写，10 用例）：
- 流量×新鲜度矩阵逐格（健康/断流/空表断流/**空闲四格不告示**/容忍线两格）；
- unknown 措辞红线（不得出现「数据是新的/仍在记录」）；
- 端点载荷契约（null vs 对象、stale/unknown 互斥、`//` 键、响应头）；
- hub 零值钉子 + 业务过滤形状钉子；
- 换源形状钉子（board fallback 两腿 session、≤7d 臂不再读物理热表）。

变异验证（cp 备份恢复）：
- M1 删空闲守卫 → 仅 TestHorizonIdleNeverAlarms 红；
- M2 恒不判 stale → 断流三用例 + 容忍线红；
- M3 hub 初始化改回 time.Now() → TestHubLastRequestSeenStartsAtZero 红。

前端：`useDataHorizon.test.ts`（10 用例，含「键缺失≠健康」「unknown≠healthy
且≠stale」）+ `DataHorizonBanner.test.ts`（12 用例，含 failed 不展示后端三句、
D21 双主题令牌钉随迁）；parity 门 8 语种全绿。

全量门：go build（admin/bg/cmd）rc=0；admin 全包仅剩 pristine 既红三条
（TestDegradePayloadsCarryMarker、TestV1ReaderTiering×2，stash 复现确认与本轮
无关）；bg ok；web vitest 全量 rc=0；vue-tsc rc=0。

## 五、部署验证

本地 → 245 → 154（记录见部署日志）。

## 六、遗留登记

- §一表末三处非总览读面（usage_trend / maas 展示 / auto_route）——后续轮次。
- 判据的容忍窗（10 分钟流量窗 / 5 分钟落后容忍）是首版经验值；若生产出现
  误报/漏报，优先调 hub 信号侧（如纳入 mirror 失败计数）而不是简单放宽窗口。
