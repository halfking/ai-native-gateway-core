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

### 2.3 ★★★★ **pgss 总量第一那条【根本不是 canonical 视图读点】—— ★★ 「按总量排序 ⇒ 逐个改读点」会走进死胡同**

★★ 把 §2.1 表里**总量第一**那条（18,062 次 / 均 410.1 ms / 总 **7,407,875 ms**）映射回源码 ⇒
★ **它不是视图读点**：★ 它的原文是
`WITH static AS (SELECT featured_models FROM routing_policy ...)`，★ 实测源码在
★ **`bg/model_probe.go:416` 与 `:500`**（★ `featuredCycle` / watchdog 的 backoff 更新语句）⇒ ★★ 它的两条腿是：

| 腿 | 读什么 | ★ 是不是 canonical 视图 |
|---|---|---|
| `static` | `routing_policy` **表本身** | ★ 否 |
| `usage` | `db.SessionFamilyTurnsHotSourceSQL()` ⇒ **会话族热腿底表** | ★ 否（★ 已经是底表） |

⇒ ⇒ ⇒ ★★ **它慢的原因不是「视图物化宽」**，★ 而是这个聚合本身重
（★ `GROUP BY raw_model` + `count(*)` 全量扫热腿 + ★ 三臂探针排除谓词）⇒ ⇒ ⇒ ⇒ ★★ **⇒ 把它按「视图读点」去改，
会改一个已经改过的地方** ⇒ ⇒ ⇒ ⇒ ★★ **⇒ 「pgss 排序」与「读点分类」必须【两个维度分开】**：

★ ⇒ **维度 A【是否读 canonical 视图】（★ 决定「能不能改」）**
★ ⇒ **维度 B【慢到什么程度】（★ 决定「值不值得改」）**
★ ⇒ ⇒ ⇒ ★★ **pgss 只给出维度 B** ⇒ ⇒ ⇒ ⇒ ★★ **⇒ 拿维度 B 当维度 A 用 ⇒ 就会把非读点排到前面**
★ ⇒ ⇒ ⇒ ⇒ ★★ ⇒ **⇒ 这正是本轮如果只按 pgss 排序就动手，会踩的第一个坑** ⇒ ⇒ ⇒ ⇒ ★★ ⇒
★ **⇒ 而且它同时说明了为什么「量具要能直接回答问题」这条通用式需要补一句：★★ pgss 能直接回答
「哪条语句累计最慢」，★ 但【回答不了】「哪条语句是读点」⇒ ⇒ ⇒ ⇒ ★★ ⇒ **⇒ 量具答得了的问题，
必须是决策真正需要的那一问**（★ 决策是「改读点」，★ 不是「优化最慢的语句」）⇒ ⇒ ⇒ ⇒ ★★ ⇒
★ ⇒ **⇒ 排序前必须先分类，★ 而分类的判据要在【源码/视图引用】这一侧，不在【耗时】这一侧**。

★ ★★ **顺带**：★ 这条查询的 `calls=18,062` 落在 8.4 天窗口里 ⇒ ★ 日均约 **2,150 次** ⇒ ★ 与仓内注释
「★ 后台 ticker 每 10min 刷新一次」量级相符 ⇒ ⇒ ★★ **⇒ 「调用次数高」本身也是一条独立线索**
（★ 高频语句即使不慢，累计成本也会进 TOP）⇒ ⇒ ⇒ ★★ ⇒ **⇒ TOP 榜上两种东西混在一起：
【慢而少】与【不慢而多】⇒ ★ 两者要分开处理，★ 否则会被「优化最慢」带偏**。

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

## §9 ★★★★ 本轮撞到的第二处「门红」：★★ 这次不是我的代码错，是【那道门的判据落错了地方】

★★★ 慢门跑完后，除了 §9 里那些「别人家的慢点」，本轮自己的改动**引出了 2 条红**。
★★ 两条都不是「代码错」，★ 而是「**门的判据落错了地方**」—— ★★ 这与本轮 §7「变异臂抓出来的三个洞」
是**同一条通用式的两次成立**，★ 所以单独记一节。

### 9.1 ★★ 红 A：`TestAdminRoutesSnapshotMatchesGenerator`（★ 行号漂移，★ 机械可修）

```
★ 第 358 行不一致 ⇒ 快照已漂移（或生成器口径变了）
生成器："/api/credentials/clear-manual-disabled\tANY\tadmin/credential_monitor.go:171"
快照　："/api/credentials/clear-manual-disabled\tANY\tadmin/credential_monitor.go:170"
```

★ 真因：本轮把 `total_requests` 那行从「一行字面量」改成「三行拼接」⇒ ★★ **`admin/credential_monitor.go`
里它后面的每一行行号都 +1** ⇒ ★★ 路由快照（`scripts/audit/asset/admin-routes.txt`，★ 逐行记 `file:line`）
漂了 13 行。

★★ **修法不是手改快照** —— ★ 脚本头部与门都写明「**生成产物，不要手改**」⇒ ★ 用生成器自己刷：
`bash scripts/audit/extract-admin-routes.sh --update-snapshot` ⇒ ★★ **diff 结果恰好 13 行，
且全部是 `admin/credential_monitor.go` 的行号 +1** ⇒ ★★ **没有别的行被顺带改动**（★ 这是「修对了」的证据，
★ 不是「跑一下看看」）。

★ ★★ **顺带发现：`--update-snapshot` 这个模式是 `origin/main` 本轮新增的**（★ 2026-10-27 合并轮），
★ ★ 且脚本注释里记着它要解决的坑：★ 原先文档里那行
`{ head -1 …/admin-routes.txt; cat …/admin-routes.new; } > …/admin-routes.txt`
★ ★ **是破坏性的**（★ `>` 先把目标截断 ⇒ `head -1` 读到空 ⇒ ★ **快照的口径说明头被整行丢掉** ⇒
★ ★ 5 道 admin-routes 门全红，★ 而红的原因**完全不指向真正的原因**）⇒ ★ ★ **新模式三件事一次做对**
（★ 先复制再读头 · ★ 改写头里的「数据行数=N」· ★ 临时文件 + `mv` 原子替换）⇒ ★ **★ 工具的演进方向是对的。

### 9.2 ★★★★ 红 B：`TestMonitorSummaryCoreModeSQLShape`（★ 这条最有价值）

```
--- FAIL: TestMonitorSummaryCoreModeSQLShape (0.00s)
    credential_monitor_toggle_test.go:71: could not locate a detail SELECT fragment that
    touches the heavy aggregates (_request_logs / recent_success_rate / percentile_cont)
```

★★ **旧判据**：★ 在源文件里找**反引号片段**，★ 要求某个片段同时
① 含 `"from credentials c"` 且 ② 命中 heavy 集合（`request_logs_with_current_month` /
`recent_success_rate` / `percentile_cont` / `credential_model_index_with_current_month` /
`credential_model_call_history`）。

★★★ **真因不是「门坏了」，★ 是「门从来就没看见过它要守的东西」**：
★★ detail 模式真正的 heavy 聚合在 `modelsSelect`（live p95 LATERAL：`percentile_cont` +
`request_logs_with_current_month`）与 `aggregatedSuccessJoin`（`recent_success_rate`）里，
★★ 而这两个是**被 `%s` 拼进去的局部变量** ⇒ ★★ **它们的文本里根本没有 `from credentials c`**
⇒ ★★ **被旧判据的前置条件直接过滤掉了** ⇒ ★★ ⇒ **旧门一直是被顶层那处「字面量视图名」顺带满足的。**

★★ ★★ 而本轮把那处字面量换成了 helper ⇒ ★★ **巧合消失 ⇒ 门红** ⇒ ⇒ ⇒ **⇒ 真问题是判据的位置**：

| | 旧判据（扫源码片段） | 新判据（判产物 SQL） |
|---|---|---|
| 判据落点 | 源文件的反引号片段 | `buildMonitorSummarySQL` 的**返回值** |
| 拼接进来的内容 | ★ **看不见** | ★ **看得见** |
| 「字面量 → helper」 | ★★ **假红** | ★ 不受影响 |
| 门原来真的在守 heavy 吗 | ★★ **没有**（被巧合满足） | ★ **有** |

★★ **修法**：★ 判据直接调用 `buildMonitorSummarySQL`，★ 取 core / detail 两个模式的**最终 SQL** 再判。
★★ **并且顺手加强了一条**：★ **core 产物不得命中任何 heavy 聚合** ⇒ ★ 与源码注释
「Core mode deliberately avoids request-log and per-model rate/latency aggregates」对齐
⇒ ★★ **旧门根本没有这条**（★ 旧门只要求 `coreFound && detailFound`，★ 两个都能在【同一种模式】里被满足）。

★★ **heavy 判据盯的是【聚合/物化】语义而不是「视图名」**（★ `percentile_cont` / `COUNT` / 两个 rollup 表）
⇒ ★★ 将来把 heavy 聚合也改走底表两臂时，★ **判据仍然成立** ⇒ ★★ **门不会因为「优化」而红。**

### 9.3 ★★ 新门的「有牙」证明（★ 变异臂 2 臂全检出，★ 还原后 md5 逐字一致）

| 臂 | 注入 | 期望 | 实测 |
|---|---|---|---|
| N1 | ★ core 产物注入 `recent_success_rate` | 红 | ★ **红**（`core 产物命中重聚合 [recent_success_rate]`） |
| N2 | ★ detail 产物的全部 heavy marker 换成不含原子串的形式 | 红 | ★ **红**（`detail 产物没有命中任何重聚合标记`） |

★★ **还原后 md5 逐字一致**：`ec3e596a14c391e9a026262db98d111b`。

★★★ **★ N2 第一版是【空转】—— ★★ 这本身是本轮第二个值钱的发现**：
★ ★ 第一版 N2 给每个 marker 加 `zz_` 前缀（`zz_request_logs_with_current_month`）⇒
★ ★★ **判据是 `strings.Contains(sql, marker)` ⇒ ★★ 加前缀【不改变包含关系】⇒ 变异空转**
⇒ ★★ **而脚本当时只查「文件内容变了」⇒ ★★ 差点把「臂没检出」误读成「门没牙」** ⇒ ⇒ ⇒
★ ★★ **通用式：★★ 变异臂的注入方式必须真的改变【判据所依赖的那个性质】；
★★ 且脚本必须能【自证注入生效】—— ★★ 否则「臂没检出」会被误读成「门没牙」，★ 而这两个结论要采取完全相反的行动**
⇒ ⇒ ⇒ ★★ ⇒ **★★ 第二版 N2 改成「替换成完全不含原子串的形式」⇒ 检出**；
★★ ⇒ **★★ 第三版给脚本加了「注入生效性自证」（★ heavy marker 总出现次数的变化方向）** ——
★★ ★ 而它第一版又只查「移除型」⇒ **把 N1 反向误判成空转** ⇒ ★★ **⇒ 自证谓词必须按臂的【方向】写**
⇒ ⇒ ⇒ ★★ ⇒ **★★ 这是本轮第二次栽在「量具自己也会错」上（★ 第一次是 §7 的三态区分）**。

### 9.4 ★★ 同深度对拍：★ 新判据在 origin/main 上同样绿（★ 证明改的是判据口径，不是给自己开绿灯）

★ ★ 把**同一份新门**拷进 `origin/main` 的纯净 worktree（`4dfd4696b`）跑 ⇒ **PASS** ⇒ ⇒
★★ **⇒ 新判据在对方代码上也成立 ⇒ ⇒ ⇒ 我改的是【判据落点】，★ 不是【为了让本轮变绿而放宽判据】**。
★★ ★★ 这一步是必需的：★ **「我改了门」这件事本身需要独立证据**，★ 否则「改门」就是最方便的开绿灯方式。

## §10 ★★★ 慢门读数与红因归属（★ 「门超时」≠「门红」）

★ 慢门命令：`make guards-ursmcheck`（★ `go test ./scripts/ursmcheck ./admin -count=1 -timeout=900s`）。

### 10.1 ★★ 这次慢门是【超时】，不是【判决】

★ 实测：`real 15m3.636s` ⇒ ★★ **`scripts/ursmcheck` 包在 900s 处被 `panic: test timed out` 打断**
（★ 卡在 `TestV1FamilyRetirementDetectsFamilyVariants` 的子进程 IO wait，★ 栈显示它等了 8 分钟）
⇒ ★★ **⇒ 按 §450 的老规矩：「没跑完」不能当「没红」** ⇒ ★★ **⇒ 必须在一个能跑完的负载窗口重跑**。

### 10.1b ★★★★ 「门超时」的处理链（★ 本轮做完整了 ⇒ 结论是【判决】不是【超时】）

★★★ 上一次慢门（不带 `-v`）是**超时**（★ `real 15m3.636s` ⇒ `panic: test timed out`）
⇒ ★★ 按「**没跑完 ≠ 没红**」的老规矩，★ 那次**不能当判决** ⇒ ★★ 本轮重跑的纪律：

| 步 | 做法 | 为什么 |
|---|---|---|
| ① | ★ **`-v` 重跑**（★ 不带 `-v` 时超时只会打印【失败的】⇒ ★ **已跑过的绿门全部看不到**） | ★★ 要拿到「哪些门跑过了」的完整清单 |
| ② | ★ **解析日志**（★ 按 `=== RUN <name>` 与 `--- PASS/FAIL/SKIP: <name>` 对拍，★ 取顶层测试） | ★★ 顶层 **807 启动 / 806 有结论** ⇒ **只差 1 条** |
| ③ | ★ **单独补跑那 1 条**（★ `TestFootprintModeUnreliableDenominatorIsNoConclusion` ⇒ **0.48s PASS**） | ★★ ★ 它本来就叫「NoConclusion」⇒ ★ **它就是设计成不出结论的那条** ⇒ ★ 恰好撞上超时 |
| ④ | ★ 结论 | ★★ **807 条全部有判决** ⇒ ★★ **本轮慢门读数是【判决级】的** |

★★★ **★ 这一步最值钱的地方**：★② 的「807 启动 / 806 有结论」是**逐条对拍出来的**，★ 不是估的
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 「超时影响面 = 1 条」这个结论【有据】** ⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 而如果只按上次那样
⇒ ⇒ ⇒ ★★ ⇒ 「日志里有几条 FAIL」来数，★ 会得出「1 条红」的同样结论，★ ★★ **但那是巧合**
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 因为「没跑完的那些」既不报红也不报绿 ⇒ ★ 它们在两种算法里都【隐身】**
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 「超时的那次日志里有几条 FAIL」≠「本次有几条红」** ⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 这就是
⇒ ⇒ ⇒ ★★ ⇒ 「用截断的输出下结论 = 用不完整的证据下结论」的具体形态**。

### 10.2 ★★ 超时前已经**跑完并打印**的红（★ 这些是判决级的）

| # | 门 | 归属 | 处置 |
|---|---|---|---|
| A | `TestAdminRoutesSnapshotMatchesGenerator` | ★★ **本轮引入**（行号漂移） | ★ 已修（§9.1：生成器刷新，diff 恰 13 行且全是 +1） |
| B | `TestMonitorSummaryCoreModeSQLShape` | ★★ **本轮引入**（判据落点） | ★ 已修（§9.2：判据移到产物 + 加强 core 不得含 heavy） |
| C | `TestStructuredRegistryCensusCoversEveryCandidate` | ★★ **origin/main 既有** | ★ **同深度对拍坐实**（§10.3）⇒ 不代改 |

★★ **★ 第二次重跑（★ 带 `-v`）的最终判决**：
★ `ursmcheck` 包 **807 条顶层测试全部有判决** ⇒ ★★ **806 PASS / 1 FAIL（C 条）/ 21 SKIP**
★ ｜ `admin` 包 ⇒ **ok 113.437s**（★ 含本轮 6 条守卫 + R167 的 4 条 + 真库逐凭据对拍）
⇒ ⇒ ⇒ ★★ **⇒ 本轮引入零红** ⇒ ⇒ ⇒ ★★ ⇒ **⇒ 唯一那条红已用同深度对拍坐实为 origin/main 既有**。

### 10.3 ★★ 红 C 的同深度对拍（★ 归属一次性坐实）

★ ★ 把**同一条门**在 `origin/main` 的纯净 worktree（`4dfd4696b`）上跑 ⇒ **逐字同样的 5 条红**：

```
有 5 个「像登记册」的结构化文件没在 scripts/ursmcheck/structured-registry-census.json 里归属：
      web/scripts/design-primary-clarity-baseline.json
      web/scripts/design-semantic-color-baseline.json
      web/scripts/design-spacing-scale-baseline.json
      web/scripts/design-spacing-scale-runtime-baseline.json
      web/scripts/design-type-scale-baseline.json
```

★ ★★ 五个文件**全是 `design-*` 基线** ⇒ ★★ **全是并行会话那几轮（间距/字号/主色/语义色/primary-clarity）的产物**
⇒ ⇒ ⇒ **⇒ 零条本轮引入** ⇒ ⇒ ⇒ **⇒ 不代改他人工作面** ⇒ ⇒ ⇒ **⇒ 列入属主裁决清单**
（★ 修法现成：★ census 表里二选一 —— `kind=registry` 写出读方，或 `kind=excluded` 写实质理由）。

### 10.4 ★★ 顺带：★ 「校验要查 U+FFFD」这条教训写完，★ 回头一查仓里【已经有 2 个】

★★ 本轮把「heredoc 追加中文会坏字节」写进 `AGENTS.md` / `LESSONS.md` 之后，★ 顺手扫了本轮改动过的
★ 全部 7 个文件 ⇒ ★★ **本轮改的 / 新建的 7 个文件全部 U+FFFD = 0** ✔ ⇒ ★★ 但 `AGENTS.md` 里有 **2 个**。

★★★ **归属坐实（★ 不是本轮引入）**：★ `origin/main` 的 `AGENTS.md` 实测 **U+FFFD = 2**
⇒ ⇒ ⇒ ★★ **⇒ 位置在 ㉚ R87 轮那段（★ 并行会话的工作面）**，★ ★★ **⇒ 不代改** ⇒ ⇒ ⇒ ★★ **⇒ 列入属主裁决清单**
（★ 修法：★ 把「每个还存在**节点类型**不变」那两个 U+FFFD 改回「的」）。

★★ ★★ **这条发现比「修掉它」更有价值**：★ **「我刚写完『校验要查 U+FFFD 计数』这条教训，
⇒ ⇒ ⇒ ★★ 回头一查仓里就有 2 个」⇒ ⇒ ⇒ ★★ ⇒ ⇒ 说明这条教训的【适用范围】比想象广**
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 而它【抓不到自己】：★ U+FFFD 是【合法 UTF-8 码点】⇒ ⇒ `decode('utf-8')` 通过
⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒ 而仓里【没有任何一道门】查这个 ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ ⇒ ★★ ⇒ ⇒
★ **⇒ 「仓内零 U+FFFD」目前是【无人看守】的 ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ ⇒ ⇒ ⇒ **★ 下一轮可以加一道极便宜的门**
★ ★★（★ 全仓 `grep -c $'\ufffd'` ⇒ ★ 零 ⇒ ★★ 绿）⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ **★ 而它会立刻抓出这 2 个**
⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ **★ ⇒ 也就是说：★ 一道 3 行的门，能把本轮这种「看起来完全正常」的损害变成红灯**
⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ **★ ⇒ 通用式再补一句：★★★ 「校验清单」本身也可以【变成门】⇒ ★★
★ ★★ 凡是「靠人记得做」的校验，★ 都是【迟早会忘】的校验 ⇒ ⇒ ⇒ ★★ ⇒ ⇒ ★★ ⇒ **★ ⇒ 该把它写成门**。

### 10.4b ★★ 部署与逐 API 巡检读数（★ ★ 第一次部署 `2.5.8.2885`；★ ★★ 第二次部署 `2.5.8.2886` 见 §10.4c）

★★ 部署（★ 第一次，★ 即 `2.5.8.2885`；★ ★★ 之所以要第二次，★ 见 §10.4c）：
★★ `bash scripts/deploy-local.sh deploy` ⇒ ★★ **`VERIFY_PASS=1` / `VERIFY_RELEASE=2.5.8.2885` /
★★ `DEPLOY_EXIT=0`** ⇒ ★★ 凭据解密冒烟 `failed=0`（★ providers=16,2452,2453 · creds=3）⇒ ★★ 8781+8782 双实例 ok。

★★ **本轮改动直接相关的端点冒烟**（★ ★★ 用真实 cookie（`llmgw_session`）⇒ ★★ 免鉴权时全是 401）：

| 端点 | 结果 |
|---|---|
| `/api/credentials/monitor-summary?credential_id=21` | ★ **HTTP 200 / 122 ms** · ★ `total_requests=0`（★ 本地库该凭据无请求记录）· ★★ **无报错、无空值 ⇒ SQL 形态正确** |
| `/api/credentials/sliding-window?window=60min|1440min` | ★ HTTP 400 `credential_id required`（★ 预期） |

★★★ **★ 关于 `total_requests=0` 的读法（★ 别误读）**：★ 本地库只有 2,722 行且该凭据无记录 ⇒ ★★ **0 是正确的**，
★★ ★★ 它**不能**用来验证口径；★ ★★ **口径等价性的判决在真库逐凭据对拍**（★ 13 个凭据 · ★ 视图计数 ≡ 窄投影两臂计数）
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 端点冒烟验的是「SQL 能跑、字段有值」，★ 不是「口径对」—— ★ 两个判据各管各的**。

★★ **逐 API 巡检**（★ `scripts/audit/api-sweep.sh` + ★ **仓内正本** `scripts/audit/asset/admin-routes.txt`
★ ★★ ★ **★ 那份正是本轮用生成器刷过的快照** ⇒ ★★ 清单与源码逐字一致）：

| 项 | 读数 |
|---|---|
| 巡检路由数 | ★ **514**（★ R166 基线 **510** ⇒ **+4**） |
| 退出码 | ★ **75** ⇒ ★★ **load 峰值 50.10 > 阈值 20** ⇒ ★★ **延迟读数不可信，★ 状态码/分类仍可信**（★ 脚本自身明确分开报） |
| 消失路由 | ★ **0** |
| 新增路由 | ★ **4**：`/v1/classify` · `/v1/summarize` · `/v1/summarize/title` · `/v1beta/models` ⇒ ★★ **全部来自合并进来的 `origin/main`**（★ 本轮**未改任何路由声明** ⇒ ★★ 这 4 条必然来自 origin/main）⇒ ★★ **与 R167 的「+4 全是 origin/main 新增」逐项一致** |
| **共有路由 status/class 差异** | ★★ **0** ⇒ ★★ **⇒ 本轮改动零行为回归** |
| class 分布 | ok 275 · method_not_allowed 109 · bad_request 65 · not_found 47 · other_4xx 11 · server_error 6 · meta 1 |

★★ ★★ **「+4」与 R167 逐项一致**这件事本身是一条独立证据：★ R167 的 +4 是 `/v1/*` 与 `/v1beta/models`，
★★ ★★ 本轮合并 origin/main 后仍是**同一组 4 条** ⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 说明这 4 条是 origin/main 早已存在、
★★ ★★ 而非本轮引入的路由** ⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 与「本轮未改任何路由声明」互相印证**。

### 10.4c ★★★★ 部署版本戳与【可追溯性】的两段式（★ 一度踩了「git_sha 指向未推送提交」的坑）

★★★ 本轮第一次部署（`2.5.8.2885`）的产物 `git_sha` = **`d35f19cb`** ⇒ ★★ **那个提交从未被推送**
（★ 它是本轮多次 `amend` 之间的一个**中间态**）⇒ ★★ ⇒ **★ 「可追溯性」当场破了**：
★ ★★ 运行中的这份 release，★ 指向一个**别处找不到**的提交。

★★ **修法（★ 不是「再部署一次看看」）**：★★ 先把文档**定稿**（★ 文档定稿前不部署 ⇒ 部署时的 HEAD 就是最终 HEAD）
⇒ ★★ 再 push ⇒ ★★ **再部署** ⇒ ★★ ⇒ **★ 部署产物的 `git_sha` 逐字等于【已推送的】HEAD**：

| 阶段 | 读数 |
|---|---|
| 第一次部署 | `2.5.8.2885` · `git_sha=d35f19cb`（★ 未推送的中间提交 ⇒ ★ **不可追溯**） |
| 文档定稿 + push | `origin/main = 87c7acc94` |
| ★ **第二次部署** | ★ **`2.5.8.2886`** · ★★ **`git_sha=87c7acc9`（★ 逐字等于已推送 HEAD）** · `VERIFY_PASS=1` · `DEPLOY_EXIT=0` · 凭据解密 `failed=0` |

★★★ **★ 顺带查清一件一直含糊的事：★ 仓内 `version.json` 到底怎么参与版本号链** ——
★ ★★ `deploy-local.sh` 的 `bump_local_version` 跑 `bump-version.sh` ⇒ ★★ **它把仓内 `version.json` 的
★ ★★ `build_seq` +1 并写回工作区** ⇒ ★★ ⇒ **★ 所以「仓内 version.json」= 「下一次部署要烧的序号」**
★ ★★ ⇒ ★★ **⇒ 它必须与【刚部署完】的序号一致，★ 否则下次部署会重烧已占用的号**
★ ★★ ⇒ ★★ ⇒ **★ ⇒ 所以仓内 `version.json` 是【部署之后】才提交的那一份**（★ ★★ 与 R167 的做法一致：
★ ★★ `origin/main` 上的 `version.json` 停在 2884 ＝ R167 部署完的那一份）★★。

★ ★★ 部署脚本会改 **4 个投影文件**（★ 本轮实测）：★ `VERSION` · ★ `version.json` ·
★ ★ `web/public/version.json` · ★ ★ `web/public/menu-config.json`（★ 它的 `exported_at` 也会被刷新）
⇒ ★★ ⇒ **★ 四个一起提交，★ 保持仓内与部署产物逐字一致** ⇒ ★★ ⇒ **★ 而它们是【纯投影】**
★ ★★ （★ 只带版本戳，★ 不带行为）⇒ ★★ ⇒ **★ ⇒ 提交它们**【不会】让部署产物与 HEAD 再次脱节
★ ★★ ⇒ ★★ ⇒ **★ ⇒ 「版本号链」与「git_sha 可追溯」这两件事，★ 是【两件事】，★ 要分开处理**：
★ ★★ ① **版本号链**（★ 单调不回退）★ 靠「仓内 version.json = 刚部署的序号」；
★ ★★ ② **git_sha 可追溯**（★ 指向已推送提交）★ 靠「部署发生在文档定稿之后」。

### 10.4c-2 ★★ 收口提交之后的「为什么不再重新部署」（★ 判据写清，★ 免得下一轮误判为漏做）

★★ 收口提交 `8fe9bf54c`（★ 版本戳 2886 + 合并收尾刷新的路由快照 + 本审计档）⇒ ★★ **它只含
★★ 【投影 / 生成器产物 / 文档】，★ 运行时行为与 `87c7acc94` 逐字相同** ⇒ ★★ **⇒ 因此没有第三次部署**。

★★★ **判据（★ 两条分开，别混成一条）**：
★ ★ ① **「git_sha 指向一个【已推送的】提交」——★ ✔ 成立**：★ 部署产物 `2.5.8.2886` 的
★ ★ `git_sha=87c7acc9` ⇒ ★★ 指向 `87c7acc94`（★ **已推送**，★ 且是 `8fe9bf54c` 的父）⇒ ★★ **可追溯性成立**。
★ ★ ② **「git_sha 指向【最新 HEAD】」——★ ✘ 不满足**：★ 但**不强求** ⇒ ★★ **★ 因为纯文档/投影提交
★ ★ 不改变运行时行为** ⇒ ★★ **★ ⇒ 而重部署只会【再烧一个 build_seq（2887）】换掉一个【不影响行为】的字符串**
★ ★ ⇒ ⇒ ★★ ⇒ **★ ⇒ 判据：★ 「指向已推送提交」是【必要条件】；★ 「指向最新 HEAD」只是【在有行为变更时才需要】**。
★★ ★★ ★★ ⇒ **★ ⇒ 顺带一条可复用的次序（★ 本轮实测走通）**：
★★ ★★ **★ 文档定稿 → push → 部署 → 提交投影文件 → push**（★ ★★ 最后一个 push 是【纯投影】⇒ ★★ 不必再部署）
★★ ★★ ⇒ ★★ **★ ⇒ 这样既保证【产物 sha 可追溯】，★ 又不违反【投影文件要与产物一致】，★ 且序号链单调。**

### 10.4d ★★ 4 个版本戳投影文件里，★ 本轮只提交 3 个 —— ★★ 「构建噪声」与「语义版本号」要分开

★★ 部署脚本改了 **4 个**投影文件，★ ★★ 但本轮只提交 **3 个**，★ ★ **`web/public/menu-config.json` 被还原**
★ ★★ ⇒ **★ 这不是漏提交，★ 是裁决**，★ 理由如下（★ ★★ 仓内先例本身【互相冲突】，★ 所以必须说明裁决依据）：

| 仓内先例 | 主张 |
|---|---|
| `docs/12小时内修订审计-2026-10-25-0716.md:108` | ★★ 「`menu-config.json` 的 `exported_at` 是**构建产物噪声** ⇒ **已还原，不入仓**」 |
| `docs/全面审计v3/2026-10-01/45-R75-...md:249` | ★★ 「`menu-config.json` 保留 upstream 的新时间戳，★ `exported_at` 是**自动生成元数据**，★ 取新值正确」 |
| `docs/12小时内修订审计-2026-10-26-0714.md:62-63` | ★★ 合并冲突时 `menu-config.json` **取上游**（★ 更新的一次导出） |
| `docs/db-changelog.md:9153` | ★★ 4 个版本产物**一起**提交（★ 「menu-config 只变了构建导出的 `exported_at` 时间戳，★ 与先例 `32ec82b82` 同款」） |

★★ **裁决依据（★ 按「性质」而不是「先例新旧」判）**：
★ ★ ① **VERSION / version.json / web/public/version.json** ⇒ ★★ **语义版本号**（★ 含 git_sha）
⇒ ★★ **必须与部署产物逐字一致** ⇒ ★★ **提交**；
★ ★ ② **`menu-config.json` 的 `exported_at`** ⇒ ★★ **纯构建时间戳**（★ ★ 每构建一次就变一次，
★ ★ 与「哪份代码」无关）⇒ ★★ ⇒ **★ ⇒ 提交它唯一的实际效果是【每次合并都制造一处冲突】**
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 而仓内已【两次】记录过这类冲突**（★ 10-26 那次合并冲突 · ★ R75 那次 `stash pop` 冲突）
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 故本轮取 2026-10-25 的先例：★ 还原，不入仓**。
★ ★★ ★★ **（★ 若属主另有口径，★ 改回是一行 `git checkout` 的事；★ 本轮按「噪声不进仓」处理。）**

### 10.4e ★★★★ ★★ **最终树慢门判决 + 4 条红的逐条归属（★ 同深度对拍全部坐实）**

★★ 合并 `origin/main` 之后在**最终树**上重跑慢门（★ 带 `-v` ⇒ ★ 即使超时也拿得到完整清单）：

| 项 | 读数 |
|---|---|
| 总耗时 | ★ **`real 14m55.933s`**（★ **没有超时** ⇒ ★ 是【判决】不是【超时】） |
| 统计 | ★ **2729 PASS / 4 FAIL / 195 SKIP** |

★★★ **★ 4 条红的归属（★ 逐条同深度对拍：★ 同一条门在 `origin/main` 的纯净 worktree `17604eabb` 上跑）**：

| # | 门 | `origin/main` 纯净树 | 本轮树 | 归属与处置 |
|---|---|---|---|---|
| ① | `TestAdminRoutesSnapshotMatchesGenerator` | ★ **红**（★ **逐字同**：★ 生成器 `credential_state_handlers.go:185` vs 快照 `:177`） | ★ ★★ **已修 ⇒ 绿** | ★★ **合并收尾项**（★ origin/main 的提交改了 `admin/credential_state_handlers.go` 的行号 +8）⇒ ★★ **本轮用生成器 `--update-snapshot` 重刷** ⇒ ★★ **diff 恰好 4 行且全是 +8 ⇒ 零意外改动** ✔ |
| ② | `TestSection1975CandidateListMatchesGeneratorByteForByte` | ★ **红**（★ 逐字同） | ★ 红 | ★★ **origin/main 既有** ⇒ ★★ **不代改**（★ 见下方「为什么 ②③ 不代跑」） |
| ③ | `TestTsvHollowoutReadersRejectEmptyRegistry` | ★ **红**（★ 逐字同） | ★ 红 | ★★ **origin/main 既有** ⇒ ★★ **②③ 是同一个文件**（★ `docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv`）⇒ ★★ **重跑一次生成器可同时修好两条** ⇒ ★★ **但本轮不代跑**（★ 见下） |
| ④ | `TestStructuredRegistryCensusCoversEveryCandidate` | ★ **红**（★ 逐字同） | ★ 红 | ★★ **origin/main 既有**（★ 5+ 个 `design-*.json` 未登记进 census）⇒ ★★ **对方工作面 ⇒ 不代改** |

⇒ ⇒ ⇒ ★★★ **★ ⇒ 本轮引入零红**（★ 4 条红全部在 `origin/main` 上逐字同样存在）⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 本轮把 ① 修好了（★ 合并收尾），
⇒ ⇒ ⇒ ★★ ⇒ ⇒ 剩下 3 条是对方的存量** ⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 「我 push 的树比 origin/main 少 1 条红」**。

★★ ★★ **★ 为什么 ②③ 不代跑（★ 这是一个有代价的「不代改」，★ 理由必须写清）** ——
★★ ②③ 的产物是 **`scripts/scan-secrets.sh` 的扫描结果**（★ ★★ 行号漂移：★ 已装 257/265/302/303/304/305
★★ ★★ vs 生成 270/278/315/316/317/318 ⇒ ★★ **逐字只有行号不同，★ 内容零变化** ⇒ ★★ **重跑生成器即可修好**）
⇒ ⇒ ⇒ ★★ **⇒ 但 `scripts/scan-secrets.sh` 正是【并行会话此刻正在改】的文件**
（★ ★ 主工作区 `git status` 实测：`M scripts/scan-secrets.sh` · `M scripts/scan-secrets.baseline`）
⇒ ⇒ ⇒ ★★ ⇒ **⇒ 我现在重跑，★ 产的是「origin/main 版 scan-secrets.sh」对应的清单**
⇒ ⇒ ⇒ ★★ ⇒ **⇒ 对方一提交他们的 scan-secrets.sh，★ 清单【又漂】⇒ ⇒ ⇒ ★★ ⇒ ★★ ⇒ **★ ⇒ 两害相权：
⇒ ⇒ ⇒ ★★ ⇒ ★★ ① 我代跑 ⇒ ★★ 提交一个**必然再次过期**的产物 + ★★ 与对方的改动**制造合并冲突**
⇒ ⇒ ⇒ ★★ ⇒ ★★ ② 我不代跑 ⇒ ★★ **保持与 origin/main 逐字一致（★ 不让情况变差）** + ★★ 等对方定稿后由他们重跑
⇒ ⇒ ⇒ ★★ ⇒ ★★ ⇒ ★★ **★ ⇒ 本轮选 ②，★ 并把「重跑命令」原样交出去**：
★★ ★★ `python3 scripts/audit/measure-pipefail-scope-drop.py --out docs/audit/evidence/2026-10-21-pipefail-scope-drop-candidates.tsv`
★★ ★★ （★ ★★ 本轮已实测该命令产出 **83 行 / 37 文件**，★ 与门内 baseline `83` 一致 ⇒ ★★ **命令形态与产物规模都已验证**）。

### 10.5 ★★ 本轮新增的 2 条【属主裁决项】（★ 本轮不开工，★ 但必须留档）

| # | 项 | 证据 | 建议修法 | 为什么本轮不动 |
|---|---|---|---|---|
| 1 | ★ **`TestStructuredRegistryCensusCoversEveryCandidate` 红**（★ 5 个 `design-*.json` 未登记进 `scripts/ursmcheck/structured-registry-census.json`） | ★ 同深度对拍：★ **origin/main 纯净树上逐字同样的 5 条红** | ★ census 表里二选一：★ `kind=registry` 写出**读方**（★ 它是门读的输入 ⇒ 掏空它会让门瞎）／ ★ `kind=excluded` 写**实质理由** | ★ **属并行会话的设计门工作面** ⇒ ★ 不代改 |
| 2 | ★ **`AGENTS.md` 里有 2 个 U+FFFD**（★ 位置：★ ㉚ R87 轮段落「每个还存在**节点类型**不变」） | ★ `origin/main` 的 `AGENTS.md` 实测 **U+FFFD = 2**（★ 非本轮引入）；★ ★ 本轮改动/新建的 7 个文件全部 **0** | ★ 把那两个 U+FFFD 改回「的」；★ ★ 并可加一道 3 行的门（★ 全仓 `grep -c $'\ufffd'` ⇒ 零 ⇒ 绿） | ★ **在对方段落里** ⇒ ★ 不代改 |

★★ **★ 顺带一条建议给属主**：★ 那道 3 行的「全仓 U+FFFD = 0」门，★ **会立刻抓出上面第 2 项**
⇒ ⇒ ⇒ ★★ ⇒ **★ ⇒ 凡是「靠人记得做」的校验，★ 都是迟早会忘的校验 ⇒ ⇒ ⇒ ★★ ⇒ **★ 该把它写成门**
（★ ★★ 本轮 §10.5 已给出理由与形态）。

## §11 本轮明确没做（★ 留给下一轮，别重复排查）

- ★ **`:462` 的 live p95 LATERAL**（★ 同族，★ 但需要 details 列 ⇒ 形态不同 ⇒ 要另做窄投影+details 版本）。
- ★ **`maas_credit_consumption_buckets` 的 INSERT…SELECT**（pgss mean 22,079 ms × 6 次）。
- ★ **`WITH policy AS (routing_policy.featured_models)` 那条**（pgss 总量第一：7,407,875 ms / 18,062 次）
  ⇒ ★★ **但 §2.3 已坐实：它【不是 canonical 视图读点】**（★ 两条腿都不碰视图）⇒ ★★ **它要单独按「聚合本身重」处理，
  ★ 不属于「读点迁移」这条线** ⇒ ★★ **⇒ 别按 pgss 排名直接开下一轮的工单**。
- ★ **其余 ~93 处 canonical 视图读点**（★ 本轮只修了 TOP 1）。
  ★★ **本轮实测起点量（★ 口径见下）**：全仓 `request_logs_with_current_month` **607 处命中 / 204 个 `.go` 文件**
  ⇒ ★ **其中生产代码文件 ≈ 79 个**（★ 排除 `_test.go` / `sql/migrations/` / `cmd/tools/` / `tests/` / `scripts/ursmcheck/`；
  ★ ★ 含 1 个视图定义文件 `db/request_logs_view_schema.go` + 2 个审计工具文件）⇒ ★★ **★ 这个数是「字面提到该视图名」
  的文件数，不是「读点数」** ⇒ ★★ **★ 真正的读点数必须用仓内既有扫描器口径量**（★ 见下）。
- ★★ **★★ 下一轮（读点登记簿）不必从零新建 —— ★★ 仓内已有登记簿原型与一整套方法论**：
  ★ `admin/request_logs_read_inventory_test.go` 已有三张表 + 一套判据：
  ★ ① `requestLogsReadInventory`（★ 已退役 v1 族读方清单，★ 890 之后**必须为空**）·
  ★ ② `indirectRequestLogsReaders`（★ **切换层读方**：表名收进 Go 标识符再拼进 SQL 的那批）·
  ★ ③ `retiredV1ReferenceExceptions`（★ 已退役关系的残留引用 = **生产缺陷台账**）·
  ★ ④ `allKnownRequestLogsReaderFiles`（★ 前两者的**并集**，★ §9.259 明写「退役排期要用并集」）。
  ★★ **★ 它自己记着的方法论教训（★ 直接可用，省下一轮踩坑）**：
  ★ ★ 「**刻意不自动分类**」——★ 曾试图按「语句窗口内有无 `gw_session_id` / `request_id =`」自动分 A/B/C/D，
  ★ ★ 拿已人工核定的 14 个点交叉验证**判错 5 个** ⇒ ★★ ⇒ **这道门只数个数，不下判定**；
  ★ ★ 「**§9.259：这张表不是读方全集**」——★ 旧审计 §5.5.5「最终分类」只覆盖 14 个文件，
  ★ ★ 而实际读 `_request_logs*` 的生产文件有 **104 个 / 237 个调用点**（2026-10-01 实测），
  ★ ★ ★ **§5.5.8 自称「第一版表不完整，逐条补齐」——但那张表从未覆盖其余 90 个文件，且它自称补全过
  ★ ★ ★ ⇒ 没有任何机制会在这个缺口扩大时报红**；
  ★ ★ 「**扫描器必须有阳性对照**」——★ `TestRetiredV1ReferencesAreAllAccountedFor` 用「真 v1 必须命中 /
  ★ ★ ★ canonical 视图必须不命中」钉住扫描器区分力（★ 否则「实测为空」= 恒绿）。
  ★★ **⇒ 下一轮的正确做法是【扩展这套既有登记簿】+【给 canonical 视图读点加一列「形态/分类/豁免」】，
  ★★ ★ 而不是另起一个表** ⇒ ★★ **★ 而且「分类」这一步必须人工核定，★ 不能自动分类**（★ 上面那条教训就是依据）。
- ★ **`no_topic_session.go` / `session_title.go` 两道守卫的旧前提**（★ §3.4，已过期但属他人工作面）。
- ★ **写路径效率**（★ 原始目标第 4 条的另一半）。
- ★ **110 条非 GET 面逐条运行时验证**（★ 需属主定「可实跑 vs 只静态验」的分层口径）。
- ★ 对 252 **全程只读**；★ 未连 211；★ 本轮 `.sql` 变更 = 0 ⇒ **211 / 252 无需补 SQL**。
