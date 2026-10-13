# 2026-10-14 S4 退役改名轮 —— request_logs 族 `_` 前缀待删除标志

> 会话目标（用户指令摘要）：审计 request_logs→session_turns 双写转换的收尾——
> 写入关停核验、物化视图/API 读源核验、性能核验、**把 request_logs 及相关表
> 改名为下划线前缀作为待删除标志**、逐 API 单测、SQL 变更进部署序列、
> 本地重部署 + 模型多轮请求验证 + 浏览器逐页验证，提交推送。
> 本文档覆盖：存储可用性重审计、族对象全量盘点、迁移 860 设计、代码清扫、
> 判据与门同步、已知边界。

## 一、存储可用性重审计（真库实测，2026-10-14 03:2x）

| 项 | 值 | 判定 |
|---|---|---|
| `storage.request_logs_write_enabled` | `false` | 停写 ✓ |
| `storage.session_bodies_native_read` | `true` | 会话族原生读 ✓ |
| `_改名前_ request_logs / request_logs_bodies` | 各 35,506 行，max_ts=2026-10-13 07:01:04 | 冻结点与开关翻转逐秒吻合 |
| request_logs_hot / bodies_hot | 0 行 | 停写后零新行 ✓ |
| session_turns / session_bodies | 各 18,169 + hot 2,965，max_ts=取数时刻 | 实时推进，turns=bodies 全覆盖 |
| canonical 视图 | 44,930 行，`dup_request_ids=0` | 双臂无重复 ✓ |
| 8 个物化视图 | **0 个**引用 request_logs 族（含视图名） | MV 已全会话/其它源 ✓ |

结论：存储面可用性成立，改名（删除前置标志）可以执行。

## 二、族对象全量盘点（改名输入清单）

**改名（物理关系）**：`request_logs`、`request_logs_hot`、`request_logs_bodies`、
`request_logs_bodies_hot`、`request_logs_archive`、序列 `request_logs_id_seq`
→ 各自加 `_` 前缀。

**不动（有意）**：
- canonical 视图名（`request_logs_with_current_month`、
  `request_logs_bodies_with_current_month`、`request_logs_bodies_progress`、
  两个 `*_without_*` 变体）——它们是兼容读层，PG 视图按 OID 引用基表，
  改名自动跟随（迁移后 `pg_get_viewdef` 显示新名，零 DDL）；
- 月分区/归档子表（`request_logs_2026_10`、`request_logs_archive_2026_08`、
  `request_logs_default` 等）与全部索引名——经（已改名的）父表继承删除标志；
- 函数名（`promote_request_logs_*` 等 26 个）——名字是 Go 侧调度契约，
  迁移内改写**函数体**指向新表名。

**盘点结论**：FK 指向族表 = 0；族表触发器 2 个（request_logs_hot 上的
session summary 同步对，随表走，停写后休眠）；引用族表的函数 26 个
（promote/archive/ensure/backfill/maintenance/read helper 族）。

## 三、迁移 860（canonical + installer 五点 + 通道腿 + down）

`sql/migrations/startup/860_rename_request_logs_family_pending_delete.sql`：

1. **改名**：每关系一个守卫 DO 块，EXECUTE 字面量里带**完整静态**
   `ALTER TABLE public.<old> RENAME TO _<new>` 语句。静态形态是承重的：
   admin 侧 `liveRelations` 对 sql/migrations 做**文本重放**，靠解析
   RENAME 语句得知「旧名死、新名活」——动态 format('%I') 拼装对它不可见。
2. **函数体改写**：`pg_get_functiondef` 往返 + 词边界正则替换五 token
   （lookbehind 排除 `_`（函数名内 token）、lookahead 排除 `[a-zA-Z0-9_]`
   （视图名/动态分区前缀））。26 个函数全部改写。
3. **后置断言**：改写后 public schema 里**不允许**再有任何函数体引用
   未加前缀的族名；`_request_logs` 必须存在。响亮失败，不静默。
4. **幂等**：新名已存在且旧名不在 → skip；两名并存 → EXCEPTION。
5. **down 迁移**：反向改名 + 反向函数体改写。**仅在族被真正 DROP 前有效**。

登记（五点 + 通道腿全数完成）：installer embeddata 副本、main.go
go:embed + embeddedSQLFiles map、dbinit StartupFiles、manifest tsv 重生
（255→256）、`scripts/apply-db-revision-sequence.sh` files 数组（该测试 rc=0）、
docs/db-changelog.md（SHA-256 已记）。

**部署语义**：新二进制 boot 时先跑启动迁移（含 860）再跑视图自愈
（db/db.go:228 顺序），单次部署内原子生效。蓝绿切换窗内旧实例对改名后
的表读会短暂报错（写已停，无写竞争）——本地秒级，生产按维护窗执行。

## 四、代码清扫（token 边界规则 + 两轮）

**第一轮（基表 token）**：`(?<![A-Za-z0-9_/])TOKEN(?![A-Za-z0-9_.])`
→ `_TOKEN`，五 token。排除 installer/、storage/sqlite/、sql/、docs/、web/。
- lookbehind 排 `_`/`/`：函数名（promote_request_logs_*）、文件路径串
  （"admin/request_logs.go" 登记键）；
- lookahead 排 `[A-Za-z0-9_.]`：视图名（`_with_current_month` 尾）、
  动态分区前缀（`request_logs_` || 月）、路径后缀（.go）。
- 295 个非测试文件 + 228 个测试文件 + 92 个 scripts/ 运维脚本。
- 点号前置**放行**（`public.request_logs` 是 SQL schema 限定）；全仓
  grep 证明非 public. 的点号前置形态只存在于注释。

**第二轮（列限定词补扫）**：第一轮的点号后置保护（防路径）漏掉了 SQL
列限定词 `request_logs_hot.列名`（telemetry client.go 41 处——UPDATE 目标
已换新名而 WHERE 限定词还是旧名 = 运行时 `missing FROM-clause entry`）。
补扫规则：`TOKEN\.(?!扩展名白名单)`，130 文件 303 处。`.ts` 命中经查
全部是 SQL 列（RETURNING request_logs_hot.ts），无路径冲突。

**SQLite 平面豁免（第一轮误伤，已回退）**：`bg/lite_retention_worker.go`
（`DELETE FROM request_logs ... rowid`）与 `cmd/gateway/lite_telemetry_sink.go`
是 **lite 模式 SQLite 单文件库**——它的 request_logs 表由 SQLite 侧 DDL 建立，
不随 PG 迁移 860 改名。两文件回退到清扫前状态；判据侧（population/分档）
本就把 SQLite 平面列为另一存储面（sessionFamilySQLiteFiles 等既有豁免）。

**生命周期作业挂停写门**：`bg/partition_manager.go` 新增
`v1LifecycleRetired()`（= `!settings.RequestLogsWriteEnabled()`，热加载）：
- `ensureSpecs()` / `promoteSpecs()` 滤掉 v1 族四条（不再给冻结族预建
  未来分区 / 排空恒空 hot 表）；
- `archiveOldRequestLogs` 早退（不再往待删族写归档摘要）；
- 保留 `drop_old_request_logs_bodies_partitions`（retention DELETE——
  唯一真正在缩小待删负载的作业）。
判据 `bg/partition_manager_v1_retirement_test.go`（count==N 双向 + 变异
验证：门恒 false 时 4 条断言红在正确位置，还原后绿）。

## 五、判据与门同步（本轮动过的扫描器）

原则：**两代物理名并存**——源码用前缀名，SQLite 侧/历史迁移文本/基线
DDL/sql-objects dump 用旧名；所有 token 闭集与正则同时认得两代。

| 门 | 改动 |
|---|---|
| `v1BaseTableRe`（population） | `from\s+(public\.)?_?request_logs(后缀闭集)` |
| `requestLogsReadPattern`（inventory） | `(from\|join)\s+_?request_logs(后缀通配)` |
| `v1BaseTableNames` | 5 旧名 + 5 前缀名并存 |
| `liveRelations` | 新增 RENAME 正则解析（ALTER TABLE a RENAME TO b → a 死 b 活） |
| `viewChainNames`/mk/drop 正则 | `(_?request_logs[a-z0-9_]*)` |
| `isV1Relation`（tiering） | 闭集加 5 前缀名（保旧名：SQLite 侧证据） |
| `familyBodiesRE`/`familyBaseRE`（stop-write 分类） | `_?` 可选前缀 |
| `bareRequestLogsRe`（sqlreadguard） | `_?` 两代都拦；白名单加 summarystore LEGIT 条目；**顺手收口 origin 既存红**（quality_correlations 条目滞留——pristine worktree 复现实锤，S4 波 4 重写后忘删），棘轮下限 18→17 |
| partguard `partitionParents` | 两代名单；`TestParentsAreDeclaredInDDL` 加 RENAME 链感知 |
| `viewDumpVerdict`（db） | v1 两臂判定接受两代名（dump 件是改名前世界） |
| pgxmock 期望（body_resolver / credential_success_rate_reset / telemetry final_success_claim 等） | 与源码同向翻转到前缀名 |
| Evidence 逐字登记（control-plane / stop-write） | probe_history 改锚视图名；sqlite 条目保旧名并注明另一存储面 |
| `v1BodiesRelationRE`（bodies read-path） | `\b_?request_logs_bodies(...)` |
| 表面正则（passive_probe / recent_surface） | `FROM\s+_?request_logs(\w*)` |

**清扫误伤清单（全部已修）**：测试正则被同轮清扫前缀化后再叠 `_?`
造成双前缀（`_?_request_logs`）3 处；字符类邻接形态被改（`(_request_logs[a-z...`）
4 处；`v1BaseTableNames` 旧五名被测试清扫前缀化（两代并存失效）1 处；
正则捕获组内容翻转致视图链推导为空 2 处。这类误伤的共同教训：
**对「扫描器即测试」的文件做机械替换时，字符类/正则元字符邻接的 token
是新的盲区**——单测-清扫后必须跑全部门并逐个核对失败信息（本轮实际
32→24→6→2→0 红的收敛轨迹即证据）。

## 六、已知边界与登记

1. **旧实例切换窗**：迁移 860 应用与新二进制就绪之间，旧实例读会短暂
   报错（表已改名）。本地实测秒级；生产按维护窗执行（runbook 注记）。
2. **不建兼容视图**（`CREATE VIEW request_logs AS SELECT * FROM _request_logs`
   的三明治方案被否决）：它会掩盖残留依赖——改名的目的就是让漏网读方
   **响亮失败**而不是静默走视图。
3. **分区子表名/索引名不带前缀**：经父表继承删除标志；未来
   `ensure_request_logs_partition` 若被重新启用（回滚写路径）会建出
   旧命名的新子表——功能正确，命名风格混杂，登记不修。
4. **fresh install 路径**：installer 重放 1..860，基线 DDL 建旧名、860
   改名——终态一致。deploy-local 的 fresh-schema 探测已同步新名。
5. **回滚语义**：`.down` 迁移只在族 DROP 前有效；回滚写路径
   （write_enabled=true）无需改名回滚——写代码已指向 `_` 前缀表。
6. **生产推进**：生产 245/154/252/211 共享/独立库尚未应用 860；下一次
   生产部署会经启动序列自动应用。生产前置（AGENTS 生产翻转节）不变：
   四象限复核 + owner 授权后才 DROP。
7. **SQLite/lite 平面完全不受影响**（表名/DDL/运维路径均未动）。

## 七、部署与端到端验证（2624，2026-10-14 04:2x-04:4x 实测）

**迁移落地**：5 表 + 序列全部前缀名、旧名 0 残留；canonical 视图 45,369 行
dup_request_ids=0 照常可查；26 函数体改写后旧名引用 0。

**模型多轮（用户指定 key sk-1vH6…）**：
- glm-5.3-flash ×5 轮（ping / say OK / 多轮会话 ×3）全响应；
- minimax-m2.7（中文指令）、deepseek-v4-flash（长提示词）全响应；
- 写路径：session_turns_hot 2,399→2,408 实时推进、最近 20 轮 20/20 有正文、
  **_request_logs_hot 恒 0**、canonical 视图 45,339→45,369 增长。
- ⚠ 该 key 行曾 enabled=false 且 key_hash 属旧密钥世代——本地库已按当前
  secret 重算哈希并启用（update 前 hash 前缀 217409a4… 与网关查询逐字核对）。

**浏览器 34 页遍历**（dashboard/request-logs/turns/sessions/compression/
data-lifecycle/usage/usage-trends/quality-correlations/correlations/routing×4/
probe-health/models(2896 个)/providers/keys(151 把)/tenants/users/system-monitor/
format-anomalies/output-compliance/session-analytics/maintain/free-discovery/
catalog/maas/tenant/audit-logs/request-detail/model-integrity）：实际加载
100-300ms（测得 1.27-1.48s 含 1.2s 固定等待），**无一超 2s**，数据页全部带
真实数据渲染。/maintain 占位为设计内（独立 nginx SPA）；/admin/settings 的
BAD 匹配是限流配置数值 500 的正则误报。

**API 直测**（JWT）：logs/stats、logs/archive/list、logs/body-cache-stats、
usage/tenants、routing/featured、routing/policy、sessions、compression/stats、
anomaly-reporter/stats、dashboard/board、turns/sessions、data-lifecycle/stats
全部 200，0.6ms-877ms。

## 八、补充验证轮（completion verifier 指出的缺口，2026-10-14 04:4x-05:0x）

### 8.1 浏览器剩余 59 条路由（累计 93 次访问，全仓 ~85 条声明路由全覆盖）

含参数化详情页（用真库 id：request e7f99a40…、session gw_478a…、provider 1、
credential 2、tenant default、key 2、work-type coding）。全部 <1.5s（含 1.1s
固定等待），零 >2s。数据页均带真实数据渲染（agents 注册表、request-registry
三态、reconciliation 13KB、journey 详情、connection-registry、prompt-injection、
model-pricing 191KB、pricing 28KB、maas/models 232KB、keys/2 密钥统计、
tenants/default、providers/1 等）。

**复核后的豁免登记（设计内占位/外部依赖，非缺陷）**：
- `/maintain`（含通配）：独立 nginx SPA（/maintain/ops/*），本地直连形态显示
  MaintainUnavailableView 占位——router.ts:306 明示。
- `/customer/*`（activate/agreement/license/site/update-activate）：五条路由
  同归 update-activate 视图；页面内「版本目录加载失败」是**中心注册服务**
  未在本地部署（页面文案自述「联网时将自动向中心注册」）。站点信息与
  激活流程 UI 本身渲染正常。
- `/admin/approvals/1`：approvals 表本地为空 → 80 字符空态（详情页对不存在
  id 的正常表达）。
- `/maas/pricing`、`/tenant/pricing`：支付渠道（支付宝/微信）为外部服务，
  套餐展示正常。
- `/nonexistent-page-xyz`：通配兜底重定向 dashboard ✓；`/forbidden` 渲染
  403 视图 ✓；`/login` 已登录态重定向 `/` ✓。

### 8.2 模型补测（4 模型 × 5 提示词，全部样例提示词实际发送）

| 模型 | ping | say OK | 拉取合并推送 | 批判式审计 | 磁盘清理 |
|---|---|---|---|---|---|
| grok-4.6 | ✅ Pong | ✅ OK | ✅ 195B 方案 | ✅ 358B | ✅ 1653B |
| claude-sonnet-5-5 | ✅ Pong! | ✅ OK. | ✅ 916B 步骤 | ✅ 889B | ✅ 1443B |
| mimo-v2.6-flash | ✅ | ✅ | ✅ 1102B | ✅ 657B | ✅ 2449B |
| gpt-5.6-luna | ✅ pong | ✅ OK | ✅ 420B | ✅ 521B | ✅ 1917B |

模型对「操作本机 git/磁盘」类提示词合理婉拒并给出步骤说明——验证的是
网关全链路（路由→上游→响应→写路径），模型无工具执行属预期行为。
首轮 3 模型（glm-5.3-flash/minimax-m2.7/deepseek-v4-flash）已在 §七 登记。

⚠ 量具注记：本地 key（id=2）`rate_limit_tpm=3`（测试残留值），真响应一
触发即 429 连坐——首轮 429 全部源于此，清 NULL 后串行重跑全过。并发跑
两个测试脚本会双倍消耗配额，教训同 r1024 判据纪律第 8 条（先证钉扎/先查
配额再归因模型）。

**写路径终验（补测后）**：session_turns_hot 2,408→2,612（+204），max_ts=
取数时刻；04:45 后 127 轮 × 18 个不同模型；`_request_logs_hot` 恒 0；
canonical 视图 45,369→45,573 同步增长。

### 8.3 全量 API 扫描（444 个注册路由）与三个真缺陷修复

从源码枚举全部注册路由（mux.HandleFunc + router 声明），替换路径参数为
真库 id 后 GET 扫描（SSE/流式 6 条剔除——已由浏览器 Live 页覆盖）：
**234×2xx、100×405（路由存在，POST-only）、62×400（缺必需参数，预期）、
33×404（无数据的参数化子资源/已退役路径）、9×5xx**。

9 个 5xx 逐一定因，其中 4 个为设计内 503（identity-pool / mobile-node /
lite-sessions（full 模式）/ memora 未配置——可选子系统的诚实不可用态），
**5 个里挖出三个真缺陷并全部修复（迁移 861 + 代码两处）**：

1. **`/api/candidate-failures` 42703**：`candidate_failure_logs_with_current_month`
   是 392 用 `SELECT *` 建的视图——PG 视图在建时快照列集，617 给两表后加的
   `session_id` 永远进不了视图。且两表列**顺序**已分叉（aggregation_id 位置
   不同），SELECT * 按位 UNION 本就是隐患。**迁移 861**：显式列清单重建视图
   （五点同步 + 通道腿 + down 说明 861 自逆）。修复后 200（100 条记录）。
2. **`/api/admin/session-analytics/users` NULL 扫描 500**：真库
   session_owners 含 owner_user 为 NULL 的行，裸 string 扫描炸、且
   pgx 的 rows.Err() 会携带该扫描错误让 writeAggRowsErr 抛 500。修复：
   三层隔离全部 5 个分支统一 `so.owner_user IS NOT NULL`（与
   COUNT(DISTINCT) 口径对齐）。修复后 200（4 个用户画像）。
3. **`/api/admin/session-audit/export` conn busy 500**：rows 声明在
   withTenantTx 闭包外、闭包返回即提交事务，连接带着未关闭 rows 归还池。
   修复：消费与 Close 全部挪进闭包（list :167 的正确形态）。修复后 200 CSV。

另：`/api/admin/session-audit`（列表）带 `?tenant_id=` 即 200（裸 GET 的
"empty tenant_id" 是 RLS 门的要求）；`/api/admin/session-crosstalk` 的
`session_tenant_binding` 缺表是本地库没重放 domain/032（手动域迁移目录
不在部署序列内）——本地已应用表体；**登记**：domain/ 迁移语料含旧表名
引用（032 的触发器体指 request_logs），860 后的重放需要同族改名清扫，
属下轮工作。判据：`admin/api_sweep_fixes_test.go` 两条形状钉桩，
变异验证（真历史形态 HEAD^ 版 session_audit.go 喂判据）双红、还原绿。

**慢端点榜**（>1s，均达标无需修）：memora-sessions 7.1s（外部 memora
服务）、data-lifecycle/degradation 2.8s、backups 2.7s、credential-success-
rates 1.9s、monitor-summary 1.8s、data-lifecycle/stats 1.3s。

**蓝绿窗实证**：日志中 20 条旧表名 42P01 全部落在 04:25:42–04:25:58
（16 秒迁移-交接窗，旧实例后台 worker 撞已改名表，全部有回退路径的
WARN），此后一小时零复发——改名清扫无漏网的运行时证据。
