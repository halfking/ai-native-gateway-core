# 35 — R65-backlog 收口轮：admin 真库 17 既有失败专项 + rows 族三域迁移 + doHealthCheck 错误通道

> 2026-10-01 01:20–03:00，基线 `e28098cd2`（先 `git fetch` 合并并行会话两笔 docs 提交）。
> 轮次命名沿用多会话共享 R 序（memory 48h 审计轮轨 R64 之后），与 R35-N1 登记的三件 backlog 一一对应：17 失败专项（34 号文档 §5.2）、rows 族 backlog（§1.3）、doHealthCheck 复合 err（§1.3）。

## 0. 基线复现（先复现后动手）

- DSN：`.env.local` 的 `LLM_GATEWAY_DATABASE_URL`（llm_gateway@127.0.0.1:5432 共享开发真库）→ `TEST_DATABASE_URL`。
- `go test ./admin/ -count=1 -v` 全量：**17 个 FAIL 与 34 号文档 §5.2 完全一致**（此前一次非 verbose + `tail -40` 只看到 3 个，是输出截断假象——**失败清单必须 -v 全量枚举，不得以 tail 窗口为准**）。
- 分组：logs_get_log 族 3、telemetry 2、breakdown 族 9、RLS 1、balance 1、tenant stats 1。

## 1. 专项 ①：17 既有失败 —— 2 个真生产 bug + 6 文件夹具适配

### 1.1 真生产 bug（夹具红灯底下挖出的实锤）

**P1-A `admin/tenants.go:827`（42P01）**：R36-A1 内联 raw-union 片段 `logsTable` 以 `-- sqlreadguard:allow` **行注释收尾**，by-application 查询以 `FROM `+logsTable+` rl` 同行拼接——` rl` 别名被注释吞掉 → `missing FROM-clause entry for table "rl"`。**租户统计端点 by-application 阶段对任何租户任何窗口都 500**。修法：别名换行拼接（守卫 marker 必须与裸读同行，不能挪行——`internal/sqlreadguard` 匹配规则是行级 contains）。`session_catalog_usage.go`/`integrity_fingerprint_probe.go` 两处 marker 为自包含查询，无同族拼接陷阱（逐一核对）。
**回归窗口归因（R65 批判复审 blame 订正，撤回初稿"252-dev 从未跑过该阶段"的说法）**：`git blame` 铁证——`3efae99851`（09-30 06:53，三十六轮二段）引入片段时**无 marker**，`TestTenantStatsDaily_Live` 在 252-dev 实跑绿（该提交自述"修复后 0.7s/1.5s 全绿"）；`9b0801f832`（09-30 13:47，四十一轮续 merge）为平息 sqlreadguard 红把 marker 加到片段收尾行——**回归在此刻引入**，合并后复验只修了 chain.go/sqlreadguard 两红、未对真库复跑本测试，42P01 静默存活至 R65。教训：**平守卫红的改动本身改变了 SQL 语义，"守卫平红"≠"回归绿"，受影响的真库行为测试必须复跑**。

**P1-B `admin/session_analytics_breakdown.go`（42803，三端点全量 500）**：分桶 SQL `GROUP BY range, label` 失败。**机制归因（R65 批判复审 psql 变体实验 A–E 订正，撤回初稿"PG17 下 GROUP BY 输出别名不展开校验"的过归因）**：
- 实验 A：非保留字别名（`bucket_range`/`bucket_label`）+ `GROUP BY bucket_range, bucket_label` ——**成功**（无需 CTE）。GROUP BY 引用输出别名的通用能力正常；
- 实验 B/D：`AS range` + `GROUP BY range`（无论是否带表别名）——**必失败**，报误导性 `ss.request_count must appear in GROUP BY`；实验 E：加引号 `AS "range"` + `GROUP BY "range"` ——**同样失败**。真凶是 **`range` 被用作输出列名**（RANGE 是 PG 保留字，GROUP BY 对该名的解析退化为未分组校验失败，报错文本错误指向 CASE 内的输入列）；
- 实验 C：`ORDER BY CASE <别名> WHEN…`（任意别名，含非保留名）——**必失败**（普通名 42703 does not exist）。别名在 ORDER BY **表达式内**不可解析是 PG 文档明定的通用规则（输出别名只能作裸项），与版本无关；
- 修复采用 CTE 内层派生 `bucket_range`/`bucket_order` + 外层裸名分组排序——同时满足两条规则的稳妥形状（19 万真实行 psql 分组实证 + 端到端测试钉住）。初稿曾选过"GROUP BY 1,2 序号"中间态，被 ORDER BY 表达式规则击穿（ORDER BY 引用未分组输入列同样 42803），最终落 CTE 形状。
- 同族修复 nil 契约：空结果 `[]` 而非 JSON `null`（查询助手 `result := []T{}` 初始化——handler 层初始化会被助手返回的 nil 覆盖，修在源头）。

**影响面**：`/api/admin/session-analytics/{model-breakdown,session-shape,health-distribution}` 三端点 + `/api/admin/tenants/{code}/stats` by-application 阶段（读面恒 500，无数据损坏）。

### 1.2 夹具适配（六文件，逐个真库实跑）

| 文件 | 族 | 适配 |
|---|---|---|
| `logs_join_test.go` | 42703 | bodies 夹具去掉 601 已删的 `tenant_id`；**断言适配**：查询是 `COALESCE(x::text,'')`，无匹配契约值是空串，修前断言 `Nil` 与自身查询形状矛盾（任何数据下都不可能通过）；补 cleanup（修前 commit 后零清理）。 |
| `logs_test.go` BackwardsCompatible | 42703 | 迁移 573 已删 `request_logs_hot.{request,response}_body`，"bodies 在元数据表"不可表达 → 重构为**双腿覆盖契约**：`information_schema.view_table_usage` 必须同时含 `request_logs_bodies_hot` 与 `request_logs_bodies`（晋升行可达性的存储层前提）+ `_hot` 腿行级 JOIN 实证。 |
| `logs_test.go` MissingBodies | — | 同上 COALESCE 空串断言适配。 |
| `telemetry_test.go` BothTablesWritten | 42703 | 查询去掉 573 已删的 bodies 列，只验 preview；bodies 内容侧由 bodies 表查询断言。 |
| `telemetry_test.go` TransactionRollback | 契约过时 | 生产自 migration-455 UNIQUE(request_id) 起是**幂等 upsert**（注释明说容忍 at-least-once 重投递）→ 重写为 `TestPersistRequestLog_ReingestUpsertsSameRequestID`：两表各 1 行 + 最新投递胜出。 |
| `session_analytics_breakdown_test.go` | 23503/428C9/22P02 | 夹具租户 `tnt_test` 先建后删（fk_session_tenant）；`duration_seconds` GENERATED ALWAYS 列改经 `last=first+N 秒` 表达；`provider_id` 字符串改 bigint；上下文 super_admin→**tenant_admin**（super_admin 无租户过滤会在共享真库上聚 19 万真实行，断言不确定且踩真实数据形状）；日期窗动态覆盖 `now()`（修前固定 2026-07 窗口必然过滤掉夹具行）；cleanup 注册在 setup **之前**（`t.Fatalf`→Goexit 只跑已注册 defer，事后注册会漏掉半途失败的夹具行——本轮实证）。 |
| `provider_credential_balance_integration_test.go` | 契约过时 | "must advance on failure too" 写于 R43 之前（git 取证：测试 09-18 05:34，R43 fde958650 09-19 00:59 有意改为失败**不**推进 last_checked_at，防 manual 保护窗被失败点击无限续期）→ 断言对齐 R43 契约。 |
| `session_analytics_rls_test.go` | 环境前提 | 本地 DSN 角色是 superuser+BYPASSRLS 且 `session_summaries` 未 FORCE——该前提下两租户见全量行是 PG 语义必然。显式前置检查（`pg_roles.rolsuper OR rolbypassrls`）→ skip 并指路角色受控 RLS 门（Makefile test-rls）。 |

**真库 columnar 限制（登记）**：`request_logs_bodies` 月分区全为 citus_columnar，UPDATE/DELETE 0A000 不支持——**父表 bodies 行插入后无法夹具级清理**。R65 验证过程在 columnar 分区留下 1 行 `test-backwards-compat-*` 前缀惰性孤儿（无元数据行配对、不可达、随分区轮换自愈；批判复审 psql 复核实数 1 行，初稿写 2 系误计）；主库纪律下不做 DDL。这也否决了"造父表行测晋升腿"的方案，改为 §1.2 的视图覆盖契约断言。

**孤儿清扫**：修前失败轮在 `request_logs_hot`/`bodies_hot`/`usage_ledger_hot` 留下 54 行 `test-*`/`zz-stats-daily-*` 夹具孤儿（当年无清理机制），已按前缀夹具级 DELETE 清零（54/54/54）。

## 2. 专项 ②：rows 族 backlog 第二批 —— dashboard/usage/session 三域 37 循环 / 50 守卫位点

> R65 批判复审数字订正：本轮 commit message 写"29 循环"、初稿 §2 写"session 域 15 处"均为失真。按 `git show ede614269` 逐一清点：**37 个循环**（每循环 = 一处 scan-skip 留痕或一处迭代收尾检查，或两者），**50 个守卫调用位点**（warnRowSkip 33 + writeAggRowsErr 17；另有裸 `rows.Err()` 上抛/`slog.Warn` 收尾未计入位点数）。usage 14 + dashboard 4 + session **19**（1+2+3+1+1+4+2+1+4）。

R35-N1 核心聚合面（15 循环）+ R33 补迁（8 循环）之外，本批按域收口（静态扫描 161 函数缺检中取三域，ops/probe/pricing/work_types 等留后续批）：

- **usage 域 14 循环**：`usage.go` 10（hotKeys/byProvider/byModel/byKey/keyModels/keyTrend/keyTraffic/byApplication/listTenants×2）、`usage_enhanced.go` 2、`usage_provider_detail.go` 2（CSV 导出中断 slog.Warn 留痕，对账语义同 R35-N1 先例）。
- **dashboard 域 4 循环**：`dashboard_board_aux.go` 1、`dashboard_board_fallback.go` 3（内部 helper：warnRowSkip + `rows.Err()` 上抛，对齐 board_queries 先例）。
- **session 域 19 循环**：`session_state_handlers` 1、`session_title` 2、`session_compare` 3、`session_export` 1、`session_crosstalk_check` 1、`session_audit` 4、`session_clusters_handler` 2、`session_management_api` 1、`session_panorama_handler` 4。

**语义分型（不搞一刀切）**：
- HTTP 聚合 handler → `warnRowSkip` + `writeAggRowsErr`（中断 5xx，42P01→503 引导）；
- 内部 helper（`return x, err`）→ `warnRowSkip` + `rows.Err()` 上抛；
- 富化/面板降级通道（enrichHealthData/loadSessionTitlesBatch/generateHandoffSummary/panorama 面板/mgmt requests）→ `warnRowSkip` + 中断 `slog.Warn` 留痕后按已取得数据继续（闭包返回 err 会把富化失败升格成端点 500，与 best-effort 头注矛盾）；
- 完整性通道（session_export 消息导出：内存构建、头未发出）→ skip 留痕 + 中断**上抛**（导出包绝不静默截断）；
- CSV 流式导出（audit export）→ 头已发出，中断 slog.Warn 留痕。
- 既有 fail-fast 形态（scan err→500 返回）保留，只补缺失的 `rows.Err()` 终检（panorama timeline、clusters list）。

**接线守卫扩充**：`aggregate_read_guard_test.go` requireRefs 16→30 文件（新迁移文件全部纳入）。**变异验证**：删 `dashboard_board_aux.go` 唯一 `warnRowSkip` → 守卫红并精确指认 `lost guard wiring: warnRowSkip no longer referenced`；还原 → 绿。（两次无效变异留档：同文件多站点时删一处不会红——守卫是文件级 contains，防的是整文件退回吞错；改名 helper 只会 build 红不经过守卫断言。）

## 3. 专项 ③：doHealthCheck 错误通道重构 + 死 handler 接线

- **定性修正**：`doHealthCheck` 的 err 只来自 `loadCredentialRowLite`（解密失败/探测失败是带内 `health_status=unreachable`+`health_error`，不走 error 通道）——旧 handler `writeError(404, "credential not found")` 把 **DB 故障/连接超时伪装成凭据不存在**。
- **更深的发现**：`checkCredentialHealth` 是 `//nolint:unused` **死代码**——文件头注声称 `GET .../check-health` 存在，但 dispatch（`providers.go:1549`）只接 POST。R65 接线 GET（文档化 API 面成真）+ 删除 nolint。
- **三门**：`writeLookupErr(w, "credential not found", err)`——ErrNoRows→404 原文案；42P01→503 analytics_view_missing；其余→500 不外泄细节。
- **回归（真库门控，全实跑）**：不存在凭据→404 原语义；closed pool→500 而非 404；dispatch 级 GET 可达（旧日 405）+ POST 分支保持（closed pool 断言，不向真库写任务行——第一版直连真库插了 1 行 background_tasks，已 DELETE 并改写测试）。
- **变异验证**：回退旧形态 `writeError(404)` → ClosedPool 测试红（`DB failure must NOT be reported as 404 credential-not-found`）；还原 → 绿。

## 4. 顺手收口：installer 802 embed 断链（非本轮引入）

`go build ./...` 后 installer 编译失败：`embeddata/startup/802_...sql: no matching files found`。**stash 对照干净 HEAD 同样红**——并行会话 `e076f6381`（802 索引逐字回填）写了 `//go:embed`/map/runner/测试四点声明，唯独没拷 embeddata 文件本体（44.3 节 541 同族）。修复：canonical 文件字节一致拷贝（`cmp` 实证）→ installer 13 包 build+vet+test 全绿。

## 5. 验证矩阵（全部实跑，命令与结果）

| 验证 | 结果 |
|---|---|
| `go test ./admin/ -count=1 -v`（基线，真库） | 17 FAIL 复现（与 34 号文档一致） |
| 17 目标测试定向复跑 | 16 PASS + 1 SKIP（RLS 角色前置） |
| `go test ./admin/ -count=1`（专项收口后，真库） | **ok 73.5s，0 FAIL** |
| `go test ./admin/ -count=1`（rows 批后，真库） | **ok 78.1s，0 FAIL** |
| `go test -race ./admin/ -count=1`（真库） | **ok 84.4s，无 DATA RACE** |
| 17 目标 + 新增测试最终态定向复跑 | PASS（含 GETRouteWired 三测） |
| `go build ./...` + `go vet ./admin/` | 通过 |
| `cd installer && go build/vet/test ./...` | 13 包全绿（802 embed 修复后） |
| 守卫变异（board_aux 删 warnRowSkip） | 红→还原绿 |
| 通道变异（回退 writeError 404） | 红→还原绿 |
| 夹具残留复查 | tnt_test 三表 0 行；test-* 前缀 0 行；background_tasks 999999 0 行 |
| （批判复审）终态 HEAD 定向复跑 17+新增+守卫 | 0 FAIL（2 SKIP 均有意） |
| （批判复审）残留复查再跑 | 发现 logs_get_log_columnar 三测 t.Cleanup 时序坑每轮漏 7 行（积累 15 行实锤）→ 修 defer 后同文件 8 测重跑泄漏 0 |

## 6. 本轮教训

1. **失败清单必须 -v 全量枚举**：非 verbose 输出 + tail 窗口会把 17 个失败看成 3 个（本轮亲历）。
2. **夹具红灯下常埋真生产 bug**：17 个"既有失败"里 2 个是生产 SQL 错误（三端点+租户统计 500），归因清单（34 号 §5.2）只对了大方向，逐个跑才见真凶。
3. **PG 别名解析三连坑**（表达式 GROUP BY 校验 / ORDER BY 表达式内别名 / 保留字误导性报错）——版本鲁棒形状是 CTE 内层派生+外层裸名分组排序。
4. **cleanup 注册必须先于 setup**：`t.Fatalf`→Goexit 只执行已注册的 defer。
5. **变异验证要选单引用位点**：文件级 contains 守卫在多站点文件上删一处不会红——这不是守卫失效，是守卫的粒度契约。
6. **测试不得向共享真库写非夹具表**：wiring 测试的 POST 分支改用 closed pool 断言可达性，零写入。
7. **SQL 片段以 `--` 注释收尾时，一切同行拼接都是陷阱**——可复用片段的收尾注释要有消费方换行纪律，并用真库端到端测试兜底。
8. **平守卫红 ≠ 回归绿**（42P01 归因教训）：为平 sqlreadguard 红而加的行级 marker 本身改变了拼接处 SQL 语义——平红类修改必须复跑受影响的真库行为测试，"合并后复验清单"不包含的测试不会自己跑。
9. **机制归因必须做决定性对照实验**：初稿把 GROUP BY 失败归因为"PG17 别名不展开"（过归因）——非保留别名实验 A 一次即证伪。归因写进文档前，至少要有正反两个变体（失败形状 + 相邻成功形状）。
10. **数字以清点为准**：commit message"29 循环"与本文件初稿"session 15 处"均系凭印象书写，批判复审按 diff 逐一清点实为 37 循环/50 位点（session 19）——多轮已有同款失真前科（R34"10 处"实为 15 循环），数字必须从 diff 反推而非从记忆抄写。

## 7. 保持开放（登记不修）

- rows 族剩余域（ops_overview/pricing/probe_dashboard/routing/providers/work_types/tool_policy 等 ~130 处）——后续批沿用本批分型语义。
- `request_logs_bodies` columnar 分区 1 行惰性孤儿（`test-backwards-compat-*` 前缀）——随分区轮换自愈。
- `logsTable` 片段的换行拼接纪律无静态守卫——`TestTenantStatsDaily_Live` 是行为兜底；如再犯可考虑 SQL 解析级守卫（成本高，暂缓）。
- RLS FORCE 域级门（R41 登记 73 表审计）与角色受控 runner 继续走 Makefile test-rls 通道。
- 252-dev/154 生产环境的端点级部署验证未做（本轮无部署窗口）——三端点修复上线后应冒烟（部署轮所有者）。

## 8. 批判式复审（2026-10-01 03:10–03:40）：6 项声明重验，3 失真订正 + 2 顺手修正

> 方法：对本轮全部"声明完成"项以证据重验，不预设完成。重点打三类：机制归因、数字、行为残留。

### 8.1 复审发现与处置

| # | 发现 | 定性 | 处置 |
|---|---|---|---|
| 1 | §1.1 P1-B 机制归因错误：初稿称"PG17 下 GROUP BY 输出别名不展开校验"，但实验 A（非保留别名 `GROUP BY bucket_range`）**成功** | 归因过宽（决定性对照实验缺失） | **订正**：真因 = 保留字 `range` 用作输出列名（引号 `"range"` 实验 E 仍失败；ORDER BY 表达式内别名不可解析为独立通用规则）。psql 实验 A–E 编号留档 §1.1 |
| 2 | §1.1 P1-A 回归窗口错误：初稿称"252-dev 从未跑过该端点的这个阶段"，但 `3efae9985` 自述 252-dev 实跑绿 | 归因错误（未做 blame） | **订正**：blame 铁证——回归由 `9b0801f832`（四十一轮续 merge）加 marker 引入、未复跑真库测试；撤回原说法 |
| 3 | 数字失真：commit message"29 循环"、初稿"session 域 15 处" | 凭印象书写 | **订正**：按 diff 清点实为 37 循环/50 守卫位点（session 19）；§2/附录D/README 同步 |
| 4 | columnar 孤儿"2 行" | 误计 | **订正**：psql 复核实数 1 行 |
| 5 | **新缺口（非本轮引入）**：`logs_get_log_columnar_test.go` 三测试 `t.Cleanup(cleanupTestRequestLog)` 晚于 `defer pool.Close()` 执行——本轮在 logs_join_test.go 修过的同款时序坑，该文件未修；每轮全量漏 7 行（积累 15 行实锤：02:28/02:30/02:34 三轮） | 真缺口（预存） | **顺手修正**：3 处 t.Cleanup → defer（LIFO 先清后关池）；该文件 8 测重跑全绿 + 泄漏 0；15 行残留已按前缀清扫 |
| 6 | RLS 前置检查 `err != nil \|\| roleBypasses` 把前置查询自身失败也借道 skip | skip 语义过宽 | **收紧**：查询失败 Fatal、仅"确认 bypass"才 skip；注释写明 |

### 8.2 复审后验证（全部实跑）

- 定向（终态 HEAD）：17 专项 + 新增通道/接线/守卫测试 → 0 FAIL（2 SKIP 均有意：RLS 前置、OutboundBody 预留）
- `TestFetchRequestBodies|TestBodyFetchCache` 8 测（defer 修复后）→ 全绿且泄漏 0
- psql 实验 A–E（归因分层）+ `git blame`/`git log -S`（窗口归因）+ `git show ede614269` 位点清点
- 全量复跑见 §5 验证矩阵（合并后 73.1s + 本轮复审后再全量一次，见提交记录）
