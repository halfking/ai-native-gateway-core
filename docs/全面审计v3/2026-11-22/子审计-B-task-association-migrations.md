# 子审计 B：997/998/999 迁移 + 会话总结任务关联摄取链 + 会话分析 SQL meta-audit

- 审计轮：第 62 增量审计轮 · B 路子
- 仓库：`/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-3`（HEAD=`c446de9a4`，干净）
- 审计窗口：`c6bf29aea..c446de9a4`（含并行车 `0f545826a` 方案与核心实现、`f712ea685` 12h 修订审计修复）
- 纪律：只读审计（go build / go test / grep / git；未连接任何生产/远程数据库）
- 日期：2026-11-22

---

## 〇、逐问判定总表

| # | 问题 | 判定 | 一句话结论 |
|---|------|------|-----------|
| 1 | 迁移 997/998/999 schema 语义 | **PASS（有条件）** | FK 指向真实存在、RLS 同族、down 幂等可回滚、锁守卫齐；999 未入 hot+分区纪律（P2 欠账），另有重复索引/类型长度不对称两处小瑕疵 |
| 2 | 九面登记复验（抽查全部 9 面） | **PASS（缺第 9 面）** | canonical/embeddata/offline/go:embed+map/StartupFiles/docker 通道/tsv/台账行 SHA 八面逐字节或逐字全对；**唯独没有 per-migration sqlguard delivery test**（994/995/996 都有） |
| 3 | 摄取链穷尽性 | **P1：写链死透** | 三包（taskaggregator/layeredsummary/sessiondedup）**全仓 import 数 = 0、不在 gateway 依赖图**；`session_summaries.task_id` 与 `has_layered_summary` 连写代码都不存在 |
| 4 | SQL 逐列对拍 | **PASS** | 14 个引用列全部存在于 890 视图投影；(key_id, gw_session_id) 去重 + session_dim 单列 PK ⇒ 无行数放大；全参数化零插值 |
| 5 | handlers 两端点（F21 + 三端对拍） | **PASS** | Error 级日志不回显 err 原文；容器键 `models`/`api_keys` Go↔TS↔Vue 逐字一致；20 个字段 tag 三端逐字一致 |
| 6 | taskaggregator F25 复核 | **PASS** | v1→canonical 切换正确、列映射无维度丢失；pgx v5 原生驱动不受 P0-1 数组分支 bug 影响；但调用方为零（死代码的 SQL 修复） |
| 7 | v1 退役门 | **PASS** | admin Retirement 系 14 测试全绿（含 Exposure / BlindSpotFiles / ProductionReader），0 offender |
| 8 | R-L1c / R-L1d 现状 | **属实、未改坏** | 两族比率口径并存仍在；两条新 SQL 与两个新结构体仍未进钉测表 |
| 9 | 定向门 | **全绿** | go build ✅ / session_analytics ✅ / sqlguard 投递+连续性门 ✅ / installer 投递五门+lock guard 门 ✅ / 路由冲突守卫 ✅ |

---

## 一、Q1 迁移 schema 语义审计

### 1.1 FK / 依赖对象真实性

| 迁移 | 依赖 | 证据 | 判定 |
|------|------|------|------|
| 997 `tasks.project_ref` → `project_dim(project_ref)` | 547 | `sql/migrations/startup/547_session_project_attribution.sql:137-138`（`CREATE TABLE IF NOT EXISTS public.project_dim (project_ref TEXT PRIMARY KEY ...)`） | ✅ 目标表与列真实存在，类型 TEXT 匹配 |
| 998 扩列 `session_summaries` | 310/606/655 | `310_session_summaries.sql:7-10`（session_key PK/tenant_id）、`:70`（created_at）；`606_...sql:37`（tags）；`655_...sql:70`（key_topics） | ✅ 索引引用的 tenant_id/created_at 在场；998 显式**不建 FK**（`998_...sql:79-88` 注释设计决定，task_id 可来自未同步 ACC 任务） |
| 999 `session_summary_layers` | 无 | `999_...sql:9`（Dependencies: 无） | ✅ 独立表，无 FK |

998 的 `canonical_session_key` 值域与 `session_dim.gw_session_id`（`350_session_analytics_fix.sql:33`，VARCHAR(128) PK）一致。

### 1.2 与 session_turns 家族 / 会话维度的关系

- 997 tasks 与 547 project_dim 同形态（本地维表、镜像外部实体）。任务归属同时存在两处：**既有的 `session_dim.task_id`**（350 触发器从 `gw_task_id` 写入，活链）与 **998 新增的 `session_summaries.task_id`**（零写方）——同语义两列两表并存（见 §三 P3-3）。
- 999 与 `session_summaries`（310，普通表）同族粒度（每会话若干行）。

### 1.3 hot+分区纪律判定（objective：大数据表 hot+分区、更新删除只在 hot、批量转移）

| 表 | 形态 | 判定 |
|----|------|------|
| 997 `tasks` | 普通表 | ✅ 设计内——维表，每任务一行，体量同 project_dim（547 也是普通表） |
| 998（session_summaries 扩列） | 普通表扩列 | ✅ 同族一致——session_summaries 本身就是普通表（聚合面，每会话 1 行） |
| 999 `session_summary_layers` | 普通表 | ⚠️ **P2 欠账**——window 层由 `cleanupOldWindows` 做**行级 DELETE**（`domains/layeredsummary/layered_summarizer.go:413-421`），segment/meta 永久累积且 `summary TEXT NOT NULL` 无大小界。对照同族：candidate_failure_logs 走月分区（`392_candidate_failure_logs_monthly_partition.sql`）、session_turns 走 hot+分区。体量估算 ≈ 会话数 ×（5 window + N segment + 1 meta），百万行级 + 行级 DELETE 与「更新删除只在 hot」纪律相悖。**缓解**：(a) 写链未接线，当前 0 行；(b) window 有界（每会话最多 maxWindows）。**接线前必须重估体量与清理路径** |

RLS：997/999 均 ENABLE RLS + tenant_isolation + super_admin_bypass 双策略（`997:118-144`、`999:113-141`），与 310 先例同构；998 扩列不涉及（310:87 已 ENABLE）。应用侧连接角色为表 owner（PG 默认 bypass），与家族行为一致，无新风险。

### 1.4 down 迁移可回滚性与顺序依赖

- 三份 down 全部幂等（`DROP ... IF EXISTS` / `DROP COLUMN IF EXISTS`）且带同款锁守卫（6 文件均有 `SET lock_timeout='5s'` + `statement_timeout='10min'`，与 995/996 同口径——`995:57-58`、`996:25-26` 实测比对）。
- 顺序依赖：998 未建 FK（注释掉）⇒ 997 可先于 998 回滚，无互相锁死；999 独立。997 down 的 `DROP TABLE ... CASCADE` 只顺带删 tasks 自己的 FK，不伤 project_dim。
- installer 门实测：`TestRegisteredStartupMigrationsCanRunInSingleTransaction` / `TestStartupMigrationsAboveCutoffHaveLockGuard` / `TestStartupMigrationsAboveCutoffHaveEffectiveSetLocal` 全绿。

### 1.5 小瑕疵（P3）

- **重复索引**：`uq_session_layer UNIQUE (tenant_id, gw_session_id, layer_type, layer_sequence)`（999:67-68）自带的唯一索引与 `idx_summary_layers_session`（999:76-77）**同列同序完全重复**——写放大白付一份。
- **类型长度不对称**：`canonical_session_key VARCHAR(128)`（998:48）vs 同值域的 `session_summaries.session_key VARCHAR(255)`（310:9）——当前值域是 gw_session_id（≤128）所以自洽，但任何人把 session_key 写进该列就会被截断报错。
- **跨租户 id 复用风险**：`tasks` PK 是全局 `task_id`（997:35，不 scope tenant），而 `EnsureTaskExists` 的 `ON CONFLICT (task_id) DO UPDATE` 不更新 tenant_id（task_aggregator.go:268-273）——若 ACC 任务 id 跨租户复用，第二租户的行会静默挂到第一租户的 tasks 行上（metadata 无唯一 (tenant_id, task_id)）。取决于 ACC id 语义，登记不修。

---

## 二、Q2 九面登记复验（全部 9 面逐一核过）

| 面 | 核验方式 | 证据 | 判定 |
|----|---------|------|------|
| ① canonical 三对文件 | ls | `sql/migrations/startup/99{7,8,9}_*.sql` + `.down.sql` 共 6 文件在场 | ✅ |
| ② embeddata 副本 | `cmp` 逐字节 | `installer/cmd/llm-gw-installer/embeddata/startup/99{7,8,9}_*` 6/6 **逐字节一致** | ✅ |
| ③ 离线升级副本 | `cmp` 逐字节 | `installer/migrations/99{7,8,9}_*/{up,down}.sql` 6/6 **逐字节一致** | ✅ |
| ④ installer go:embed + map | grep | `installer/cmd/llm-gw-installer/main.go:1894-1909`（go:embed ×6）、`:2557-2562`（embeddedSQLFiles map ×6） | ✅ |
| ⑤ dbinit StartupFiles | grep | `installer/internal/dbinit/runner.go:2005-2007`（3 个 up 文件在序列尾部，996 之后） | ✅ |
| ⑥ docker 通道 | grep | `scripts/apply-db-revision-sequence.sh:2821-2823`（files=()）+ `:3650-3654`（ledger_artifact_probe ×3，探针语义与迁移内容匹配：tasks 表+idx_tasks_tenant_status / session_summaries 4 新列 / session_summary_layers 表） | ✅ |
| ⑦ tsv 台账 | grep | `sql/schema/installed_startup_migrations.tsv:406-408`（index 388/389/390；紧随 996 的 387，与 996 台账行注记的索引顺移一致） | ✅ |
| ⑧ db-changelog 台账行 | sha256 对拍 | `docs/db-changelog.md:11139-11141`；实测 `shasum -a 256`：997=`f0e47511…` / 998=`e5706ba1…` / 999=`b4df3d4f…` 与台账行**三三一致** | ✅ |
| ⑨ 通用投递门 + per-migration sqlguard delivery test | 实跑 | `TestStartupMigrationsStayInEveryDeliveryChannel` ✅ 绿；installer 五门（ByteMatchCanonical/AboveCutoffHaveLockGuard/CanRunInSingleTransaction/StartupFilesAreAllEmbedded/AtOrAbove704AreRegistered）✅ 全绿；**但 `internal/sqlguard/` 没有 migration_997/998/999_delivery_test**（同目录 977/978/980/990_991/994/995/996/971/972 都有） | ⚠️ 缺口 |

**P2-1（九面口径第⑨面缺位）**：996 台账行把「sqlguard migration_996_delivery_test（幂等 DDL/闭集/守卫/自登记/九面对拍）」列为九面之一，995/994 同款；997/998/999 的交付却止步于通用门。后果：DDL 语义（幂等重放、探针断言、守卫在场）没有专属钉测，后续任何人改这三个文件字节，通用门只能抓「副本不一致」，抓不住「语义回归」。建议补三份 delivery test（形态照抄 migration_996_delivery_test.go）。

---

## 三、Q3 摄取链穷尽性（本轮最重要发现）

### 3.1 写方/读方全景（全仓 grep）

| 表/列 | 代码内写方 | 代码内读方 | 接线状态 |
|-------|-----------|-----------|---------|
| 997 `tasks` | `domains/taskaggregator`（EnsureTaskExists :262 / UpdateTaskRecord :219） | 无 | **包零 import** |
| 998 `is_duplicate` / `canonical_session_key` / `similar_session_keys` | `domains/sessiondedup`（similarity_detector.go:237 / :266） | 无 | **包零 import** |
| 998 `session_summaries.task_id` | **无（连写代码都不存在）** | 无 | 死列 |
| 998 `has_layered_summary` | **无（连写代码都不存在）** | 无 | 死列 |
| 999 `session_summary_layers` | `domains/layeredsummary`（saveLayer :374 / cleanupOldWindows :414） | 同包（getNextSequence :354 / GetLayers :447） | **包零 import** |

### 3.2 决定性证据：三包不在二进制里

```
$ grep -rn "domains/taskaggregator\"|domains/layeredsummary\"|domains/sessiondedup\"" --include="*.go" .
（零命中——无任何 import 语句）
$ go list -deps ./cmd/gateway | grep -E "taskaggregator|layeredsummary|sessiondedup"
（零命中——不在 gateway 依赖图，exit=1）
```

对照组（同窗口内已接活的链）：errorplaybook 有 `cmd/gateway/error_playbook_init.go:39-131` 全套装配；admin/session_analytics 有 `cmd/gateway/main.go:8549` `sessionanalytics.RegisterRoutes(mux, saSQLDB, wrapAdmin)`（生产 mux 门 `TestSessionAnalyticsRoutesOnProductionShapedMux` 绿）。

### 3.3 归因与后果

- 归属：`0f545826a`（窗口内）交付 schema + 三包，**提交信息自己声明「后续步骤：[ ] 集成到 SessionSummaryWorker」**、`docs/audit/2026-11-21-实施进度总结.md`「Phase 2: 集成到现有系统 (1-2天)」列在待完成——**分期意图是明示的，不是隐瞒**。
- 但 `f712ea685` 把三张迁移的九面投递登记补齐 ⇒ **下一次 211/154/245 部署将建出：三张永久空表 + session_summaries 5 个死列 + 4 个部分索引**（dedup_candidates 的谓词 `similar_session_keys != '{}'` 恒空；layered_pending 谓词 `has_layered_summary=FALSE` 覆盖全部存量行 ⇒ 全表二级索引）。563 触发器显式列清单不含新列 ⇒ 热路径无影响（`563_session_summary_trigger_on_hot.sql`）。
- 与 995 关系：**不同链**。995 = errorplaybook worker 摄取 `candidate_failure_logs_hot` 错误窗口（P0-1 修复后已活，已接线）；997 = tasks 会话→任务聚合（死）。taskaggregator 与 997 是**同一条写链**——F25 把它的 SQL 修对了，但调用方仍为零 ⇒「修好的死代码」。
- 判级 **P1**（非 P0）：无运行期故障、无数据损坏；但「迁移将上生产而功能不存在」的错位应该在台账行与主档里点名（当前 997/998/999 台账行只写了「补登投递链」故事，没写「功能未接线、表将空转」这一面）。
- 接线前置项（届时必看）：layeredsummary 每层 window/segment 都是一次 LLM 调用 ⇒ 接线时必须过与 R3（未知错误分析费用闸）同族的成本闸；taskaggregator 的 `getTaskSessions` 对每会话发一次 canonical 视图相关子查询（task_aggregator.go:109-117）⇒ 任务大会话多时必须批量化。

---

## 四、Q4 SQL 逐列对拍（userModelUsageSQL / userAPIKeyUsageSQL）

### 4.1 视图投影核对（基准 = `890_drop_request_logs_v1_family.sql:103-228` 的 118 列投影，两臂 `session_turns_hot ∪ session_turns`）

| SQL 引用列 | 投影出处 | 判定 |
|-----------|---------|------|
| outbound_model | `t.model AS outbound_model`（890:112） | ✅ |
| client_model | `d.client_model`（890:111，details 腿） | ✅ |
| total_tokens | `NULLIF(COALESCE(prompt,0)+COALESCE(completion,0),0)`（890:121，可 NULL ⇒ `SUM+COALESCE` 安全） | ✅ |
| cost_display / cost_usd | 890:145 / :120（numeric(14,8)） | ✅ |
| success / cache_read_tokens / latency_ms / ts | 890:119 / :125 / :117 / :104 | ✅ |
| api_key_id | `(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)`（890:108，expr 列） | ✅ |
| gw_session_id | `(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)`（890:144，expr 列，WHERE IS NOT NULL 过滤正确） | ✅ |
| owner_user / api_key_owner_user / key_alias | `d.owner_user`（890:152）/ `d.api_key_owner_user`（890:154）/ `d.key_alias`（890:153，details 腿） | ✅ |
| id | `NULL::bigint`（890:103）⇒ 注释「COUNT(DISTINCT id) 恒 0」的说法成立，COUNT(*) 是正确选择 | ✅ |

registry 侧已有同口径登记：`admin/canonical_read_points_registry_test.go:740-755`（两条读点逐列登记，含「扫描器只登记 SELECT 列」的 ts WHERE 谓词手工补记）。

### 4.2 去重逻辑反例推演

- `session_dim` PK = `gw_session_id` 单列（`sql/objects/tables/session_dim.sql:30`）⇒ `key_sessions`（DISTINCT (api_key_id, gw_session_id)，queries.go:1803-1810）JOIN dim 后每 (key, session) 至多 1 行 ⇒ **请求行×会话行放大被真正防住**；且 COUNT/SUM 都发生在 `key_stats`（按请求行聚合，:1787-1801），proj 臂只产 project_count/top_project，互不污染。反例（若 dim 有重复 gw_session_id）也不成立——PK 排除。
- `COUNT(*)`：视图一行 = 一次 turn/请求 ⇒ 请求数语义正确。

### 4.3 owner 双源 / 注入面 / 性能

- owner 键 `COALESCE(rl.owner_user, rl.api_key_owner_user) = $1`（queries.go:1701/1798/1806）与既有锚 `admin/user_profile.go:968` **逐字同族**（2026-11-20 数字出处轮对生产验证过的形态）。与 994 视图族的 `NULLIF(COALESCE(...),'')`（994:66/95）存在家族口径差：对「按具体 owner 查询」两者等价（$1 非空、''≠owner 名）；仅 owner='' 的行归属去向不同（994 视图归 api_key_owner_user，Go 侧落到空组）⇒ **P3 口径注记，非缺陷**。
- 注入面：两个常量 SQL 全部 `$1/$2/$3` 位置参数经 `QueryContext` 绑定，owner/日期零插值 ⇒ 无注入面。
- 性能：
  - `COALESCE(owner...)` 谓词不能用 owner 索引，但 registry 已把「经 canonical 视图读无法表达分区下界」登记为全家缺陷（`canonical_read_points_registry_test.go:2583`）⇒ 与 user_profile.go:968 同病同登记，非本窗口新增。
  - `LEFT JOIN LATERAL (... key_alias ... ORDER BY a.ts DESC LIMIT 1)`（queries.go:1839-1844）：可用 916 表达式索引 `((CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END), ts DESC)`（`916_session_turns_api_key_ts_index.sql:79`，分区逐个 CONCURRENTLY + hot 单独建）⇒ 有序扫 LIMIT 1，非全表扫。P3：无时间上界，历史全量按键索引扫，量大时建议改 JOIN api_keys 表取别名。
  - `total_cost = SUM(cost_display)+SUM(cost_usd)`（:1692-1693）= 美元+积分跨单位相加——与同族 GetModelRanking `SUM(internal_cost + external_cost)`（queries.go:1512）同病，AGENTS.md 2026-11-20 统计轮 **L1 已登记**（根修须动 API 契约）。窗口内未改坏，按存量口径处理。

---

## 五、Q5 handlers 两端点（F21 + Go↔TS↔Vue 三端对拍）

### 5.1 F21 错误日志

- `HandleGetUserModelUsage`：`slog.Error("sessionAnalytics: user model usage failed", "owner", owner, "error", err)` + 固定文案 `"failed to get user model usage"`（`admin/session_analytics/handlers.go:1184-1185`）✅
- `HandleGetUserAPIKeyUsage`：同款（handlers.go:1234-1235）✅
- 均不回显 `err.Error()` 给客户端；参数解析错误回显的是 `errorf` 固定文案串（`parseUserDetailParamsFromRequest`，无内部错误拼接）⇒ 无泄漏面。判定 **F21 修复属实**。

### 5.2 三端契约对拍（逐字）

| 契约点 | Go | TS | Vue | 判定 |
|--------|----|----|----|------|
| 容器键 models | `json:"models"`（queries.go:1615） | `models: UserModelUsage[]`（sessionAnalytics.ts:476） | `res?.models`（UserDetailView.vue:502） | ✅ |
| 容器键 api_keys | `json:"api_keys"`（queries.go:1724） | `api_keys: UserAPIKeyUsage[]`（sessionAnalytics.ts:517） | `res?.api_keys`（UserDetailView.vue:514） | ✅ |
| UserModelUsage 10 字段 | queries.go:1600-1611 | sessionAnalytics.ts:462-474 | vue:252-258（model/request_count/total_tokens/total_cost/success_rate/cache_hit_rate/avg_response_time_ms/last_used_at 均消费） | ✅ 逐字一致 |
| UserAPIKeyUsage 10 字段 | queries.go:1709-1720 | sessionAnalytics.ts:503-515 | vue:286-294（key_id/key_alias/request_count/total_tokens/total_cost/project_count/top_project/last_used_at 均消费） | ✅ 逐字一致 |
| 比率单位 | 0~1（SQL AVG CASE） | number | `percent()` ×100（vue:442-445） | ✅ 自洽（跨族不一致见 R-L1c） |

P3 附注：`withTimeout`/`defaultQueryTimeout` 定义未用（handlers.go:1133/1138，包族现象非本窗口引入）；两 handler 未包查询超时（同族一致，依赖 r.Context() 与服务端 statement_timeout）。TS 的 internal_cost/external_cost 字段 Vue 未渲染（只显示 total_cost）——契约位保留，无缺陷。

---

## 六、Q6 taskaggregator F25 复核

`git diff f712ea685^ f712ea685 -- domains/taskaggregator/task_aggregator.go`：

- `FROM request_logs` → `FROM request_logs_with_current_month rl`（42P01 根修，:110/:115）✅
- `SUM(internal_cost_usd + external_cost_usd)` → `SUM(COALESCE(rl.cost_display,0) + COALESCE(rl.cost_usd,0))`（:114-117）——成本键映射符合 994/matrix 家族口径 ✅
- 列契约：gw_session_id/tenant_id/cost_display/cost_usd 均在 890 投影 ✅；无维度丢失（其余 SELECT 列不动）
- 驱动形态：pgxpool（pgx v5 原生协议）扫 text[]（key_topics/tags）——**不走 database/sql stdlib 分支 ⇒ 不受 P0-1 数组 Scan bug 影响** ✅
- 判定 PASS。但该包当前零调用方（§三）——修复对象是死代码；接线时的前置项（相关子查询批量化、LLM 成本闸）见 §3.3。

---

## 七、Q7 v1 退役门实跑

```
$ go test ./admin/ -run 'Retirement' -count=1 -v
--- PASS: TestResetCredentialSuccessRateRowsIsANoOpAfterV1Retirement
--- PASS: TestHotPromoteRetirementEndpoints / GuardIsUnconditional / ...（hot promote 系 5 条）
--- PASS: TestRetirementListedColumnsHaveAProductionReader (6.75s)
--- PASS: TestRequestLogsRetirementExposure
--- PASS: TestRetirementExposurePopulationIsEveryKnownReader
--- PASS: TestRequestLogsRetirementBreakersRegistryIsConsistent
--- PASS: TestRequestLogsRetirementRepointVerdict
--- PASS: TestProbeRetirementExposureBlindSpotFiles (45.54s)
--- PASS: TestSessionRetirementHTTPGuards
ok  github.com/kaixuan/llm-gateway-go/admin  53.560s
```

14 条全绿，**0 offender**。两个新端点 SQL 与 taskaggregator 均读 canonical 视图 ⇒ 不在 v1 暴露面（Exposure 门实测放行）。

---

## 八、Q8 R-L1c / R-L1d 现状确认

- **R-L1c（属实，未改坏）**：`userModelUsageSQL` success_rate/cache_hit_rate = 0~1 比率（queries.go:1696-1697，AVG CASE 1.0/0.0），注释自述登记（:1685-1686「与同族 GetModelRanking 的 0~100 百分比口径不同」）；`GetModelRanking` = ×100（queries.go:1505/1511）。前端本页 `percent()` ×100（vue:442-445）与 0~1 契合 ⇒ 页面内部自洽、跨族不一致仍在。**建议**后续轮统一到一端（改 SQL 侧 ×100 比「前端按端点分支」便宜），属 i18n/口径列车。
- **R-L1d（属实，未改坏）**：`contract_test.go` SQL 列存在性钉测表只钉 topUsersSQL/userRankingSQL/userRankingByProjectSQL（contract_test.go:98-100）⇒ 两条新常量不在表内；`costPinnedTypes` 9 类型（contract_cost_fields_test.go:31-44）不含 `UserModelUsage`/`UserAPIKeyUsage`——两者都带 total_cost/internal_cost/external_cost 三键，而文件头自述「新增带三 cost 字段的类型时应加进本表（漏加=该类型字段形状无 CI 保护）」⇒ 按其自己的规则该加未加。**建议**：本轮即可补（机械改动，两处各 +2 行），不必等下一列车。

---

## 九、Q9 定向门实跑记录

| 门 | 结果 |
|----|------|
| `go build ./admin/... ./domains/taskaggregator/ ./domains/layeredsummary/ ./domains/sessiondedup/` | ✅ BUILD OK |
| `go list -deps ./cmd/gateway` ∩ 三包 | 空（三包不在依赖图——P1 证据） |
| `go test ./admin/session_analytics/` | ✅ ok 0.038s |
| `go test ./admin/ -run 'Retirement'` | ✅ ok 53.6s（14 条） |
| `go test ./internal/sqlguard/ -run 'Delivery|Ledger|Continuity|LockGuard|Startup'` | ✅ ok（6 条 PASS，含 TestStartupMigrationsStayInEveryDeliveryChannel） |
| `go test ./installer/...（installer 子模块）-run 'StartupFiles|StartupMigrations|Embed|Delivery'` | ✅ ok（12+ 条 PASS，含 ByteMatchCanonical / AboveCutoffHaveLockGuard / CanRunInSingleTransaction / StartupFilesAreAllEmbedded） |
| `go test ./cmd/gateway/ -run 'TestSessionAnalyticsRoutes'` | ✅ ok（2 条：路由不冲突 + 生产同形 mux 钉测） |

---

## 十、分级汇总与归因

### P1（1 项）
1. **997/998/999 写链死透——三包零接线、两列零写代码，迁移却将随九面登记上生产**。
   证据：import 全仓 0 命中；`go list -deps ./cmd/gateway` 不含三包；`session_summaries.task_id`/`has_layered_summary` 全仓无写方；对照 errorplaybook（cmd/gateway/error_playbook_init.go:39）与 sessionanalytics（cmd/gateway/main.go:8549）均已接线。
   归因：**窗口内引入**（0f545826a 交付 schema+包但声明「集成到 SessionSummaryWorker」未做；f712ea685 补齐投递登记把「表上生产」变成确定事件，但未在台账行/主档点名「功能不存在、表将空转」的错位）。
   建议：① 在 997/998/999 台账行与审计主档补「表先于功能上线、当前零写方零读方」登记；② 接线（SessionSummaryWorker 集成）落地前不把 997-999 从 pending deploy 推进；或接受空表上线的低成本并明确登记。P1 的实害是「审计链错位 + 空转面」，不是运行期故障。

### P2（2 项）
2. **997/998/999 缺 per-migration sqlguard delivery test**（九面口径第⑨面缺位，994/995/996 同目录都有）。归因：窗口内 f712ea685 补登时未补。建议照 migration_996_delivery_test.go 形态补三份。
3. **999 session_summary_layers 未入 hot+分区纪律**：window 层行级 DELETE（layered_summarizer.go:413-421）+ segment/meta 永久累积 TEXT 无界，百万行级潜在体量与「更新删除只在 hot」纪律相悖。归因：窗口内引入（0f545826a），缓解=当前 0 行。建议接线前重估（按 candidate_failure_logs 392 月分区先例或 hot 独立表）。

### P3（6 项，登记不修或接线前处理）
4. `idx_summary_layers_session` 与 `uq_session_layer` 唯一索引**同列同序完全重复**（999:67-77）——写放大白付一份。
5. `canonical_session_key VARCHAR(128)` vs 同值域 `session_summaries.session_key VARCHAR(255)`（998:48 / 310:9）——类型长度不对称暗坑。
6. `tasks` PK 全局不 scope tenant + `EnsureTaskExists` ON CONFLICT 不更新 tenant_id（task_aggregator.go:268-273）——ACC id 跨租户复用会串租户归属。
7. owner 裸 `COALESCE` 与 994 视图族 `NULLIF(COALESCE(...),'')` 的家族口径差（按具体 owner 查询等价；owner='' 行归属不同）——与 user_profile.go:968 同形，建议家族统一时一起拍板。
8. LATERAL 别名查询无时间上界（queries.go:1839-1844）——916 表达式索引兜底非全表扫，量大后建议改 JOIN api_keys。
9. `withTimeout`/`defaultQueryTimeout` 死代码 + 两 handler 无查询超时（handlers.go:1133/1138）——包族存量。

### 修复质量确认（窗口内 f712ea685 对本路子的 5 笔修复，全部属实）
- F2 两个新端点 SQL 切 canonical（queries.go:1687-1845）✅ 且逐列契约正确
- F21 两 handler Error 日志不回显 ✅
- F25 taskaggregator 切 canonical ✅
- F10 canonical 读点登记簿补登 2 条（registry test :740-755）✅
- 九面投递登记 + 锁守卫 + 台账 SHA ✅（八面实测全对，仅第⑨面缺）

### 窗口内引入 vs 基线存量
- 窗口内引入：997/998/999 迁移本体、三包死链、P2-1 缺 delivery test、P2-3 999 分区欠账、P3-4/5/6。
- 基线存量：R-L1c 两族比率口径、R-L1d 钉测缺位（1621daffe 引入、f712ea685 登记）、total_cost 跨单位相加（2026-11-20 L1）、withTimeout 死代码。

---

## 十一、通用式沉淀

1. **「投递链补齐」≠「功能上线」——补的是表的通道，不是功能的调用方**。九面登记让空表必然上生产，登记故事里必须同时写「写链是否存在」，否则台账行的语义是「能部署的表」，读者的理解是「可用的功能」。
2. **修死代码的 SQL 是零收益修复**（F25 修对了但调用方为零）——修摄取链前先 `go list -deps` 确认链在依赖图里；P0-1（errorplaybook）与 F25 的差别正是前者在图里后者不在。
3. **依赖图是「死面」的唯一真源**——包目录存在、单测存在、甚至被 registry test 提及，都不等于 `go list -deps ./cmd/gateway` 里有它。
4. **部分索引的谓词是数据态的函数**——`WHERE similar_session_keys != '{}'` 在零写方世界里是恒空索引，`WHERE has_layered_summary=FALSE` 在全 FALSE 世界里是全表二级索引；上表前先算谓词在当前数据态下的选择性。
5. **契约三端对拍要把容器键当一等公民**（b5eb7d812→f712ea685 的 keys/api_keys 往返已证明）；本轮实测三端逐字一致，钉测面由 contract_cost_fields_test 承担但漏了两个新类型（R-L1d）。
