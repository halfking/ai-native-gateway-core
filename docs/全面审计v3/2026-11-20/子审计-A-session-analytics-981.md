# 子审计 A：session-analytics / 迁移 981 列车（2026-11-18 后 48h 窗口）

> 只读审计 · 判定以 HEAD（3745f26f9 及其后合流态）代码为准，不以提交信息/主档自述为准。
> ★ **主控复核改判（2026-11-20，211 活体取证）**：本报告原判 **P1-D1「生产演进版视图 vs 仓内 canonical 分歧」被活体证据证伪**——直连 211 生产库实测：三个会话级物化视图（user_session_aggregates/project_task_aggregates/agent_usage_aggregates）的 owner 键均为 `NULLIF(s.owner_user,'')`，与仓内 canonical `981_...sql:208,237,271` **逐字一致**（pg_matviews.definition 全文比对）；第 4 视图 model_usage_aggregates 亦与 canonical 逐字一致（其 user_count 本就用 `COALESCE(rl.owner_user, rl.api_key_owner_user)`——981 正本 `:368` 原文如此）。提交信息所称「生产演进版 981 视图」实指**第 4 视图（请求级）自本就带双源键**，非「会话级三视图曾带外演进」。原 P1 降级为 P3（提交信息措辞易误导后续审计，建议后续提交避免「演进版」歧义表述）。**D2（users 列表锚陈旧 session_owners）被活体坐实并升级**：211 实测 session_owners 仅 12,198 行 / 4 个 distinct owner，而 session_dim 近 7 天 28,563 行、sessions 表近 7 天 created 27,626/28,634 owner 非空（96.5%）⇒ 列表行集漏掉绝大多数真实 owner、行内窗口用量被静默丢弃。D2 保留 P2 且证据升级为活体。
> 涉及提交族：`b394b197c` → `18de613e6` → `78cef308f` → `bc6c36695`（981 迁移四连）；`051c0ebc1`（路由 panic）；`cfdbede83`（numeric 扫描）；`02cec493f`/`0d32aebe5`（刷新器）；`95dcbddf1`（owner 双源键）；`95dc99803`+`3745f26f9`（projects 下钻）；`819339eb3`（fixture 46 列）；`fd2a3dad8`（JSON 解析）。

## 一、结论总表

| # | 审计问题 | 判定 | 一句话结论 |
|---|---|---|---|
| 1a | 981 幂等/可重放 | **已实现** | 全 DDL 带 IF NOT EXISTS/OR REPLACE，重放实测口径成立；唯 Part 8 回填会覆盖运维手工 is_probe=false（P3-4） |
| 1b | no-transaction 标记 | **已实现（正确撤豁免）** | 首版豁免被门禁判红→实测 REFRESH MV CONCURRENTLY 可入事务→撤豁免走 --single-transaction，HEAD 文件零标记 |
| 1c | 六处登记 | **已实现（实为 7 份副本）** | canonical/embeddata×2/installer-migrations×2/main.go×4锚点/runner/tsv/docker 通道全部在位且字节一致 |
| 1d | db-changelog 台账 | **已实现** | 行 11099 的 SHA 与实测文件 SHA 逐字相等；bc6c36695 把"与现实相反"的旧行改成事实 |
| 2 | 迁移→MV→刷新器→端点→前端 | **部分实现** | 4 视图+串行刷新+新鲜度门齐；但 Lark 告警只接 routing 族、且端点→前端腿为零消费者 |
| 3 | owner 双源键全聚合面一致 | **部分实现** | 请求级+projects 面全用 COALESCE；users 列表/详情基础统计仍锚 session_owners 单源，981 会话级 MV 用 sessions.owner_user 单源 |
| 4 | projects 下钻最终态 | **已实现** | 键列 ss.session_key + session_dim 直连双源键 + cst 窗口，session_dim 是表非视图 |
| 5 | numeric 扫描残留 | **已实现（1 处自述不符）** | 6 处修复表中 5 处落地 HEAD；#6 agent ranking 在 HEAD 不存在；SUM(bigint)→int64 未显式 ::bigint 的侥幸残留 3 处 |
| 6 | 路由重复注册 panic | **已实现（结构性）** | 改名 /user-ranking + RoutePaths SSOT + go/ast 全仓扫描守卫 + 生产同形 mux 双钉测 |
| 7 | 前端闭环 | **部分实现** | users/projects 链路端点逐一对齐 + flow13 双层断言；981 两个新端点无任何前端消费者 |
| 8 | JSON error.code 数字类型 | **已实现** | FlexibleString 只收窄在 wire 解码层，IR 层仍 string，错误归因逻辑零改动且修复了 error 帧整体丢失 |

## 二、逐项证据

### Q1 迁移 981 纪律

文件：`sql/migrations/startup/981_session_analytics_foundation.sql`（486 行）+ 同名 `.down.sql`（60 行）。

**(a) 幂等/可重放 — 已实现**
- `981_...sql:111-114` ADD COLUMN IF NOT EXISTS×4；`:116-126` CREATE INDEX IF NOT EXISTS×3；`:155/409/426` CREATE OR REPLACE FUNCTION×3；`:180/235/310/359` CREATE MATERIALIZED VIEW IF NOT EXISTS×4；`:214/291/339/389` 唯一索引 IF NOT EXISTS。
- Part 8 回填 `:449-452` 带 `COALESCE(s.is_probe,false)=false` 守卫；Part 9 `:470` 重放时重复 CONCURRENTLY 刷新（幂等）。
- 残留：重放会把运维手工置 `is_probe=false` 的会话重新标 true（该列自述为"物化缓存+手工覆盖位"，两者语义冲突）→ P3-4。

**(b) no-transaction 豁免 — 已实现且撤得对**
- HEAD 文件无 `dbinit:no-transaction` 标记（grep 仅命中头注释 `:3-4` 的复盘记录）。首版 `b394b197c` 加过豁免，门禁 `installer/internal/dbinit/no_transaction_test.go:41 TestNoTransactionMarkerIsJustified` 判红；`78cef308f` 实测（211/PG17.10）证明 REFRESH MV CONCURRENTLY 可在事务块内执行（对照组 CREATE INDEX CONCURRENTLY 被拒，证明测法有效）⇒ 撤豁免、走 installer `--single-transaction`，失败不留半套视图。lock_timeout 用会话级 SET 而非 SET LOCAL（`:24-34`，因 docker 通道是非事务流式 psql），理由链完整。

**(c) 登记面 — 全覆盖（实际 7 份副本 + 6 处锚点）**
1. canonical：`sql/migrations/startup/981_...sql` / `.down.sql`
2. `installer/cmd/llm-gw-installer/embeddata/startup/981_...sql` / `.down.sql`（diff 实测 IDENTICAL）
3. `installer/migrations/981_session_analytics_foundation/up.sql` / `down.sql`（diff 实测 IDENTICAL，第 7 份副本）
4. `installer/cmd/llm-gw-installer/main.go:1817-1821`（go:embed×2 + var×2）、`:2469-2471`（embeddedSQLFiles map×2）
5. `installer/internal/dbinit/runner.go:1961-1966`（StartupFiles，"已就绪"注记）
6. `sql/schema/installed_startup_migrations.tsv:398`（index 380）
7. docker 通道：`scripts/apply-db-revision-sequence.sh:2761-2774`（files=() 清单）+ `:3586-3594`（ledger_artifact_probe 产物探针——四视图+函数+四列）
- **全部 7 份 SQL 副本与 canonical 逐字节一致**（本审计实测 2 组 diff + 2 组 shasum）。

**(d) 台账 — 已实现**
- `docs/db-changelog.md:11099`：SHA `4b6c5fc3ef...91e`，状态 applied+verified。实测 `shasum -a 256` canonical = `4b6c5fc3ef...91e` **逐字相等**；embeddata 副本同 SHA。
- `bc6c36695` 明言上一行"内容与现实**相反**"（写未就绪+幻写版旧 SHA）并改成事实——台账曾失真，已订正。

**⚠ 重大分歧（P1，见缺陷清单）**：`95dcbddf1`/`3745f26f9` 提交信息与代码注释反复引用「**生产演进版 981 视图**（owner 键=COALESCE(owner_user, api_key_owner_user)）」，而 HEAD canonical 三个会话级视图的 owner 键仍是 `NULLIF(s.owner_user,'')`（`981_...sql:208,237,271`）且 981 文件在 `78cef308f` 后无任何提交改动（`git log --all --` 仅 2 笔）。即：生产视图定义已在本轮工作期间被带外演进，**仓内无任何迁移/文件承载该演进定义**——对生产重放 canonical 或在新机重装，会把演进定义回退成单源键版本。

### Q2 数据闭环（迁移→MV→刷新器→端点→前端）— 部分实现

**视图族**：981 建 4 个物化视图（user_session_aggregates / project_task_aggregates / agent_usage_aggregates 会话级；model_usage_aggregates 请求级），各配纯列唯一索引（CONCURRENTLY 硬前提；`task` 用 `''` 而非 NULL 归一以满足纯列索引，`:246-254/287-292`）。

**刷新器清单 — 4/4 在列**：`bg/materialized_view_refresher.go:433-436`（SessionAnalyticsViews 含 981 四视图 + 旧族 3 视图）；`0d32aebe5` 按实测成本重排（快先慢后，`:390-419` 注释含 211/252 实测耗时）。

**串行刷新 — 真实现**：`02cec493f` 新增 `serialRefreshViews` map（981 四视图强制串行），根因=211 PG 容器 /dev/shm 64MB 下 model_usage_aggregates 并行计划申请 DSM 16MB 必然 53100 → 视图永远刷不上 → 新鲜度门恒 503。串行走 `refreshSessionViews` 的单循环逐视图刷新（`:791-808`）。为什么不做"失败重试串行"有明述：刷新器连续失败有告警线，每轮先失败一次会打假告警（`:986` 注释）。

**失败处理 — 部分实现**：
- 有：每视图 Prometheus 失败指标 `recordMVRefreshFailure`（`:800`）+ slog.Error + 整轮 ctx 截断指标（`:813-818`）+ 锁竞争重试 `retryOnLockHeld`（`:788,793`）。
- **缺：Lark 告警**。`alertCallback`（main.go:4346 接 Lark）只在 routing 族 `refreshAll` 的连续失败计数里触发（`:720-732` failureCount≥2）；`refreshSessionViews`（含 981 四视图）**不递增 failureCount、不调 alertCallback** → 981 视图刷新连续失败只有指标与日志，无运维告警 → P2-2。

**新鲜度门**：`admin/session_analytics/mv_guard.go:47`（90min 预算）+ `:103 requireFreshViews` → 陈旧即 503 analytics_view_stale（防"陈旧被伪装成结论"，对齐仓内 245 9h37m 事故先例）。

**端点→前端腿 — 未实现**：`grep -rn "user-ranking|session-analytics/overview" web/src/` 零命中；`grep` 全仓 Go 侧亦无其他消费者。981 端点在 `cmd/gateway/main.go:8437` 接线后是**仅 API 可达的 UI 死面**（设计文档标注 agent/model ranking 为 Phase 3，但 overview/user-ranking 亦无消费者）→ P2-3。

### Q3 owner 双源键一致性 — 部分实现（漏两处会话级面）

**已用 COALESCE(owner_user, api_key_owner_user)（5 处）**：
- 窗口用量聚合：`admin/user_profile.go:142,149,153,164`（usageAggregatesByOwner，`95dcbddf1`）
- 员工×模型矩阵：queryUserUsageMatrix（`95dcbddf1` diff，registry 指纹同步 `admin/canonical_read_points_registry_test.go`）
- models 端点：`admin/user_profile.go:941`（handleUserModelUsage）
- projects 下钻：`admin/user_profile.go:1282`（COALESCE(sd.owner_user, sd.api_key_owner_user)，`3745f26f9`）
- 981 模型视图 user_count：`981_...sql:368`（COUNT(DISTINCT NULLIF(COALESCE(rl.owner_user, rl.api_key_owner_user),''))）

**未用（单源键，3 处）**：
- users **列表页**行集与身份列：`admin/user_profile.go:321`（COUNT(DISTINCT owner_user) FROM session_owners）、`:349-365`（listSQL FROM session_owners GROUP BY so.owner_user）
- users **详情基础统计**：`admin/user_profile.go:531-533`（baseWhere := "so.owner_user = $1"）
- 981 三个**会话级 MV**：owner 列 = `NULLIF(s.owner_user,'')`（`981_...sql:208,237,271`）→ overview/user-ranking 端点（`admin/session_analytics/queries.go:432-481,774-810`）读的就是这批单源键视图。

**数字对不上的具体机制**（user_profile.go:441-445）：列表行集以 session_owners 为锚、窗口用量按 owner 合并——COALESCE 键下有用量、但不在（陈旧的）session_owners 行集里的 owner **整行缺失**，其窗口用量被静默丢弃；且 overview（sessions.owner_user 口径）与 users 页（session_owners 口径 + request_logs COALESCE 口径）三套键并存。252 生产真值（自有提交实测）：request_logs.owner_user 当前流量 100% NULL、真实身份在 api_key_owner_user；session_owners 行集末次活跃停在 10 月。

### Q4 projects 下钻最终态 — 已实现

- 键列：`admin/user_profile.go:1293` `COUNT(DISTINCT ss.session_key)`（`95dc99803` 修 42703——session_summaries 无 gw_session_id，修前端点恒走降级分支）。
- 归属：`:1282` COALESCE 双源键 + `:1300-1301` `FROM session_summaries ss INNER JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id`（`3745f26f9` 弃 session_owners 陈旧行集；252 实测 dim 直连同窗 87,080 行 vs owners 路径 0 行）。
- session_dim 是**表**：`sql/migrations/startup/350_session_analytics_fix.sql:32`（CREATE TABLE IF NOT EXISTS session_dim）、`sql/objects/tables/session_dim.sql:9`。
- 窗口：`:1288` `ss.first_request_at >= cst_days_ago($N::INT)`（与成本趋势同日界）；上限/闸与 models 同构（days 1..90 缺省 30，`:1346`）。
- 与 users 页同源性：**部分同源**——用量五列与 models 端点逐字同表达式（request_logs 源）；但会话数/成本是会话级口径（total_cost_usd/credits），已在 `usage_meta.SourceView = "session_summaries × session_dim"`（`:1420`）与 8 语种注记显式声明口径差异，不冒充同源。

### Q5 numeric→int64 扫描 — 已实现（1 处自述不符 + 侥幸残留）

- `cfdbede83` 修复表 6 处，HEAD 落地 5 处：`admin/session_analytics/queries.go:173`（AvgTokens int64→float64 + ROUND(...,2)）、`:382/:438/:454/:475`（SUM(total_tokens)::bigint 显式收窄）。顺带修口径错 `:178-179`（COUNT(DISTINCT project_count)→SUM）。
- **自述不符（P3-1）**：修复表 #6「智能体排行 SUM(total_tokens)→AgentRanking」在 HEAD **不存在**——`grep AgentRanking admin/session_analytics/` 零命中，`cfdbede83 --name-only` 只含 queries.go + realdb 测试 + 文档。agent-ranking 仅为 `handlers.go:376` 注释里的 Phase 3 规划。
- 真库契约钉测：`admin/session_analytics/queries_realdb_test.go:43,51,60`（DSN 三级回退，无 DSN skip，断言"不报错+数据非空"双条件）。
- 侥幸残留（P3-2）：`queries.go:172,178-179`（SUM(session_count/project_count/task_count)，源列 bigint/int ⇒ SUM 返回 scale-0 numeric）与 `admin/user_profile.go:352`（SUM(so.request_count)）直接扫 int64，靠 pgx 对无小数 numeric 的解码行为通过真库实测（38982 行），未加显式 `::bigint`——与已修 6 处不同源列（numeric 带小数）但同类形状，换驱动/换版本无保证。

### Q6 路由重复注册 panic — 已实现（守卫而非仅删重）

- 根因：新包按设计文档注册 `/api/admin/session-analytics/users`，与旧族 `admin/handler.go:1334`（2026-07-07 用户画像，前端在用）同路径 → stdlib ServeMux panic → 网关退出(1) → 蓝绿候选槽起不来（`051c0ebc1` 提交信息 + `cmd/gateway/main.go:8421-8433` 注释）。
- 修法三层：① 改名 `RouteUserRanking = "/api/admin/session-analytics/user-ranking"`（`admin/session_analytics/handlers.go:387`，注释明示"不是 /users"）；② `RoutePaths` SSOT（`:390-397`）；③ 双守卫——`admin/session_analytics/contract_test.go:53`（RoutePaths 注册进生产同形 mux）+ `cmd/gateway/session_analytics_routes_guard_test.go:80,207,231`（go/ast 扫**全仓**已注册路由字面量比对 + 生产同形 mux 实注册测试）。
- 同类风险：守卫测试是全仓扫描型 ⇒ 未来任何列车新增字面量路由与既有路径撞车都会红，同类端点风险已收口。

### Q7 前端闭环 — 部分实现

- users/projects 取数链路逐一对齐：`web/src/api/admin.ts:287`（GET /users）↔ `admin/handler.go:1334`；`:346`（/users/{owner}）↔ `:1339`；`:374`（/models）↔ user_profile.go 子路由分发 `:838`；`:313`（/users/{owner}/projects）↔ `:489-490` 子路由转发 + `:1348 handleUserOwnerProjects`；`:341`（usage-matrix）↔ `:1338`。
- **981 overview/user-ranking 零前端消费者**（grep 零命中，见 Q2）。
- `819339eb3`：修的是 `admin/session_timeline_error_propagation_test.go` 的 fixture 基线红——并行车把 `sessionSummarySelectCols`（`admin/session_analytics_handler.go:181`）扩到 46 列（+total_credits_charged、+3×ttft），fixture 仍钉 42 列 ⇒ TestTimelineNoError_ReturnsDetail 在 origin/main 基线即红。归属=上游 drift 非本列车，修法=fixture 同步扩列且列序与 SelectCols 逐字一致。
- flow 13（`maestro/flows/13-session-analytics-users.yaml`）覆盖：登录→落地页出处行（request_logs_with_current_month）→窗口用量五列头→帮助抽屉（口径区别/公式/来源五段）→行按钮下钻→详情 h2→（第二层）`maestro/run-users-usage.sh` + `assert-users-usage.mjs` CDP 深度断言（折叠线下模型明细卡/矩阵/抽屉五段；退出码 0=绿/1=产品红/2=量具失效）。量具坑自记三条：视口 1440x1400、text 全匹配正则、应用内部容器滚 ⇒ scrollUntilVisible 物理到不了。

### Q8 JSON 解析修复 fd2a3dad8 — 已实现

- 改动面：`internal/ir/stream.go` 单文件 + 一份分析文档。新增 `FlexibleString`（`:299` 类型 + UnmarshalJSON：string→json.Number→报错）；wire 结构体 `Error.Code` 改 FlexibleString（`:299`），IR 层 `StreamError.Code` **保持 string**（`:222`），转换点 `:336 string(raw.Error.Code)`。
- 错误归因逻辑零改动：下游消费 `:339-340`（code 空回落 Type）、`:814/:1220/:1400`（仅序列化输出）不受影响；修复前整个 error 帧因 unmarshal 失败被丢（客户端拿不到错误详情），修复后数字 code（如 400）以字符串进 IR，failover/错误上报链路反而恢复工作。
- 边界核查：`"code": null` → json null 对 string-kind 无错误、落空串（安全）；bool/object 形态 → 报错传播（与修复前同形，可接受）。

## 三、缺陷清单

| 级别 | 编号 | 缺陷 | 证据 |
|---|---|---|---|
| **P1** | D1 | **canonical 981 与"生产演进版 981 视图"定义分歧且演进定义无正本**：三个会话级 MV 的 owner 键仓内是 `NULLIF(s.owner_user,'')`，本轮提交反复声称生产视图已演进为 COALESCE 双源键；该演进只存在于生产库，仓内无迁移承载 ⇒ 对 211 重放 canonical、或新机/灾备重装，会把生产演进定义**静默回退**，overview/user-ranking 数字随之漂移且违反本仓"台账=当时应用字节"纪律（台账记的是旧字节，库是演进字节，重放后反向失配） | `981_...sql:208,237,271`；`95dcbddf1`/`3745f26f9` 提交信息；`git log --all -- 981_...sql` 仅 2 笔 |
| P2 | D2 | users 列表页/详情基础统计锚在 session_owners（自有提交 `3745f26f9` 实测认定陈旧子集）⇒ 活跃 owner 整行缺失、窗口用量静默丢弃；同页用量五列却是 COALESCE 现役键 ⇒ 行内两套时间线 | `user_profile.go:321,349-365,441-445,531-533` |
| P2 | D3 | 981 四视图刷新失败无 Lark 告警：alertCallback 只接 routing 族连续失败计数，refreshSessionViews 不递增 failureCount | `bg/materialized_view_refresher.go:720-732 vs 791-818`；`main.go:4346` |
| P2 | D4 | 981 overview/user-ranking 端点零消费者（前端+后端均无）——端点活、UI 死面；`cmd/gateway/main.go:8404` 注释自述"死代码"教训后接线，但消费侧仍未闭环 | `grep user-ranking\|overview web/src/` 零命中 |
| P3 | D5 | `cfdbede83` 提交信息修复表 #6（agent ranking）在 HEAD 不存在——自述与交付不符 | `grep AgentRanking admin/session_analytics/` 零命中；`cfdbede83 --name-only` |
| P3 | D6 | SUM(bigint/int)→scale-0 numeric 直接扫 int64 未显式 ::bigint（3+ 处），依赖 pgx 解码行为 | `queries.go:172,178-179`；`user_profile.go:352` |
| P3 | D7 | Part 8 重放会覆盖运维手工 is_probe=false 的行（"手工覆盖位"语义未被回填守卫尊重） | `981_...sql:449-452` |
| P3 | D8 | down.sql 头注释（`:4`"先删函数（视图的刷新依赖）"）与 Part 1 注释（先删视图→再删判定函数）描述不一致；实际执行序正确（先 refresh 函数→视图→判定函数，动态 EXECUTE 无真实依赖） | `981_....down.sql:4 vs 16-23` |
| P3 | D9 | avg_context_size 恒 8192 常量（"暂时使用默认值"）——已在 cfdbede83 提交登记，本审计确认 HEAD 未修 | `queries.go:239-240` |

## 四、遗留登记建议

1. **D1 收口（最高优先）**：把生产演进版视图定义收编回仓——或新迁移（981b）重建四视图为双源键并按六面登记+台账新 SHA；或明确撤销生产带外演进、以 canonical 为准刷平生产。二选一，不能停在"生产与仓各持一版"。
2. **D2**：排查 session_owners 陈旧根因（358 触发器链断？rollup 停摆？），或把列表页/详情基础统计锚换到 session_dim 双源键（projects 卡已趟平同款修法）。
3. **D3**：refreshSessionViews 补 failureCount/alertCallback 接线（与 routing 族同构），或在告警面明确豁免 session 族并写明理由。
4. **D4**：overview/user-ranking 要么接前端（Phase 3 页面），要么在 RoutePaths/设计文档标注"API-only，暂无消费者"防止被当死代码再删一次（`051c0ebc1` 曾记录本包"从未被 import 即死代码"先例）。
5. **D5-D9**：随下次触达 session_analytics 包顺手收口；D6 建 `::bigint` 收窄惯例与 cfdbede83 已修 5 处对齐。

## 五、审计方法附注

- 全部判定基于 HEAD 工作树实测（diff/shasum/grep/git log），未连生产库；涉及生产真值（owner_user 全 NULL、session_owners 陈旧、87,080 行等）均转引自本轮列车自有提交的活体实测，已在正文标注来源提交。
- 981 台账 SHA、七份副本一致性、登记六面为**逐字节实测**；"生产演进版"分歧由仓内证据链（提交信息+文件改动史）推定，无法在本审计环境直连 211/252 验证生产视图现定义。
