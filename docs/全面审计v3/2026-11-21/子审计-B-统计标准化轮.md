# 子审计 B —— 统计标准化轮（窗口 c4b5d00cb..c6bf29aea）

- 审计对象提交簇：`2ae6b26d3`（可复用统计模块）/ `c005445c4`+`c980ee85a`（统计口径对齐轮）/ `aabacfdc3`（帮助手册 10.7 术语总表）/ `4e6e14667`（L1 成本口径修复）/ `6590e9fb5`+`b3ce1c253`（完成报告）/ `0e2516e6c`（时区收口 L-TZ1~4，同窗口交叉面）。
- 方法：只读取证；不采信提交信息原文；所有断言给 file:line 或命令实测；无法本地验证的生产侧断言标「待主控活体复核」。
- 环境实测基线：go1.27.4 darwin/arm64（`/Users/niuer/homebrew/bin/go`，`/usr/local/go/bin/go` 不存在）。
- 工作树说明：审计全程零代码改动。审计开始前已存在的 `M web/src/locales/de-DE/common.ts`（-2+1 行）与未跟踪 `docs/全面审计v3/` 为并行会话产物，非本审计所改。

## 一、判定总表

| # | 审计问题 | 三态判定 | 一句话结论 |
| --- | --- | --- | --- |
| 1 | 可复用统计处理模块 | **部分实现** | 模块建成（849 行 + 19 条钉测全绿）但**生产调用点 = 0**，两个旗舰计算器是死代码；同包其它片段（宇宙/日历/单一真源查询）确被两面复用 |
| 2 | 统计口径对齐（探测臂 + 会话宇宙） | **已实现** | 语义、文档（framework.md §146-159/400-402）、契约门三面齐；INNER 是「新臂的边界定义」而非静默剔除既有可见行；绑小原理成立且 R87 门结构断言实跑 PASS |
| 3 | 术语总表 vs 实现 | **部分一致** | 抽 5 项：活跃会话/健康分档位/90 分钟刷新对上；「花费跨单位警示」部分对上；**「今日=当前 UTC 日」与实现（系统时区）对不上**——P2 文档漂移 |
| 4 | L1 成本口径全链路拆分 | **部分完成** | API 分列字段已加（9 结构体/8 SQL），但**5 处排序键仍是混合 total_cost**，前端 3 处仍把混合值按 USD 格式展示（/dashboard 面板无警示）；CostCalculator 零参与 |
| 5 | 完成报告自述核验 | **存疑** | 抽 4 断言：1 假（100% 覆盖 → 实测 73.2%；19 个测试 → 实数 16）、1 夸大（100+ 术语 → 实数 40/39）、1 真（verify 脚本与 4 flow 齐备）、1 无法本地验证（211 部署）待主控活体复核 |
| 6 | 时区口径新增硬编码 | **无新增违规** | 窗口 diff `cst_` 新增 0 处；新增固定时区面只有 session_probe_scope.go 的 +08 FixedZone，与 981/994 物化视图日桶同尺，属 F7 同族有意独立口径且已文档化 |
| 7 | 测试实跑 | **全绿** | 5 个包级实测全 ok（含覆盖 73.2% 读数），详见 §八 |

## 二、Q1 可复用统计处理模块（2ae6b26d3）——部分实现

**P2｜模块建成但零生产调用（死代码）**
- 位置在 **Go 侧**（不在 web/src）：`domains/analytics/cost.go`（124 行，CostCalculator：CalculateInternalCost/CalculateExternalCost/SplitCost/DistributeTailDifference/ValidateCostUnit/Format*；`CalculateTotalCost` 恒返 error 钉死跨单位禁令，cost.go:122-124）+ `domains/analytics/derived.go`（239 行，DerivedMetricsCalculator：SuccessRate/ErrorRate/Average/WeightedAverage/Percentile/P50/P95/P99/GrowthRate/Min/Max/Sum/StandardDeviation/Format*）。测试 cost_test.go 211 行 + derived_test.go 275 行。
- 调用点 grep（全仓 `*.go`，排除包内与 `_test.go`）：`CostCalculator|DerivedMetricsCalculator|CalculateInternalCost|CalculateExternalCost|SplitCost|DistributeTailDifference|ValidateCostUnit|NewCostCalculator|NewDerivedMetricsCalculator` → **0 命中**（实跑命令输出为空）。
- 平行实现证据（同窗口统计工作未走该模块）：
  - L1 成本拆分 `4e6e14667`（另一作者 huangxt）直接在 `admin/session_analytics/queries.go` SQL 层加 `internal_cost`/`external_cost` 列与结构体字段（queries.go:34-35,79,89,120 等），不经 CostCalculator。
  - P95/P99 各面用 SQL 另算（admin/user_usage_stats.go:210 `latency_p95_ms`、admin/models.go:529、admin/ops_overview.go:502-522 maxP99），未用 derived.Percentile。
  - `DistributeTailDifference` 自称解决 Reconciliation F4 取整尾差——admin/ 全域 grep「尾差/TailDiff」无 reconciliation 侧消费者。
- **对分叉惯性的两面评价**：本轮在「对齐」轴上确实收敛了分叉（见 Q2：宇宙谓词/总览 SQL 单一真源，两面构造性复用）；但「计算器」轴上新增了 849 行零调用的平行库存——与 59 轮「四份枚举」同族病的温和变体（先建库、没人搬）。
- **P3｜代码异味**：derived.go:229-239 自写 `sqrt`（牛顿迭代 10 轮）注释「避免依赖math包」——同包 cost.go:4 已 `import "math"`，理由不成立；标准库 `math.Sqrt` 即可。

## 三、Q2 统计口径对齐（c005445c4 + c980ee85a）——已实现

**探测臂 SQL 语义（修后）**：`db/session_probe_arm_source.go:208-211` 定义包含探测行集 = 业务两臂（SessionFamilyTurnsSourceWindowedSQL）∪ 探测臂（session_turns_probe_hot 按 118 列 canonical 投影展开 + usage_facts 按 request_id 聚合费用挂回，`SessionFamilyProbeArmSourceSQL`:173-200）。三臂按 lane 互斥，COUNT 不重复（:21）。探测臂无费用列，费用三列 `t.probe_cost/probe_currency/probe_credits` 来自 usage_facts（traffic_class≠business）事务内聚合（:188-197），与供应商对账同源。118 列投影类型契约由 `nullByColumn` 表（:102-149）+ init 期 panic 防呆（:156-159）+ 契约门 TestProbeArmProjectionParity/SQLComposition/AttachmentCountNotNull 钉住（实跑 3 条 PASS）。

**INNER 改动的语义代价（c980ee85a ①）**：
- 改动本体：`db/session_probe_arm_source.go:187` `JOIN (...) uf ON uf.request_id = t.request_id`（INNER），注释 :34-41 完整记录理由——211 真库 probe_hot 43,455 行/日 ⊃ usage_facts 探测面 14,401 行/日，差集 = node_prober 0-token 存活探测，不进统计事件流也不进对账快照；owner 口径「包含探测 == 供应商对账」。
- **回答审计问题：不会把既有可见行静默剔除**。改前（c005445c4 之前）探测臂根本不在行集里；INNER 只决定新臂自身的边界（有 fact 的探测行才进）。但有两个披露义务：①「包含探测」用户语义 ≠「全部探测行」，实为「有计费事实的探测行」——代码注释已自述（:34-41），framework.md:146 也写明「usage_facts 全量」；② fact 写延迟边缘的行当日不出现、次日不补（:40-41，按对账口径日锁定）。**P3 命名提示**：开关文案「包含探测」对 0-token 存活探测仍盲，建议 UI 微文案披露（不阻塞）。

**会话宇宙（修后语义）**：`domains/analytics/session_probe_scope.go:49-67` 把 994 物化视图的宇宙定义（sessions 内连接 + session_dim 左连接 + owner 双源键非空 + is_probe + session_analytics_is_probe 三层判定）提为 Go 常量；契约门 `session_probe_scope_contract_test.go:32-65` 做归一化词序列对拍迁移 SQL（实跑 PASS）——「两份拷贝各改一半」的漂移形态被结构性地堵死。/dashboard session-overview 缺省套宇宙（admin/dashboardapi/session_overview.go:455-473），`include_probes=1` 回旧全量宇宙（历史行为），宇宙依赖缺失（未迁移环境 42P01）降级重试并显式标记 `probe_scope=degraded_included`（session_overview.go:508-515, 354-358）——**0 行/依赖缺失不静默，有逃生口有披露**。

**join 子查询「绑小」原理（c980ee85a ②）**：session_summaries 是 86 万行普通表（session_overview.go:433），宇宙 join 直接挂全表时，六腿并发各建一份大哈希 → PG 并行共享内存（/dev/shm）超限，211 首跑 SQLSTATE 53100。修法（:462-473）：把窗口/租户谓词**下推进内层子查询** `FROM (SELECT * FROM session_summaries WHERE first_request_at >= ... AND tenant_id = ...) ss` 再做宇宙连接——连接 build 侧从 86 万行缩成窗口内行集，共享内存有界；谓词是合取项可下推、连接键不变 ⇒ 行集与 994 物化视图逐行等价；引用 s./sd. 别名的宇宙合取项必须留外层（:472 注释）。R87 门更新为「六腿子查询绑小结构断言 + 字面量上限 14」（r87_session_overview_merged_legs_test.go:229-242，实跑 5 用例 PASS）。

**文档化**：`docs/statistics-standardization-framework.md:146-147`（包含探测/探测臂两行定义）、:157-159（探测行费用边界）、:400-402（两个片段的单一真源登记与契约门指向）——两个口径均文档化且与实现一致（本审计逐条对照过）。

## 四、Q3 术语总表（aabacfdc3）抽 5 项对照

术语表本体：`web/content/help-manual.zh-CN.md:874` 起，四组 39 术语行（实测 `awk` 计数；自称 40，量级一致）。逐项：

| 术语 | 术语表定义 | 实现源 | 判定 |
| --- | --- | --- | --- |
| 成功率 | 成功÷请求，防除零显示 0% | `domains/analytics/aligned_source.go:103` `SUM(success_count)::NUMERIC/NULLIF(SUM(request_count),0)*100,0` —— 算术一致；但「HTTP 200 且模型完整返回」的 success flag 写入侧语义本轮未追溯到位（预算内未定位写入点） | 算术对上/语义待复核 |
| 活跃会话 | 最近 24h 内仍有请求 | `admin/dashboardapi/session_overview.go:497` `FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours')` | **对上** |
| 健康分档位 | A≥90、B≥75、C≥60、D≥40、F<40 | `admin/session_health.go:199-211` gradeFromScore 逐档相同 | **对上** |
| 花费（跨单位警示） | 混合排序键，只可相对比较，「页面顶部有说明」 | 5 个会话分析页面有 costNote（SessionAnalyticsOverviewView.vue:212 等，zh-CN/sessionAnalytics.ts:118）；但 ① 说明实际在表格**下方**非顶部；② 同窗口新增的 /dashboard 会话统计面板 `web/src/components/analytics/SessionStatsRankings.vue:36-38,83,111,135` 把混合 `total_cost` 用 `$%.4f`（USD 格式化器）渲染且**无任何警示** | **部分对上** |
| 今日 / UTC 日 | 「今日=当前 UTC 日；看板默认口径」「全站统计按 UTC 0 点切日」 | 实现已是**系统时区**：`admin/board_time_range.go:143` boardPresetTimeRange 用 `settings.SystemDayStart`、:44-49 includesTodayUTC 注释明写「系统时区『今天』零点」（settings/timezone.go:34 默认 Asia/Shanghai）；同窗口 `0e2516e6c` 又把前端 6 处查询窗口默认改 systemTzDateStr()。术语表（aabacfdc3，23:58）晚于 0e2516e6c（23:29）落库，仍写 UTC | **对不上（P2）** |

- **P2｜术语表-实现漂移（今日/UTC 日）**：帮助手册是客户可见文档，宣称的切日规则与看板实际口径相反。同族附带：`includesTodayUTC` 函数名与函数体（系统时区）名实不符（board_time_range.go:44）。趋势日桶前端聚合仍按 UTC 日（simpleOverviewModel.ts:555-556 注释自述「与看板日窗同径」——该注释在系统时区改造后也已过期，属上一轮 F2 修复未清的注释尾巴）。
- **P3｜「陈旧/刷新中 ≈90 分钟」对上**：`admin/session_analytics/mv_guard.go:51` `sessionMvFreshnessBudget = 90 * time.Minute`。
- **待主控复核**：aabacfdc3 自称「钉测 26/26 绿」——HelpManualView.test.ts 只钉章节存在性（「少一章即红」，:5），未找到 26 条对应该术语表的钉测清单。

## 五、Q4 L1 成本口径修复（4e6e14667）——部分完成

**后端（已做）**：9 结构体补 `InternalCost`/`ExternalCost`（admin/session_analytics/queries.go:34-35 OverviewSummary、79 TopUser、89 TopProject、120 UserRanking、841/889/906 UserDetail/UserProject/ProjectTask、1370 AgentRanking、1486 ModelRanking）；8 处 SQL 补分列。`total_cost` 注释明写「保留用于向后兼容和排序……跨单位相加」（queries.go:30）。

**残留一（P2）｜排序键仍混合口径**：5 处 `case "cost": orderClause += "total_cost"`（queries.go:682-683、1233、1307、1406、1526，覆盖用户排行/项目排行/智能体排行/模型排行族）——59 轮 L6（「排行页花费排序键仍是混合口径」）未收口；SQL 混合相加本体仍在 10 处（queries.go:212,445,461,482,539,984,1223,1296,1390,1510），按提交自述属「有意保留」，但新分列字段没给排序提供替代键。

**残留二（P2）｜前端展示侧未拆干净**：
- 合规面：总览页「内部成本（美元）/外部成本（积分）」两卡分列（SessionAnalyticsOverviewView.vue:79-84，由 trends.cost 分别求和，:302-306）；UserProfileView 直接渲染 `internal_cost_usd`（UserProfileView.vue:575-577）。
- 残留面：① 同一总览页 topUsers/topProjects 表仍渲染 `money(u.total_cost)`/`money(p.total_cost)`（SessionAnalyticsOverviewView.vue:175,204）——有 costNote 兜底（:212）；② **/dashboard 会话统计面板**（SessionStatsPanel → SessionStatsRankings.vue:83,111,135）三处表格渲染混合 `total_cost` 且用 `formatUsd` = `$${value.toFixed(4)}`（:36-38）——把「美元+积分」的混合数**当美元展示**，且面板内无 costNote（grep 该组件与 SessionStatsPanel.vue 均 0 命中）——比总览页更差：既是假单位又无警示。

**残留三（结构性）**：本次修复与 Q1 模块零互动——CostCalculator.ValidateCostUnit/SplitCost 本可以在扫描层/渲染层拦这个洞，实际全走 SQL 平行路径。59 轮 R1 前身 L1 的「API 契约不清晰」已解，「跨单位数字不上屏」没解完。

## 六、Q5 完成报告自述反验（6590e9fb5 / b3ce1c253）

| 自述断言 | 反验结果 | 证据 |
| --- | --- | --- |
| 「100%测试覆盖，全部通过」「19个测试函数」 | **假**（覆盖 73.2%；测试函数 16 个：cost_test 6 + derived_test 10，两处提交树与当前树一致） | 实跑 `go test ./domains/analytics/ -count=1 -cover` → `coverage: 73.2% of statements`；`git show 6590e9fb5:...cost_test.go \| grep -c "^func Test"` = 6、derived = 10 |
| 「Maestro验证脚本 verify-statistics-pages.sh: 4个统计页面流程」 | **真**（路径在 scripts/ 非 maestro/，小偏差） | `scripts/verify-statistics-pages.sh` 存在，FLOWS 四项（02-overview-live/12-live-stage-tree/13-session-analytics-users/16-session-analytics-system）且 4 个 yaml 都在 maestro/flows/（ls 实测） |
| 「标准术语表: 100+术语，5大类」 | **夸大**（framework §三实数 40 行术语；帮助手册 10.7 实数 39 行 4 组） | `awk '/^## 三、/,/^## 四、/' ... \| grep "^|" ... wc -l` = 40；帮助手册同法 = 39 |
| 「部署 211 @ 2.6.1-2ae6b26d (build_seq=3171)」「API 端点验证通过」 | **待主控活体复核**（不能连生产；2ae6b26d 为仓内真实提交，版本号格式符合仓内惯例） | 生产复核要点：211 `/version` git_sha==2ae6b26d、build_seq==3171、`session-analytics/reconciliation` 200 |

- 另：6590e9fb5 自述「Maestro UI 测试因登录时序问题未完成」与 2489bba0a「CDP 探针 211 统计页面 58 条断言全绿」并存——后者是同窗口后续补救（e879feb1a 参数化 ADMIN_DISPLAY_NAME 后共享登录流修复），自述时点未说谎，但**报告未回填后续 UI 验证结论**，读 6590e9fb5 会得出「UI 未验证」的过时印象（P3 报告时效性）。

## 七、Q6 时区口径

- **`cst_*` 新增 = 0**：`git diff c4b5d00cb..c6bf29aea -- '*.go'` 中 `^+.*cst_` 零命中（实跑）。
- **`AT TIME ZONE 'Asia/Shanghai'` 新增仅注释与契约测试**：domains/analytics/session_probe_scope.go:27,81 与 session_probe_scope_contract_test.go:69；无新增可执行 SQL 硬编码。
- **新增固定时区面（有意独立口径，已文档化）**：`domains/analytics/session_probe_scope.go:71` `shanghaiLoc = time.FixedZone("CST", 8*60*60)` + ShanghaiCalendarDayStart/RangeUtc（:74-96）。理由在注释里自洽（:25-32）：981/994 物化视图日桶是 `(first_request_at AT TIME ZONE 'Asia/Shanghai')::date` **SQL 写死**，对齐面必须用同一把尺；settings.SystemDayRangeUtc 折系统时区，非 +08 部署机会错位一天。契约门 TestShanghaiCalendarRangeUtcBucketParity 钉窗口界逐值（2026-11-14T16:00:00Z 形态，实跑 PASS）。
- **P3 备注**：这是「固定刻度（物理结构）」与「系统时区（统计窗口）」两族并存——若 owner 未来改 `system.timezone`，board「今日」（SystemDayStart）随设置走而会话宇宙日窗钉 +08。判定与上轮 F7 同族（物理结构跟固定刻度），**不判为违规**；但日历分叉点应在 framework 文档里显式登记（当前只在代码注释，framework.md 未提两族并存）。

## 八、Q7 测试实跑读数（go1.27.4 darwin/arm64）

```
go test ./domains/analytics/ -count=1            → ok 0.011s（-cover: 73.2% of statements）
go test ./admin/dashboardapi/ -run 'SessionOverview|R87' -count=1
    → ok 0.025s（R87 5 用例 PASS：OldPerLegGone/MergedAggregates/MergedTrends/ScanPointsCapped/SevenConcurrentLegs）
go test ./admin/ -run 'Statistics|Stats' -count=1 → ok 0.522s
go test ./db/ -run 'ProbeArm' -count=1 -v
    → 3 PASS（ProjectionParity / SQLComposition / AttachmentCountNotNull）ok 0.059s
go test ./admin/session_analytics/ -count=1       → ok 0.037s
```

## 九、复核要点清单（给主控）

1. **生产活体（待复核）**：211 `/version` 是否 `2.6.1-2ae6b26d…(3171)`；`/api/admin/session-analytics/reconciliation` 200；request-logs 开「含探测」后 COUNT/token/cost 与 usage_facts 三面逐字相等（c980ee85a 自述 D1/D2/D3 对齐）。
2. **P2 修复建议采纳面**：① 术语表 10.7「UTC 日/今日」改按实现重写（或按页面族分列两把尺）；② SessionStatsRankings.vue 混合 total_cost 停用 USD 格式化器/补警示；③ 排序键 cost 增设 internal/external 分键或至少披露混合。
3. **死代码处置**：CostCalculator/DerivedMetricsCalculator 849 行零调用——要么在 L1 残留面（§五）接线后保留，要么登记回收（避免成为下一个「四份枚举」）。
4. **P3 小项**：derived.go 自写 sqrt；includesTodayUTC 名实不符；simpleOverviewModel.ts:555-556 过期注释；「包含探测」UI 微文案披露；framework.md 补「+08 固定尺 vs 系统时区」两族并存登记；6590e9fb5 回填 UI 验证后续结论。
5. **量具信任度**：本轮新契约门（probe arm 3 条 + universe 2 条 + R87 5 条 + Shanghai bucket 1 条）全部实跑 PASS，且对拍对象是迁移 SQL 文件本体（非自说自话），可信。
6. 共享工作树注意：`web/src/locales/de-DE/common.ts` 有并行会话未提交修改（非本审计产物），提交前需甄别。
