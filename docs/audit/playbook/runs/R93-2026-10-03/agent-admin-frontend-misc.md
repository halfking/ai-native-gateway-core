# R93 域C 报告：admin/前端/capability/杂项域（第三十三轮）

审计人：分域子代理 C（只读）。窗口 a0da9066d..HEAD，负责 8 笔提交。方法：逐笔直读 diff 与 HEAD 现状对照（提交信息不作为证据），目标测试本地复跑（admin/bg/executors 三包定向用例 + vue-tsc + i18n:check），SQL 门对窗口内全部新增 Go 侧 SQL 做了 Sprintf/拼接扫描。工作树中 VERSION/version.json/web/public/menu-config.json(version 时间戳)/docs/db-changelog.md 的未提交改动为并行构建产物，未纳入判定。

## 一、逐提交判定

### 1. 60b372bd7 fix(auto-tuning): proposals/accuracy 空窗返回 [] — **成立**

- 后端两处落点：`admin/auto_route_tuning.go:238`（`results := make([]proposalRow, 0)`）、`:803`（`results := make([]accuracyRow, 0)`），两个 handler 都在 `RegisterTuningRoutes` 挂载（:83、:88）。
- 同族清扫：同文件还剩 `var summary []strategyRow`（:888）/`var breakdown []breakdownRow`（:932）两处裸 nil——但 `handleStrategies` 带 `//nolint:unused`（:838）且**未注册**（:82-90 的挂载清单里没有它），前端唯一引用 `getTuningStrategies`（web/src/api/tuning.ts:44）无任何 view 消费（全树 grep 零命中）⇒ 休眠死码，无活体 null 面。修复范围覆盖了全部**在路由**端点。
- 前端防护真落地：`web/src/views/AutoTuningView.vue:51-52`（`proposalList/accuracyList = computed(() => x ?? [])`）、`:72`/`:86`（`Array.isArray(...) ? ... : []`）、`:291`/`:351`（`.length` 全部读防护后的 computed）、`:147-157` 新增 formatTs/fmtNum/fmtPct 空值兜底。另一消费方 `web/src/views/AutoRoutingOpsView.vue:77` 用 `(r.proposals ?? []).length`，同受防护。

### 2. ece68f148 feat(capability): 自愈回填 + 读错误不再当默认值 — **成立**（两处契约/运维注记见发现 P3-2/P3-3）

- **回填任务**（bg/capability_backfill.go）：周期 30min（:57）、staleAfter 6h（:61）、单轮批上限 50（:64）；探测复用既有 `singleResponsesPing`，**无证据不写**（:404-414 `supportsResponses == nil` 直接跳过并留痕）；准入闸门提成纯函数 `capabilityBackfillAdmit`（:212-243，凭据状态白名单 active/cooling/degraded :201-205 与 bg/node_probe 同集合）；upsert 带幂等（persistRow :459-472，ON CONFLICT + RowsAffected==0 报错）。接线在 cmd/gateway/main.go 两个互斥装配点（:4490、:4670 附近，均有 CHECKPOINT 日志）。
- **频率/打爆核算属实**：101 绑定 ÷ 每轮 50 ≈ 2-3 轮铺完，staleAfter 6h ⇒ 每绑定 ~4 次/天，提交信息"~400 次/天/凭据"算术成立。探测**串行**、单次 20s 超时、每 tick ≤50 次 ⇒ 峰值 ~1.6 req/min，无并发打爆面。kill switch `LLM_GATEWAY_CAPABILITY_BACKFILL`（:75，:96-103）tick 级生效、不需发版；但**默认开**——升级即开始消耗真实上游 token，属运营决策已在代码内声明（见 P3-3）。
- **读错误方向**：旧代码读错误 ⇒ debug + no-op（fail-open 到活检测）；新代码 `resolveDurableResponsesVerdict`（domains/streaming/executors/responses_durable_verdict.go:40-61）读错误 ⇒ 回落 SQL 持久结论 + executor 侧抬 Warn（executor_chat.go:466-474）。降 Debug→Warn 属实。对路由行为：正向结论**只**开非流式腿（:483-488，`upgradeEligible` 带 `!params.IsStream`，不外推 SSE）；负向关双腿（:476-482）。
- **三态落地**：`provider/client.go:1631-1636` SQL 投影加 `(cmcap.id IS NOT NULL) AS supports_native_responses_known`，Candidate 增 `SupportsNativeResponsesKnown`（:184-192），区分「没回填过」与「回填了 false」——与同文件 :1695-1701 的 cmcap/cmstream 双 LEFT JOIN 对上。
- **bg/probe_http.go bodySample**：:182-185 每次 outcome 都截留上游原文（truncateProbeBody 走 SanitizeErrorText），正向结论证据不再恒空。

### 3. 242114d08 perf(capability): 透传路由已读 node state — **成立**

- **perf 声称真消（调用点直读）**：`GetSupportsResponses` 全树唯一非测试调用点 executor_chat.go:460-461 已改为传 `cand.RoutedNodeState`；快照在 router.go:1335（filterHealthyNodes 批量 MGET 路径）与 router.go:1419（chooseLeastCooledCandidate）附加。`credentialfpslot/node_state.go:404-412`：prefetched 非 nil 则不再 GET。
- **诚实口径**：过期判定仍用 Redis TIME（node_state.go:426-433，注释 :394-397 明说「2 次 RTT → 1 次而非 → 0」），与实现一致。
- **一致性窗口**：executor 用的是**路由时刻**快照；MGET 与执行之间新写入的结论本请求看不见、下请求生效——窗口为毫秒级路由→执行时延，且过期快照仍按 Redis 时钟判过期（不复活死结论）。fail-open 面正确：批量读失败 filterHealthyNodes 提前 return（router.go:1317-1319），候选不带快照 ⇒ executor 自读；`pickedIdx` 配对修复（router.go:1393-1419）防止把别人的快照挂到早退分支选中的候选上。
- 测试面：`TestVapour03_PrefetchedStateRemovesTheNodeKeyGet`（redis hook 数 GET）等 280+246 行两份测试，本地复跑绿。

### 4. e5197e0a4 fix(admin): 会话列表按日期和 task_id 过滤 — **成立**

- **SQL 注入面：参数化，无拼接**。`admin/session_analytics_list_filters.go:23-24` 的 `fmt.Sprintf` 只往 SQL 里放**整型占位序号**，task_id 值走 `args`（:25）；日期解析成 time.Time 后绑参（:34-41）；非法日期/倒挂区间返回 400（handler :311-315）。`$2` 同号双引用是 PG 合法形态。argCount 线程在 handler（admin/session_analytics_handler.go:309-345）里先 search 后 filter 再 LIMIT `$n`/OFFSET `$n+1`，序号对齐有测试钉（TestAppendSessionListFilters_TaskAndRFC3339 断言 next=5）。无变量遮蔽（同层作用域复用赋值，`go build ./admin/` 过）。
- 列存在性有守卫测试直接读迁移文件（session_analytics_list_filters_test.go:79-100，测 655 的 gw_task_id 与 350 的 sd.task_id），本地复跑 PASS。
- 语义注记：日期窗口用 `last_request_at >= from AND first_request_at <= to` 的**重叠**判定（非包含判定），与参数文档一致；日历日上界 -1µs 对齐 timestamptz 微秒精度，正确。

### 5. 8fcb0d392 docs(sweep) 第十六轮 — **部分成立**（README 计数落地即过时，见 P3-4）

- 死路径修正核实：新路径存在（docs/06-deployment/04-runbooks/ops/session-health-operations.md、docs/03-design/03-interface-design/api-yaml/session-analytics.yaml），旧路径（docs/ops/…、docs/api/…）确已不存在。
- session-analytics.yaml 补登 task_id（:59 引用、:661-667 定义）与 e5197e0a4 实现语义一致（"匹配 session_dim.task_id 或 session_summaries.gw_task_id"）；date_from/date_to 此前已在（:642、:652），非缺项。
- **但** README 八语种 809→816 在提交时刻就已过时：`git ls-tree 8fcb0d392` 里 817_*.sql 已在树（其祖先 ab1c1b804 添加），实测 max 系列号为 817；HEAD 至今仍写 816。清扫轮自己重新引入了它正在修的那类过时（详见 P3-4）。

### 6. 317351daf feat(auto-ops): 四页整合 AUTO 路由运营工作台 — **成立**

- **路由注册一致性**：新宿主 `/routing-v2/auto-ops`（router.ts:203），旧四路由 redirect 到 `?tab=` 深链（router.ts:206-210）保兼容；四个子面板由宿主直接 import 不再有独立路由。
- **权限面**：宿主路由无 `requiresSuper`，super 门控下沉页签级——`normalizeTab`（AutoRoutingOpsView.vue:44-49）对非 super 把 profiles/tuning 判 null 弹回标注页签；SegTabs 仅 isSuper 追加两页签（:115-121）；KPI 行 super 项门控（:67-80）。深链 `?tab=tuning` 非 super 落 annotate，语义为 fail-closed。**后端层不变且层级正确**：tuning 全家挂在 `h.superAdmin`（admin/handler.go:1374 `RegisterAutoRouteRoutes(mux, h.superAdmin)` → admin/auto_route.go:98），标注族挂普通 admin（handler.go:1381-1386）与「全员标注」语义一致；task-profile 写端点仍在普通 admin（handler.go:1406 → taskprofile/handler.go:128-137，apply-tier-config/reload/import 可被任意 admin 直打）——**前存在缺口，本提交未改动、代码注释自认**（R64 兜底口径），见 P3-5。
- **菜单一致性**：menu-config.json 四项收敛为一项 `/routing-v2/auto-ops`（tenantScope "*"，与标注全员语义一致），与 appNav.ts:139 对齐；HEAD menu-config 与 appNav 无 drift（工作树仅 exported_at 时间戳差异）。
- **门禁覆盖**：vue-tsc include `src/**/*.vue`（web/tsconfig.app.json:23），HEAD 本机复跑 exit 0；`npm run i18n:check` PASS（0 missing keys）。本次复跑仅验 HEAD 现状，非该提交当时态——两提交同窗口且后续 5575414bf 又改过同视图，HEAD 绿即收敛态绿。

### 7. 2602bd32e feat(usage-trend): 模型多选+清除全部+自动刷新 — **成立**

- 服务端：重复 query 参数多选、去重/去空/上限 20（usage_trend_series.go:113-135 带 TestUsageTrendFiltersFromRequestModels 钉测）；`usageTrendModelsWhere` = `AND <常量表达式> = ANY($n)`（:102-108），modelExpr 是编译期常量（`m.dim_key`/providerModelsNameExpr/usageTrendModelExpr），值全部绑参——**无注入面**；六处调用点（:296/:335/:372/:486/:515/:554）args 线程序号一致；模型已定时跳过 `__others__` 折叠（:413-416）与注释口径一致。真库形态测试（TestUsageTrend…）复跑绿。
- 端点挂普通 `h.admin`（handler.go:1260 → usage.go:65-68），只读看板面，层级恰当。
- 前端：ModelPicker 多选 + 清除全部 + 自动刷新落在 UsageTrendExplorer.vue（128 行改动），vue-tsc/见上 HEAD 复跑绿。

### 8. f762e60f1 feat(usage-trend): 全屏铺满收口 — **成立**

- `fillViewport` meta（router.ts:276）在 App.vue:222/:481-487 消费（去 24px 内边距）；「系统自检」菜单项从 appNav 移除（appNav.ts:142）但 `/dashboard?tab=selfcheck` 仍是合法看板页签（DashboardView.vue:24 VALID_TABS 含 selfcheck）——「看板横条仍可达」属实；menu-config.json 为对应重导出，与 appNav 一致。

## 二、SQL 门红线（窗口全量）

窗口内新增动态 SQL 只有两处构造器，均为**占位序号 Sprintf + 值绑参**形态：`appendSessionAnalyticsListFilters`（session_analytics_list_filters.go:23/34/39）与 `usageTrendModelsWhere`（usage_trend_series.go:105）。capability_backfill 的 scan/upsert 全参数化（$1/$2/$3 + ON CONFLICT）。全窗口 diff 的 `Sprintf(` 新增行扫描无用户数据进格式串、无 SET+placeholder 混用、无裸拼接。**无红线违例。**

## 三、发现清单

**P0/P1/P2：无。**

**P3-1（休眠残留）**：`handleStrategies` 两处裸 nil slice（admin/auto_route_tuning.go:888、:932）编码 null 的形态原样保留；当前不可达（未注册 + 前端 getTuningStrategies 死码），但任何一次「把 strategies 挂回路由」的改动都会原样复活 60b372bd7 修掉的整页空白症状。建议随下次触达删除或补 make。

**P3-2（契约注释与实现有一个角不符）**：responses_durable_verdict.go:40-52 声称「回落 SQL 与无结论在路由结果上恰好一致」，论证只覆盖了 nativeNonStream 推导；当闸门经 **nativeStream 单腿**进入（`SupportsNativeResponsesStream=true` 来自 native_responses_stream 行，而非流式行 known=true/supported=false）且 Redis 读错误时，回落结果 (false, true) 会把**流式腿一并降级**（executor_chat.go:476-482），旧代码读错误则不动。方向保守（退 chat 安全），且 chat 显式要求 Responses 时 mode-fallback 可在同请求内回升（:1329-1360）；但「恰好一致」这一句作为契约注释不成立，建议改注释或把 SQL 回落降级限定在非流式腿。

**P3-3（运维：默认开的无退避重试 + 默认计费）**：① 无证据行不推进 last_tested_at ⇒ 持续 5xx/网络死的上游每 30min tick 重探最多 50 次、无逐行退避（bg/capability_backfill.go:404-414 + dueBindings ORDER BY last_tested_at NULLS FIRST 恒排最前）；有界（~1.6 req/min/凭据）但「~400 次/天」只约束**写证据**的探测，失败重试不计入该账。② kill switch 默认 ON（:96-103），升级即开始对全部 openai-responses 绑定发真实计费请求，无 opt-in——代码内已自declared并提供免发版关停，属已声明运营决策，记录在案。

**P3-4（清扫轮自伤）**：8fcb0d392 的 README 八语种计数 809→816 落地即过时——817 迁移文件在同期祖先 ab1c1b804 已入树，正确值 817；HEAD 至今仍写 816（README.md:112 等八处）。与 783e5df8f 自述「第五次同类遗漏」同一病灶：新迁移只补注册表不补 README 计数。建议把该计数改为派生（构建期生成或测试钉）。

**P3-5（前存在，本窗口未改动但工作台重新收口了入口）**：task-profile 写端点（apply-tier-config/reload/import，taskprofile/handler.go:133-136）挂普通 admin 中间件而非 superAdmin（admin/handler.go:1406）——任意非 super admin 可绕过前端页签门控直打 API。代码注释自认「后端暂挂普通 admin 中间件」（R64 口径）；前端 normalizeTab 只是 UI 门。建议立票把该族升 superAdmin 或在 handler 内加角色判。

**P3-6（UX，fail-closed）**：AutoRoutingOpsView.vue:43 `const isSuper = isSuperAdmin()` 在 setup 时一次性取值；super 用户在 userInfo 水合前深链 `?tab=tuning` 会落标注页签，且 userInfo 到位后无再归一化 watch。仅体验问题（反向不成立，非 super 拿不到页签）。

## 四、核实为健康的面

- 60b372bd7 修复覆盖全部在路由端点，前端两处消费方都有数组防护与空值格式化兜底。
- ece68f148 的「无证据不写」在 :404 有实现且有测试；准入门纯函数化后每条判据可脱离 schema 测试；upsert 幂等并校验 RowsAffected。
- 242114d08 未夸大收益（2→1 RTT 口径诚实），nil 快照契约（"no snapshot ≠ no verdict"）在 fail-open 分支、pickedIdx 配对、过期判定三处都守住了。
- e5197e0a4 参数化彻底，argCount 线程有序号钉测；列存在性测试直接读迁移源文件防漂移。
- 前端三个大改提交：vue-tsc（覆盖 src/**/*.vue）与 i18n:check 在 HEAD 复跑全绿；menu-config 与 appNav 无 drift；超管面后端层级（tuning=superAdmin）未松动。
- 目标测试本地复跑：admin（filters+usage trend+迁移列守卫）、bg（CapabilityBackfill）、executors（DurableVerdict+Vapour03）全绿。

## 五、不确定项

- vue-tsc/i18n/go test 均为 HEAD 现状复跑，未逐提交 bisect 当时态（窗口内有后续修正提交，HEAD 绿为收敛结论）。
- P3-2 的流式单腿场景需要「stream 能力行 true + 非流式行 false」的绑定形态才触发，未在真库验证该形态是否存在（迁移 612/613 结构上允许）。
- capability_backfill 的 scan/persist 两条 SQL 提交信息称对真 PG17+pgx 验过，本轮未接真库复现，仅静态审读+单测绿。
