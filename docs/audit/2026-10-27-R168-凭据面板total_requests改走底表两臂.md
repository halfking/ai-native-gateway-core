# R168 · 凭据面板 total_requests 改走底表两臂窄投影（★ 推翻 R167 的一个决定）

> 日期：2026-10-27 · 承接 R167（抽屉长窗改走底表两臂）
> 原始目标第 4 条：「优化请求的列表、记录查询、写入过程的效率，**不要有表扫描或慢 SQL**的问题」

## §0 摘要

★ R167 留下了两个未了的口子：①「其余 ~94 处 canonical 视图读点未逐个评估」；②「写路径效率未审」。
★ 本轮做的是 **①**：**先量出哪些读点真的慢，再逐个修**。

★★★ **本轮最值钱的一条：★ R167 决定「`:383` / `:462` 实测不慢 ⇒ 不动」——★ 这个决定是错的。**
★ 错因不是纪律错，而是 ★★ **「不慢」的证据取自【本地 2722 行的小库】，不可推广到【252 的 111 万行】**。
★ ★★ 252 生产实测（只读，网关角色）：

| 形态 | Execution Time（单凭据 `credential_id=21`） |
|---|---|
| **A canonical 视图（现状）** | **14,728.010 ms** ★★ 单个凭据！ |
| B 原生 118 列 source | 389.618 ms |
| **C 窄投影 UNION ALL（本轮修法）** | **247.063 ms** ★★ **59.6×** |
| D 拆成两条独立 count | **statement_timeout** ★★ 最差 |

★ × 74 个凭据 ⇒ ★★ 整个端点 ≈ **7~18 分钟** ⇒ ★★ 触发角色级 `statement_timeout`。

★★★ **机理（★ 「plan 相同 ≠ 耗时相同」的第二次坐实，且这次落在【单次扫描】上）**：
★★ 两版的 plan **cost 逐字相同**（`cost=41.94..441.93 rows=391`），
★★ 唯一差异是 heap scan 的 **行宽**：视图形态 `width=3083`（118 列全物化）vs 原生形态 `width=32`
⇒ ⇒ **plan cost 里没有「物化宽度」** ⇒ ⇒ 「EXPLAIN 一样」不蕴含「代价一样」。

## §1 本轮定位与边界

- ★ 范围：**只改** `admin/credential_monitor.go` 的 `total_requests` 计数子查询（`:383`）。
- ★ **不改**：`:462` 的 live p95 LATERAL（★ 同族但需要 details 列 ⇒ 形态不同 ⇒ 下一轮）。
- ★ **不改 schema ⇒ 无迁移 ⇒ 不进部署序列** ⇒ 211 / 252 无需为本轮补任何 SQL。
- ★ 对 252 **全程只读**；★ 未连 211；★ 生产写 = 0。

## §2 量具选型：★★ 不要从源码猜「谁慢」，★ 要问数据库

★★ 面对「~94 处视图读点」，从源码逐个判断「这个查询慢不慢」是不可靠的（★ 判据既难统一，
★★ 又会重复 R167 那次「用小库读数否定生产慢点」的错误）。
★ ⇒ 本轮改用 **`pg_stat_statements`**（252 已装，`shared_preload_libraries` 含它）。

### 2.1 252 的 TOP 慢读点（按 `total_exec_time` 降序，前 6）

| calls | mean_ms | total_ms | 读点 |
|---|---|---|---|
| 18062 | **410.1** | 7,407,875 | `WITH policy AS (SELECT featured_models FROM routing_policy ...)` |
| 1556 | **468.5** | 729,012 | `SELECT client_model FROM request_logs_with_current_month rl WHERE rl.credential_id=$1 AND ts > now()-interval $2 AND success=$3 -- R50 dual-arm` |
| 22585 | 29.7 | 671,345 | `SELECT request_id, request_status FROM request_logs_with_current_month WHERE request_id = ANY($1) ...` |
| **1** | **556,812** | 556,812 | `with arm1 as (select c.id, (select count(*) from request_logs_with_current_month rl where rl.credential_id = c.id) ...)` ★★ **= `:383`** |
| 28815 | 16.5 | 475,952 | `SELECT EXISTS(SELECT ... FROM request_logs_with_current_month WHERE gw_session_id=$2 ...)` |
| 6 | **22,079** | 132,476 | `INSERT INTO maas_credit_consumption_buckets ...` |

### 2.2 ★★★ pgss 的两个坑（★ 本轮实测，必须写下来）

1. ★★ **`pg_stat_statements` 的统计窗口是 `stats_reset` 之后** ⇒ 252 实测
   `stats_reset = 2026-10-18 22:03:37` ⇒ ★★ **窗口 8.4 天** ⇒ ★★ **条目里的 query 文本可能来自【旧二进制】**
   ⇒ ⇒ ★ **「call count」不等于「当前仍在跑」** ⇒ ⇒ ★★ **pgss 只能当【候选清单】，每个候选必须【直接重测】**。
   ★★ 本轮 `:383` 的结论**不依赖 pgss** ⇒ 它是**我刚在 252 上直接 EXPLAIN ANALYZE 测出来的**。
2. ★ `pg_stat_statements.track = top` ⇒ **嵌套查询不记** ⇒ ★ 顶层慢查询掩盖了函数体里的慢查询。

★ ★★ **一个被否证的假设（★ 留档免下一轮重查）**：
★ pgss 里那条 468 ms 的 `R50 dual-arm` 查询曾让我怀疑「252 上的 `recent_success_rate` 函数体是陈旧的」。
★ ★★ **实测否证**：★ 252 上该函数**只有一个 overload**
★（`recent_success_rate(bigint,text,integer,integer)`）★ 且**函数体已是新版**（只读 `session_turns_hot`）。
★ ⇒ 「陈旧函数」假设不成立；★ 且 ★★ **252 上没有任何数据库函数的函数体读 canonical 视图**
★（`pg_proc.prosrc LIKE '%request_logs_with_current_month%'` 实测 0 行）⇒ ⇒ **DB 函数不是读点**。

## §3 ★★★ 批量替换的前提：★ 三条，全部坐实

### 3.1 视图体 ≡ 原生 source（★ 逐字）

★ 用仓内导入口 `db.CanonicalSessionOnlyDDLForTest()` 与 `db.SessionFamilyTurnsSourceSQL()` 对拍：

| 项 | 结果 |
|---|---|
| 视图 DDL 长度 / 原生 source 长度 / 导出投影长度 | 11097 B / 11011 B / 5319 B |
| 臂1 投影逐字相同 | **true** |
| 臂2 投影逐字相同 | **true** |
| 臂1/臂2 的 FROM/JOIN（归一化空白后）相同 | **true** |
| ★ 剥掉视图的 `CREATE … AS` 头 + 剥掉原生 source 的外层括号 ⇒ **全量逐字相同** | **true** |
| 视图含 `NOT EXISTS`（反连接） | false |
| 视图含 v1 族（`_request_logs`） | false |
| 导出投影的顶层列数 | **118** |

⇒ ★★ **「视图 = 两臂 118 列投影」是逐字事实，不是推断。**

### 3.2 ★★ 252 生产行集等价（★ 以网关自己的角色，全列双向差集）

★ 对拍 SQL **由 Go 侧生成**（★ 投影逐字来自 `db` 包 ⇒ 避免手抄 118 列出错）。

| 角色 | count_view | count_native | only_in_view | only_in_native | 按 credential 分组差集 |
|---|---|---|---|---|---|
| **`llm_gateway`（网关运行角色）** | 9070 | 9070 | **0** | **0** | **0 / 0** |
| `postgres`（superuser，绕过 RLS） | 9072 | 9072 | **0** | **0** | **0 / 0** |

★ 窗口 = 最近 1 天；★ 两侧在**同一条语句、同一快照**内；★ 两个角色的行数差 2 = 并发写入。

### 3.3 ★★★ RLS 与「视图按属主权限执行」——★ 等价性的一条硬前提

★ PostgreSQL 的**视图按【属主权限】执行**；★ 而基表**开了 RLS** ⇒ ★★ **「读视图」与「直读底表」的 RLS 上下文，
★★ 只有在【连接角色 == 视图属主】时才相同**。

| 环境 | 视图 RLS | 视图属主 | `session_turns` | `session_turns_hot` | `session_turn_details` | `session_turn_details_hot` |
|---|---|---|---|---|---|---|
| 本地 | false | `llm_gateway` | RLS=true | **RLS=false** | RLS=true | RLS=true |
| **252** | false | `llm_gateway` | RLS=true | **RLS=true** | RLS=true | RLS=true |

★★ ★★ **本地与生产的 RLS 形态不同**（★ 252 的 `session_turns_hot` 也开了 RLS，
★★ 且多出两条 **RESTRICTIVE** 的 `session_turns_owner_filter` / `session_turns_hot_owner_filter`）。
★★ ⇒ ★★ **在本地做的等价性验证不能直接代表生产** ⇒ ★ 必须到生产复核。
★★★ **252 的网关连接角色实测 = `llm_gateway`**（★ 两条独立证据：DSN
`postgres://llm_gateway:***@172.16.2.210:5432/llm_gateway`；★ 进程列表 `postgres: llm_gateway llm_gateway`）
⇒ ★★ **== 视图属主** ⇒ ⇒ **两条路径的 RLS 上下文相同** ⇒ ⇒ **等价性成立且不依赖角色**
⇒ ⇒ ★★ **并被 3.2 的双角色差集 0 反向印证**（★ 若 RLS 对网关角色有过滤，两角色行数会不同）。

### 3.4 ★★ 仓内那道「禁迁」守卫的**旧前提已过期**

★ `admin/logs_turns_source.go` 与 `admin/session_view_dependency_risk_test.go` 曾写：
★★ 「视图里 27.6% 的 request_id 在原生源查不到 ⇒ **按 request_id 反查或全量时间窗聚合禁止迁**」。
★★ ★★ 那条理由的**前提是「v1 还在」** ⇒ ★ 迁移 890 之后 canonical 视图**本身就是**会话族两臂体
⇒ ⇒ ★★ **「视图 vs 原生源」这个区分已经没有对象** ⇒ ⇒ ★★ **可迁的读点比旧规则允许的多**。
★★ ★★ 该守卫已于 2026-10-20 更新过（不变式换成「**读的源必须覆盖会话族的两条腿**」）⇒ ★★ **本轮的改动符合它**。
★★ ★★ 尚存的两条（`no_topic_session.go` / `session_title.go`，理由是「header-less 流量只会出现在视图里」）
★★ ⇒ ★★ **它们的前提也已过期**（★ 890 后视图无 v1 腿 ⇒ 两侧同样缺这类行 ⇒ ★ 3.2 的差集 0 即是证据）
⇒ ⇒ ★★ **列为下一轮的复查项，本轮不动**（★ 属他人工作面 + 需逐条验）。

## §4 修法

### 4.1 `db` 包新增窄投影两臂 helper

`db/request_logs_view_schema.go`：

- `SessionFamilyCredentialIDExpr` —— credential_id 的**唯一**规范表达式
  （★ 与视图投影里的那一项**逐字相同** ⇒ 与迁移 729 的表达式索引**同形**）。
- `SessionFamilyCredentialCountSourceSQL(credExpr)` —— 返回
  `(SELECT t.credential_id FROM public.session_turns_hot t WHERE <expr> = <cred>
   UNION ALL
   SELECT t.credential_id FROM public.session_turns t WHERE <expr> = <cred>)`
  （★ **窄投影** ⇒ heap scan 的行宽从 3083 B 压到 32 B；★ **不带 details 腿** ⇒ 计数不需要它）。

★ 形态对齐仓内既有先例 `db.SessionFamilyLatestTurnPerSessionSQL()`（★ 同款窄投影两臂）。

### 4.2 `admin` 侧替换 + 参考臂 + 毒丸标记

- `admin/credential_monitor.go`：`total_requests` 的表达式改为
  `COALESCE((SELECT COUNT(*) FROM <helper("c.id")> q), 0)`。
- `monitorSummaryTotalRequestsCanonicalArmSQL()`：**参考臂**（视图形态），★ **仅供对拍，生产不得引用**。
- `monitorSummaryTotalRequestsCanonicalArmMarker` = `/*r168-canonical-arm*/`：
  ★★ **参考臂的产物带唯一标记** ⇒ ★ **生产 SQL 里不得出现它**（★ 见 §7 的洞）。

### 4.3 ★★ 口径等价性的四条前提（★ 缺一条就不能换）

1. 视图的 `credential_id` 列 = `db.SessionFamilyCredentialIDExpr`（★ 逐字相同 ⇒ 谓词逐字相同）；
2. ★ 视图的 details 腿是 LEFT JOIN，★ details 唯一键是 `(tenant_id, request_id, partition_date)`
   ⇒ **同一键最多 1 行** ⇒ ⇒ **LEFT JOIN 不改变行数** ⇒ ⇒ 计数等价；
3. 两臂不重叠（★ promote 是**移动**不是复制）⇒ ⇒ UNION ALL 不重复计数；
4. 视图 = hot ∪ 母表两臂体（★ 890 后无 v1 冻结分支）⇒ ⇒ 视图与原生 source 的**行集**相同（★ §3.2 实测）。

★ ★★ **252 生产逐凭据对拍（★ 单语句同一快照）**：

| cred | view_cnt | narrow_cnt | equal |
|---|---|---|---|
| 1 | 0 | 0 | **t** |
| 2 | 25,852 | 25,852 | **t** |
| 10 | 0 | 0 | **t** |
| 21 | 132,075 | 132,075 | **t** |
| 37 | 23,326 | 23,326 | **t** |

## §5 守卫（6 条）

`admin/credential_monitor_total_requests_test.go`：

1. `TestMonitorSummaryTotalRequestsUsesNarrowTwoArms` —— 生产形态**不得**再读视图；★ coreMode 的
   `0::bigint` 是**另一个形态**（core 模式刻意不算总数）⇒ 单独断言。
2. `TestMonitorSummaryTotalRequestsArmsPositionallyAligned` —— helper 的两臂**除表名外必须逐字相同**
   （★ 投影列必须**恰好**是 `t.credential_id`；★ 谓词必须逐字等于 `SessionFamilyCredentialIDExpr`）。
3. `TestMonitorSummaryTotalRequestsCanonicalArmReadsView` —— 参考臂**必须**仍读视图（★ 否则对拍变成自己和自己比）。
4. `TestMonitorSummaryTotalRequestsCanonicalArmIsTestOnly` —— AST 静态门：★ 必须同时认
   `*ast.SelectorExpr`（跨包）与 `*ast.Ident`（同包）★ 两种 Fun 形态（★ R167 的 M3 就是这个坑）。
5. `TestMonitorSummaryTotalRequestsHasNoCanonicalArmMarker` —— ★★ **毒丸标记门**（★ 名字与标记两种形态都拦）。
6. `TestMonitorSummaryTotalRequestsMatchesCanonicalView_RealDB` —— ★ 真库逐凭据对拍
   （★ 窄投影一侧**直接调用生产 helper** ⇒ ★ 对拍测的正是生产真正用的那串 SQL；
   ★ 参照侧是**手写**的视图形态 ⇒ ★ 两侧独立；★ 断言「对拍了 0 个凭据 ⇒ 门恒真 ⇒ 报红」）。

## §6 ★★★ 真仓变异臂 7 臂（★ 全部检出，还原后 md5 逐字一致）

| 臂 | 注入 | 期望红 | 结果 |
|---|---|---|---|
| M1 | helper 两臂退化成「读 canonical 视图」（★ 真实回归：有人「简化」helper） | 用窄两臂门 | ✔（3 条红） |
| M2 | helper 两臂都读 hot（★ 漏掉已 promote 的行） | 两臂对齐门 | ✔（2 条红） |
| M3 | helper 谓词丢掉 CASE 归一（★ 与 729 索引不同形） | 两臂对齐门 | ✔（2 条红） |
| M4 | helper 投影多取一列（★ 窄投影退化成宽投影） | 两臂对齐门 | ✔ |
| M5 | 把参考臂**名字**当字面文本塞进生产 SQL（★ AST 门看不见的那种接法） | 毒丸门 + 视图文本门 | ✔（2 条红） |
| M7 | 把参考臂当 **Go 表达式**接回生产（★ 产物带毒丸标记） | 毒丸门 | ✔（3 条红） |
| M6 | helper 谓词算错（★ 口径漂移） | 真库对拍门 | ✔ |

★ 每臂还原后 md5 逐字一致（`db` 侧 `1cb558275dd92249604d02ece4583f06`；`admin` 侧 `ec3e596a14c391e9a026262db98d111b`）。

## §7 ★★★ 变异臂抓出来的三个洞（★ 本轮最诚实的一段）

★★ **第一个洞（★ 门有真洞）：投影断言用了 `Contains`**
★ ⇒ `"SELECT t.credential_id, t.ts"` 也 **Contains** `"SELECT t.credential_id"` ⇒ ★★ **宽投影退化检不出**
⇒ ⇒ ★★ **门已修**：投影断言改成**精确比对**（`legSelectList` 取 SELECT 列表后 `==`）。

★★ **第二个洞（★ 门有真洞）：AST 门看不见「拼进 SQL 字符串」的接法**
★ ⇒ 参考臂若被当**字面文本**或**Go 表达式**拼进 SQL 字符串，★ **AST 只看 Go 表达式 ⇒ 看不见**
⇒ ⇒ ★★ **补第三判据：毒丸标记**（★ 参考臂的产物带 `/*r168-canonical-arm*/` ⇒ 生产 SQL 不得含它）
⇒ ⇒ ★★ 并让门**同时拦「名字」与「标记」两种形态**（★ M5 与 M7 各覆盖一种）。

★★★ **第三个洞（★ 量具缺陷 —— 我自己的）：变异臂自己也会有「假失败」**
★★ ★★ 第一版变异臂里 **M1 / M5 报「门没红」，实则是【包编译不过】**
★★ （★ 变异删掉了唯一的 `dbpkg` 使用 ⇒ 导入未使用 ⇒ 编译失败 ⇒ ★★ **没有任何 `--- FAIL:` 行**
★★ ⇒ ★★ **「编译失败」与「门没红」在报告里长得一模一样** ⇒ ⇒ ★★ **量具必须区分两者**）。
★★ ★★ 另两处量具缺陷：
★★ ① **真库对拍门缺 `TEST_DATABASE_URL` 会被 skip ⇒ 恒绿** ⇒ ★★ 变异臂必须把 DSN 带上；
★★ ② **同一段文本在仓里出现两次** ⇒ `str.replace` 默认改**第一个** ⇒ ★★ M1 一度改到了
★★ `SessionFamilyLatestTurnPerSessionSQL`（★ 另一个函数）⇒ ⇒ ★★ **必须改【最后一次】出现**。

★★★ ⇒ ⇒ ★★ **通用式：★★ 变异臂必须【能编译】⇒ ★⇒ 否则它测的是【编译器】而不是【门】；
★★★ ★★ 且量具必须区分【编译失败】/【测试失败】/【skip】三态 —— ★ 三者都能伪装成「门没红」。**

## §8 读数速查

```
# 252 生产 · 网关角色 · 单凭据计数（credential_id=21）
视图（现状）            14,728.010 ms
原生 118 列 source        389.618 ms
窄投影 UNION ALL（本轮）    247.063 ms   ⇒ 59.6×
拆成两条独立 count       statement_timeout（最差）

# plan 宽度（★ cost 逐字相同）
视图形态   Bitmap Heap Scan ... width=3083
原生形态   Bitmap Heap Scan ... width=32
两版 cost  cost=41.94..441.93 rows=391   ← 逐字相同

# 视图体 ≡ 原生 source
剥 CREATE 头 + 剥外层括号 ⇒ 逐字相同；118 列；无 NOT EXISTS；两侧都无 v1 族

# 252 行集对拍（最近 1 天）
llm_gateway：9070 / 9070 / only_in_view=0 / only_in_native=0 / 分组差集 0/0
postgres    ：9072 / 9072 / only_in_view=0 / only_in_native=0 / 分组差集 0/0

# 252 按凭据计数对拍（单语句同一快照）
1: 0=0 t / 2: 25852=25852 t / 10: 0=0 t / 21: 132075=132075 t / 37: 23326=23326 t

# pgss 窗口
252 stats_reset = 2026-10-18 22:03:37 ⇒ 窗口 8.4 天 ⇒ 文本可能来自旧二进制
```

## §9 本轮明确没做（★ 留给下一轮，别重复排查）

- ★ **`:462` 的 live p95 LATERAL**（★ 同族，★ 但需要 details 列 ⇒ 形态不同 ⇒ 要另做窄投影+details 版本）。
- ★ **`maas_credit_consumption_buckets` 的 INSERT…SELECT**（pgss mean 22,079 ms × 6 次）。
- ★ **`WITH policy AS (routing_policy.featured_models)` 那条**（pgss 总量第一：7,407,875 ms / 18,062 次）。
- ★ **其余 ~93 处 canonical 视图读点**（★ 本轮只修了 TOP 1）。
- ★ **`no_topic_session.go` / `session_title.go` 两道守卫的旧前提**（★ §3.4，已过期但属他人工作面）。
- ★ **写路径效率**（★ 原始目标第 4 条的另一半）。
- ★ **110 条非 GET 面逐条运行时验证**（★ 需属主定「可实跑 vs 只静态验」的分层口径）。
- ★ 对 252 **全程只读**；★ 未连 211；★ 本轮 `.sql` 变更 = 0 ⇒ **211 / 252 无需补 SQL**。
