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
