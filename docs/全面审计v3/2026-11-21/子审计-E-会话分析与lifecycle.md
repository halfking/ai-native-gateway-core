# 子审计 E：会话分析闭环续 + 时区收口 + lifecycle/迁移纪律（第 61 增量）

- 审计窗口：`c4b5d00cb..c6bf29aea`（提交簇 5/6/7，共 99 笔，其中本子审计对象 8 笔）
- 审计人：子审计 E（只读取证；唯一写入=本报告）
- 方法：逐 commit 读 diff + 全仓 grep 穷尽性对拍 + 字节级 shasum 对拍 + 定向 `-race` 实跑（go1.27.4）
- 判定三态：✅ 成立（证据充分）／⚠️ 部分成立／❌ 不成立；「生产侧」推定标 **待主控活体复核**

## 一、判定总表

| # | 审计对象 | commit | 判定 | 一句话结论 |
|---|---------|--------|------|-----------|
| 1 | session.closed 双行扇出 | d1f64a879 | ✅ 成立 | 根因实锤（bus 一行一消费者，画像 worker 抢认领）、修法=发布层扇出两行（EventID 后缀去重）；**同族发现：request.completed 双订阅竞争未修（P2）** |
| 2 | 分析模型幻别名归一 | 1072c9f60 | ⚠️ 部分成立 | 只包了 5 个 ModelFor 调用点中的 2 个（两处 SetModel）；**3 处未包仍可产出幻别名（P2）** |
| 3 | L1 成本口径 | 4e6e14667 | ⚠️ 部分成立 | 契约字段/SQL/growth 全齐（9 结构体+10 处 SQL），但**零钉测新增**；排序键仍混合 total_cost（L6 既登记，一致） |
| 4 | 994 探针 pg_catalog 根修 | 946686609 | ✅ 成立 | `end_user_count` 列探针改走 pg_attribute+pg_class+pg_namespace；同脚本 18 处 information_schema 全打在真表上，**无同族残留** |
| 5 | 时区收口 L-TZ1~L-TZ4 | 0e2516e6c(+4eb00e2fe) | ✅ 成立 | TZ1=真修（6 视图查询窗口换 systemTzDateStr）；TZ2/TZ3=只加注释（独立口径判定，与登记语义一致，非假收口）；TZ4=设计内判定无代码改动 |
| 6 | lifecycle intervalSeconds 双单位 | 7d2cc318d | ✅ 成立 | 根因=formatDuration 已带单位再拼 " seconds" ⇒ "30 minutes seconds"(22007)；修法=单形态 "N seconds"+钉测；spec/worker 两侧默认值一致无漂移 |
| 7 | 993 自登记 + 995 八面登记 | 0fc42bd4f + 3643a6408 | ⚠️ 部分成立 | 993 补登记真（canonical==embeddata 逐字节）；995 九面齐且三份 up 逐字节一致，**但缺 installer/migrations/ 副本面（993/994 都有，P3）** |
| 8 | -race 定向 | （本轮实跑） | ✅ 全绿 | 10 个分析族包 + cmd/gateway 定向 + admin/session_analytics + sqlguard 全 ok，零 race 报告 |

## 二、逐题发现

### Q1 session.closed 双行扇出（d1f64a879）✅

**根因（成立）**：`domains/analysis/bus/pg_poll.go:33-51` 认领 SQL 为 `processed_at IS NULL AND type = ANY($1) ... FOR UPDATE SKIP LOCKED`＋mark 置 `processed_at=NOW()`（pg_poll.go:99-103）——一行事件全仓只有一个消费者。`domains/clientprofile/worker.go:61-68` ProfileWorker 订阅 `session.closed`（画像「总会话数」），`cmd/gateway/main.go:2932` 经 `SetupClientProfileIntegration` **无条件接线**（仅 `dbConn.Enabled()` 门控，main.go:2930）⇒ 画像 worker 先认领标终态，摘要链永远看不到。与提交信息「252 探针实录 worker=client_profile_worker attempts=0」自洽。

**修法（成立，发布层扇出，不改认领模型）**：
- `domain/analysis/events.go:23` 新增 `EventSessionClosedSummary = "session.closed.summary"`，头注完整记录背景。
- `cmd/gateway/session_analytics_close_chain.go:79-116` PublishSessionClosed 发布两行：legacy 行（EventID 后缀 `-p`）归画像 worker、summary 行（`base+"|summary"` 哈希 + 后缀 `-s`）归摘要链；payload 共享。
- `domains/analysis/workers/session_summary_worker.go:71-73` SubscribedTypes 只订 summary 专用类型；Handle 兼容两种类型（worker.go:85 防御性）。
- 幂等性：EventID 确定性哈希 + `domains/analysis/bus/publisher.go:90` `ON CONFLICT (event_id) DO NOTHING` 发布层去重；钉测断言「两行 EventID 互不相同」「同会话两次开合 ID 不同」（session_analytics_close_chain_test.go:69-77）。
- 双跑防护：`StartSessionAnalyticsCloseChain(pool, redis, v2ChainStarted)`（session_analytics_close_chain.go:275）V2 已启动则不重复起 loop。

**同族第三处（P2，新发现，本轮未修）**：`request.completed` 存在**同样的双订阅竞争**——
- `domains/analysis/workers/intent_worker.go:43-45` IntentWorker 只订 `request.completed`（loop 起 `cmd/gateway/main_pipeline.go:1442`，受 `EnableAnalysis` 门控，main_pipeline.go:1430）；
- `domains/clientprofile/worker.go:62` ProfileWorker 同时订 `request.completed`（loop 起 `domains/integration/client_profile_integration.go:137`，DB 在即启动）。
- 两者各自建 `NewPGPollFunc` 对**同一批 `type='request.completed'` 行** FOR UPDATE SKIP LOCKED 抢认领 ⇒ 谁先认领谁独占，另一方丢事件（意图统计与画像总请求数二选一失真）。d1f64a879 只扇出了 session.closed，未触此点。**待主控活体复核**（生产 211 是否同开 EnableAnalysis 与画像管线——若 EnableAnalysis=false 则竞争不触发）。

### Q2 分析模型幻别名归一（1072c9f60）⚠️ 2/5 调用点

**机制（成立）**：`domains/analysis/stage_config.go:84-107` ModelFor 解析序＝settings 值→"auto"→resolver→`stage.defaultModel()`；settings **未命中直接返回幻别名**（stage_config.go:106），且仓内无 "summary-fast"/"intent-fast"/"embedding-fast" 解析表 ⇒ 原样发往分析端点必 400（154 派发实录）。

**已修（2 处 SetModel 调用点）**：
- `cmd/gateway/main_pipeline.go:1760`（SetV2DispatchAnalysisResources）
- `cmd/gateway/session_analytics_close_chain.go:171`（buildSessionSummarizer，独立闭链共用）
- `normalizeAnalysisModelAlias`（session_analytics_close_chain.go:128-134）三种幻别名→"auto"；钉测 TestNormalizeAnalysisModelAlias（close_chain_test.go:82-97）。
- 辅证：resolver 已换 `sessionAnalyticsAutoResolver`（close_chain.go:63-65，显式 "auto" 透传），summarizer 三处 Complete 均带 `WithModel(s.model)`（sessionsummary/summarizer.go:186/246/451）⇒ main_pipeline.go:1662 适配器默认 "summary-fast" 不会被采用。

**未修（3 处同族调用点，P2）**——全仓 `ModelFor(` 调用点穷尽 grep（去测试共 6 处，除定义/两处已包外）：
1. `cmd/gateway/main.go:6211`：autoroute embedding shadow，env 未设/auto 时 `NewLLMStageConfig(nil)`（DefaultModelResolver）→ settings 未配 `session_analytics.model.embedding` 即返回 "embedding-fast" ⇒ 非空非 auto ⇒ **下行 fallback "text-embedding-3-small"（main.go:6214-6216）成死分支**，幻别名直进 NewEmbeddingClassifier。
2. `domains/analysis/request_summary.go:113`：llmRefine 用 `ModelFor(StageRequestSummary)`，未配即 "summary-fast"（stage_config.go:44）。可达性＝request_summary_mode 为 llm/hybrid（request_summary.go:68/83；引擎装配 main_pipeline.go:1777）。
3. `domains/analysis/clusterer.go:140`：vectorCluster 用 `ModelFor(StageEmbedding)`，未配即 "embedding-fast"。可达性＝管理面手动触发聚类（main_pipeline.go:1784 SetClusterRunner）。

**根修未做**：根因层（stage_config.go:106 未命中路径 / defaultModel 返回幻别名）未改，修在调用点 ⇒ 每加一个 ModelFor 消费者都要记得手包。**待主控活体复核**（三处未包点的生产可达性）。

### Q3 L1 成本口径（4e6e14667）⚠️ 契约齐、钉测缺

**已做（全部在 admin/session_analytics/queries.go，+66/-12）**：
- 结构体 9 个补 `internal_cost`/`external_cost` 字段并保留 `total_cost` 兼容注释：OverviewSummary（queries.go:34-37）+ growth（35/37）、TopUser（78-80）、TopProject（88-90）、UserRanking（119-121）、UserDetailSummary（840-842）、UserProject（888-890）、ProjectTask（905-907）、AgentRanking（1380-1383 区段）、ModelRanking（1496-1499 区段）。
- SQL 10 处同步拆分：getOverviewSummary 当前/前周期（212-214、238 区段）、getTopProjects（539-541）、getUserDetailSummary（984-986）、GetUserProjects（1235 区段）、GetProjectTasks（1308 区段）、GetAgentRanking（1402 区段）、GetModelRanking（1522 区段）；growth 计算同步（246-249 区段）。Scan 列序逐处对应，无错位。
- trends 早已有拆分（queries.go:353 `SUM(internal_cost) as internal, SUM(external_cost) as external`）；`MarshalJSON` 走 Alias 内嵌（queries.go:823-830）⇒ 新字段正常序列化。
- 单位口径注释明确：total_cost=美元+积分跨单位相加（queries.go:30）。

**缺口（P2）**：
1. **零钉测新增**——4e6e14667 只改 queries.go；`contract_test.go`（123 行）不包含任何 cost 键断言（grep 零命中），`handlers_test.go` 仅钉 `sort_by=cost` 参数（handlers_test.go:139/148），`queries_realdb_test.go` 无 cost 断言。按 AGENTS.md「端点形状钉测必须包含序列化输出断言」纪律，新增 9 结构体 ×2 字段的 JSON 形状无钉——后续重命名字段/漏 Scan 不会被 CI 拦。**待主控活体复核**（真实响应含 internal_cost 键）。
2. 排序键仍混合口径：queries.go:685/1238/1312/1413/1533 `orderClause += "total_cost"`——与 AGENTS.md L1/L6 登记「排序依据仍是混合口径，根修须动 API 契约与 contract_test.go」**一致**（本轮是登记的 L1 第一步，非收口完成）。

### Q4 994 产物探针 pg_catalog 根修（946686609）✅

**修了什么**：`scripts/apply-db-revision-sequence.sh:3626` 994 探针中 `user_session_aggregates.end_user_count`（**物化视图**的列）从 `information_schema.columns` 改为 `pg_attribute a JOIN pg_class c JOIN pg_namespace n ... a.attname='end_user_count' AND a.attnum>0 AND NOT a.attisdropped`；注释块（3618-3624）写明 information_schema 只收 relkind∈{r,v}。`session_dim.stop_reason` 是真表 ⇒ 保留 information_schema（正确选择，非一刀切）。四 MV/唯一索引原有 pg_class relkind='m' 口径未动。

**同族残留排查（无残留）**：同脚本 18 处 information_schema 逐一核对——981 探针（:3613）打在 `sessions` 真表；其余（:3381/3385/3387/3469/3499/3514/3521 等）打在 ursm_snapshot_slot_daily/sanitize_block_reviews/market_price_reference/prompt_injection_policies/market_price_platform_quotes 等真表；995 探针（:3632）两表两函数；`scripts/audit/migration_ledger_drift_report.py` 无 information_schema 用法。**待主控活体复核**（211 台账有 994 行的机器上重跑部署验证探针转绿——commit 信息称已实测 f→t）。

### Q5 时区收口 L-TZ1~L-TZ4（0e2516e6c + 4eb00e2fe）✅

- **L-TZ1（真修）**：0e2516e6c 改 6 个前端文件，把**查询窗口默认值/清空值/今日 KPI 的 UTC 日串**换 `systemTzDateStr()`：AnnotationView.vue:58-66（utcToday→getAnnotationDefaultWindow，含 clearFilters:153-154/chips:170）、AutoRoutingOpsView.vue:65-66、UsageEnhancedView.vue:37-39、UsageTrendExplorer.vue:97-99、useReconciliationPage.ts:80-81、ErrorTrendPanel.vue:43-44。`systemTzDateStr`（web/src/components/ui/kxDatePresets.ts:43）按系统时区挂钟迭代产出日串，实现真实。
- **残留与登记一致**：剩余 `toISOString().slice(0,10)` 全部为文件名/导出标签（useChatActions.ts:154、sessionSummaryExport.ts:36、sessionDetailExport.ts:169、taskProfile.ts:151）、UTC 日标签补零迭代（simpleOverviewModel.ts:595，与后端 UTC 日聚合标签同刻度）、display label（ProviderUsageExplorer.vue:62）、Intl catch 兜底（useFormat.ts:98/160，F6 主路径已注入时区）——无查询参数残留。
- **L-TZ2（只加注释＝符合登记）**：`admin/provider_cost_reconciliation.go:138-140` 补两条理由注释（monthKey 只消费 Year/Month＋对账月窗=日历月独立口径），代码零改动。登记原文即「待对账轮拍板」的独立口径判定，非假收口。
- **L-TZ3（只加注释＝符合登记）**：`admin/data_lifecycle_partition.go:551-556` parsePartitionBounds 保持 UTC，注释写明「物理分区边界/历史行定位」独立口径（与 bg/partition_manager.go partitionTZ 同族）。
- **L-TZ4（设计内判定，无代码改动）**：`settings/timezone.go:36` `systemTimezoneCacheTTL = 5 * time.Minute` + spec Description「改后最多 5 分钟内生效（热加载窗口）」（timezone.go:51）⇒ 多副本 ≤5min TTL 收敛是登记的设计内行为，无需改动。
- F1~F6/F3 主修在 4eb00e2fe（report_rollup.go 读侧/settings_timezone.go F3 动态偏移标签/useFormat 注入/BoardPanel 前端出口等 16 文件，含 5 份钉测），本子审计不重复。

### Q6 lifecycle intervalSeconds 双单位根修（7d2cc318d）✅

**根因（成立）**：旧 `formatSeconds` = `formatDuration(s) + " seconds"`，而 formatDuration 30 分钟已产出 "30 minutes" ⇒ "30 minutes seconds" ⇒ SQLSTATE 22007，idle/absolute 两条扫描（bg/session_lifecycle_worker.go:328/384）全部失败。
**修法（成立）**：`bg/session_lifecycle_worker.go:646-652` 单形态 `"N seconds"`（interval 字面量对任意秒数合法）；钉测 TestIntervalSecondsProducesValidIntervalLiteral（_test.go:78-98）含「多单位形态不许回归」断言。04479803e 为其 gofmt 收尾。
**两侧一致性（无漂移）**：spec 侧 `settings/spec_modules.go:367/380/392` 默认 "30m"/"168h"/"5m"（TypeString，Unit duration）；worker 侧 `cmd/gateway/session_analytics_close_chain.go:239-247` parseDurationSetting 以 time.ParseDuration 解析、非法回落 def（代码默认 30m/168h/5m 与 spec 逐字对应），再经 intervalSeconds 转 "N seconds"。无 59 轮 TTL 式双拷贝。

### Q7 993 自登记补 + 995 八面登记（0fc42bd4f + 3643a6408）⚠️ 995 缺一拷贝面

**993（成立）**：`0fc42bd4f` 给 `sql/migrations/startup/993_report_snapshots_exact_cost_cents.sql:47-49` 补 `INSERT INTO schema_migrations ... ON CONFLICT DO UPDATE` 自登记（≥952 双账本纪律）；docs/db-changelog.md SHA 同步为补写后实测值 `936da9c9…`——与当前 canonical/embeddata 两份 shasum **逐字节一致**（实测均 936da9c9…）；embeddata 通道漂移（缺自登记段）由窗口末合并笔 c6bf29aea 收口（git log 证实该文件最后两次提交=c6bf29aea/0f07c848e）。993 down.sql canonical==embeddata（05d3f21f… 一致）。

**995（窗口内唯一新迁移，git log --diff-filter=A 证实）九面核验**：
1. canonical up+down 成对：sql/migrations/startup/995_error_playbook.sql/.down.sql ✅；down 撤两表+函数+台账行（down.sql:12 DELETE schema_migrations）✅
2. embeddata up 副本 ✅ **逐字节一致**（两份 shasum 均 `7d6ddc1d…`）
3. embeddata down 副本 ✅ **逐字节一致**（均 `2f067f00…`）
4. installer main.go embed var×2+map×2：installer/cmd/llm-gw-installer/main.go:1863-1867、2506-2507 ✅
5. dbinit runner StartupFiles：installer/internal/dbinit/runner.go:1997 ✅
6. tsv：sql/schema/installed_startup_migrations.tsv:404（index 386）✅
7. docker 通道显式序列清单：scripts/apply-db-revision-sequence.sh:2813 ✅＋产物探针 :3632（超额）✅
8. 迁移自携带台账自登记：995_error_playbook.sql:194-197 ✅
9. 交付面钉测：internal/sqlguard/migration_995_delivery_test.go（130 行，钉 ①正本②embeddata up/down③docker 序列+探针④runner⑤embed/map⑥tsv）✅
- 另：cmd/gateway/main.go 的 +25 行是**运行时接线**（initErrorPlaybook :7846-7852、RegisterRoutes :8140-8155），非迁移登记面——任务给的「main.go embed/var/map」实为 installer main.go，已按实核验。
- 资产台账 tsv×3（db-object-expect/gap-ledger/installer-reachability）同 commit 更新 ✅。

**缺口（P3，待主控复核）**：993/994 均有 `installer/migrations/<name>/{up.sql,down.sql,README.md}` 第三份拷贝（994 副本与 canonical 逐字节一致，sha `796e3d84…`），该面由 `installer/internal/upgrader/offline_apply.go:99-102/279-292` 离线升级包消费（包内 migrations/ 目录在才执行）；**995 无此面**（find 全仓零命中）。若离线升级通道仍在投递，995 在该通道将不被应用（离线升级依赖网关启动 ensure 链兜底）；若该通道已退役则属一致性欠账。995 交付钉测也未钉此面（与 994 的 migration_994_delivery_test.go:89-92 不同）。

### Q8 -race 定向 ✅ 全绿

`export PATH="$HOME/sdk/go/bin:$PATH"`（go1.27.4 darwin/arm64）实跑：
- `go test -race -count=1 ./domain/analysis/ ./domains/analysis/... ./domains/clientprofile/ ./domains/sessionsummary/ ./bg/` → 10 包全 ok（bg 77.5s，含 lifecycle worker 与 intervalSeconds 钉测所在包）。
- `go test -race -count=1 -run 'SessionClosed|SessionAnalytics|NormalizeAnalysisModel|SessionSummary|ErrorPlaybook|SessionLifecycle' ./cmd/gateway/` → ok（3.8s，覆盖 d1f64a879/1072c9f60/3643a6408 的 cmd/gateway 侧测试）。
- `go test -race -count=1 ./admin/session_analytics/ ./internal/sqlguard/` → 双 ok（sqlguard 92.0s，含 995 交付钉测）。
零 race 报告、零失败。

## 三、复核要点清单（按优先级）

1. **P2**：request.completed 双订阅竞争（intent_worker.go:43-45 × clientprofile/worker.go:62，pg_poll.go 一行一消费者）——同 session.closed 事故同族未修；需主控确认生产 EnableAnalysis×画像管线是否同开，并裁决是否扇出/独占类型化。
2. **P2**：幻别名归一只 2/5 调用点——main.go:6211（embedding shadow，fallback 死分支）、request_summary.go:113（llm/hybrid 模式）、clusterer.go:140（手动聚类）三处未包；建议根修 stage_config.ModelFor 未命中路径。
3. **P2**：L1 成本契约零钉测——9 结构体新字段的 JSON 序列化形状无 CI 保护；排序键 total_cost 混合口径（L6 既登记）待后端轮收口。
4. **P3**：995 缺 installer/migrations/ 副本面（993/994 有）——需主控裁决离线升级通道是否仍投递；若投递，995 需补面＋钉测。
5. **待活体**：994 探针在台账已有 994 行的机器（154/245）部署链实测转绿；session-analytics 真实响应含 internal_cost/external_cost 键；TZ1 六视图在 +08 环境的窗口正确性（0e2516e6c 无前端测试新增）。
6. 无残留项：994 探针同脚本无 information_schema-on-MV 残留；TZ2/TZ3 注释式收口与登记语义一致（非假收口）；intervalSeconds 两侧默认值一致；993/995 交付拷贝逐字节一致。

## 四、本轮通用式（沉淀）

- **「一处事件源多个订阅者」在 FOR UPDATE SKIP LOCKED 认领模型下＝隐式互斥丢弃**——修扇出必须按「事件类型 × 订阅者矩阵」穷举，只修出事的那一格会把同一形态留给下一格（request.completed 即是）。
- **调用点兜底（normalizeAnalysisModelAlias）不是根修**——根因层（ModelFor 未命中返回幻别名）不动，每个新消费者都要记得手包；穷尽性判据是 grep 全部调用点，不是「出事的那条链路修了」。
- **契约扩展（加字段）必须同步钉测**——AGENTS.md「端点形状钉测必须包含序列化输出断言」对**加字段**同样适用：漏 Scan/漏 tag 在 200 响应里不可见。
- **独立口径的「只加注释」收口是合法终态**——判定真修/假收口的判据是登记语义（TZ2/TZ3 登记即「独立口径候选」），不是「diff 里有没有代码」。
