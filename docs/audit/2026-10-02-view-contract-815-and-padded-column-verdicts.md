# 会话存储解耦 v3：§9.27 视图契约 815 三列投影 + §9.29 补位列逐列裁决

> **章节号对照（2026-10-02 15:10 修订）**：本文件初稿用过 §9.28–§9.31，与并行会话
> 提交 `5b6e3ea03`（主文档 §9.28「会话族是两个存储面」）**撞号**。已整体顺延为
> **§9.29 / §9.30 / §9.31 / §9.32**。本文件现占用 §9.27 与 §9.29–§9.32；
> 主文档侧的 §9.22–§9.26 与 §9.28 属并行会话。
>
> **归属说明（先读这段）**：本文件是
> `docs/audit/2026-09-30-session-request-data-re-audit.md` 的续篇，章节号接着
> §9.26 往下排。之所以**单独成文**而不是直接追加进那份主文档：写本轮时该文件
> 正在被并行会话编辑（其未提交改动占用了 §9.26，168 行），直接追加会让本轮的
> 提交把对方的在途改动一并带上 —— 这正是本项目「并行检出下工作区出现的东西
> ≠ 我改的东西」纪律要防的事。主文档的并发编辑落地后，本文件内容可整段并入。
>
> 基线：`4ced63132`（§9.21）+ 并行线已推送的 §9.22–§9.26。真库：
> 本机 `llm-gateway-pg`（kx-citus-pg17），全部数字为 2026-10-02 实测。

---

## §9.27 给 710 视图补 3 个投影（决策 1：拍板为「补 3 不补 4」）

§9.20 把待决范围压到 4 列（`origin_stage` / `token_band` /
`client_forwarded_for` / `trace_events`），§9.20.3 给它们加了一个「硬前提」：
必须与「所有视图读方切到 `bg.ProbeTrafficExclusionPredicateView`」同批提交。
本节给出这两问的答案，**其中一条前提被证伪**。

### §9.27.1 硬前提已经满足了，而它给的理由是错的

先核前提本身。全仓物理表版谓词 `probeTrafficExclusionPredicate` 的调用点共
**6 处**，逐处核 FROM：

| 调用点 | FROM | 判定 |
|---|---|---|
| `bg/model_probe.go:430` | `request_logs_hot rl` | 物理表，合法 |
| `bg/model_probe.go:908` | `request_logs_hot rl` | 物理表，合法 |
| `bg/credential_selfcheck.go:549` | `request_logs_hot rl` | 物理表，合法 |
| `bg/credential_selfcheck.go:603` | `request_logs_hot rl` | 物理表，合法 |
| `bg/today_success_probe.go:152` | `request_logs_hot rl` | 物理表，合法 |
| `bg/model_tier.go:176` | `request_logs_hot rl` | 物理表，合法 |

⇒ **没有任何视图读方在用物理表版谓词**。「所有视图读方切到视图变体」这条前提
在写代码之前就已经成立。

但 §9.20.3 给的理由「补 `origin_stage` 会让物理谓词在视图上立刻又 42703」是
**错的**：物理谓词两臂是 `quality_flags` + `origin_stage`，而 `quality_flags`
本来就在冻结契约里（补位 NULL）。把 `origin_stage` 也补进契约之后，两臂**都**
解析得了，42703 不会再发生。

真正的新风险是反过来的，而且更安静：

> 谓词从「必然 500」变成「能跑但语义错」。`quality_flags` 在 session 分臂由
> 733 特征层供值、缺行时为 NULL ⇒ `NOT COALESCE('probe' = ANY(NULL), FALSE)`
> 求值为 **TRUE** ⇒ 该臂对整个 session 分臂静默失效，只剩 `origin_actor` 一臂
> 生效 ⇒ 探测流量被当成业务用量 ⇒ INV-3（扫描只探真用过的模型）重新变成名义上的。

**修掉 500 之后，原来靠 500 挡着的错误用法失去了挡板。** 所以本轮没有简单地
「同批提交」，而是补了一道**新的禁令门**（见 §9.27.5）——那才是这条前提在
补完投影之后真正需要的东西。

### §9.27.2 四列的裁决：3 补 1 不补

判据是**逐值比对**，不是「session 侧有没有这个列名」。真库按 `request_id`
配对 `session_turns` × `request_logs`，1,515,960 组：

| 列 | v1 有值行 | session 侧为空 | **两侧都有值但不一致** | 裁决 |
|---|---:|---:|---:|---|
| `token_band` | 186,958 | 53,566 | **0** | 补 |
| `client_forwarded_for` | 379,179 | 177,199 | **0** | 补 |
| `origin_stage` | 1,132,604 | 177,199 | **0** | 补 |
| `trace_events` | 691,883 | **691,883** | 0 | **不补** |

`both_differ = 0` 是这张表最重要的一格：只要 session 侧有值，它与 v1 **逐值
相同**。缺口全部是「v1 有、session 侧空」的**覆盖缺口**，不是语义分歧。

覆盖缺口只在历史，不在当前（镜像在持续补齐）：

| 窗口 | 行数 | `origin_stage` | `client_forwarded_for` | `token_band` | `trace_events` |
|---|---:|---:|---:|---:|---:|
| 近 1 天 | 1,737 | 76% | 76% | 35% | **0** |
| 近 7 天 | 221,328 | 86% | 86% | 18% | **0** |
| 近 30 天 | 1,683,067 | 57% | 12% | 8% | **0** |

**`trace_events` 为什么不补**：它在 `session_turns` 上有列，但镜像**从不写它**
（1/7/30 天三个窗口非空率恒为 0），而 v1 侧有 **691,883 行**带值、且这些行的
`request_id` 已在 `session_turns` 里 ⇒ 会被视图的反连接丢弃。补投影等于把
691,883 行真实值换成 NULL：读方从物理表能看到的比从视图看到的**更多**。这是
§9.18「修好了但变全盲」的同一形状，触发条件是覆盖缺口而非语义分歧。正解是
先让镜像写该列再投影（登记为遗留项）。

### §9.27.3 `id`：你让确认的那条判断，实测确认，且比预想更硬

§9.21.4 提出「`id` 虽然在 `session_turns` 里，但不能进投影清单」，请我确认。
**确认，并且给出比「值域不同」更硬的证据**：

```
同 request_id 配对 1,515,984 组，r.id = t.id 命中 0 次
v1  request_logs.id  值域 34,616 – 2,513,875
   session_turns.id  值域 400,060 – 2,091,912
```

不是「通常不相等」，是**一次都没相等过**。v1 的 `request_logs.id` 是请求行
id、session 侧的 `id` 是 turn id。判据「session 侧的列与 v1 侧的是不是同一个
东西」——不是「session 侧有没有这个列名」。

顺带说一句为什么这个区分值钱：`id` 补投影是本项目里少见的、比 NULL **更坏**
的选择。NULL 至少让读方看见缺失；一个语义已变的同名列会让读方正常地算出一个
错的数。

**「不投影」在代码里的落点（复核过，2026-10-02 补）**：`id` 的 NULL 补位不是 815
引入的，它**源自 710** —— Go 侧 `db/request_logs_view_schema.go:362` 的
`projectionExprsV2` 第 1 列就是 `"NULL::bigint"`，别名由 `canonicalColumnOrderV2`
的首列名 `id` 拼成 `NULL::bigint AS id`。815 原样继承，未改动它。
活库顶层 viewdef 是 `UNION ALL` 三条臂，`id` 在**三条臂上各有一种写法**
（2026-10-02 按 `pg_get_viewdef` 行号实测，不是推测）：

| 行 | 臂的 FROM | `id` 的写法 |
|---|---|---|
| L1 | `session_turns_hot t`（会话 hot） | `SELECT NULL::bigint AS id` |
| L146 | `session_turns t`（会话 cold） | `SELECT NULL::bigint AS id` |
| L291 | `request_logs_hot`（v1，别名 `rl`） | `SELECT rl.id,` — **透传真值** |

⇒ **`id` 永不投影只对会话侧成立。** 会话侧不能给 id（turn id 与请求行 id 语义不同，
1,515,984 组配对相等 0 次）；而 **v1-only 的行必须带着它真实的 `request_logs.id`
出去**，否则只存在于 v1 的历史行会在视图里彻底失去主键。

**我曾把这写成「2 处 ⇒ 会话臂与 v1 臂都补 NULL，决策在两条臂上一致落地」——
那是把 v1 臂当成了会话臂。** 错误来源：只数了 `NULL::bigint AS id` 这个字面量的
出现次数（2），没有去看第 2 处属于哪条臂、也没有检查第 3 条臂里 `id` 是什么写法。
**计数本身没错，归属错了。** 教训：「N 处命中」不仅要写清量的面，还要写清**每一处
属于谁**。

对应地，`db/view_schema_v2_contract_test.go` 的值层门也据此重写为三条臂各自正确的
期望：会话 hot 臂（`req-dual`）与会话 cold 臂（`req-turn-parent`）必须为 **NULL**；
v1 臂（`req-v1-only`）必须**等于源表真值 900001**——只断言「非空」太弱，turn id 也是
非空的，而那正是要防的回归。夹具同步改了：原夹具不给 `id`，而
`cloneTablesFrozenDDL` 只发「列名 + 类型」、不带 NOT NULL 也不带 DEFAULT（生产形态是
`NOT NULL` + `nextval`）⇒ v1 行在 scratch 里恒为 NULL ⇒ **旧版门在 v1 臂上恒真**。

> 订正：本审计早一版把这件事写成「全库唯一 1 处命中」。那个 1 数的是 **815 迁移文件**
> 里的命中数（`grep -c` 正好 1），却被当成了活库终态。**同一个字面量在文件里 1 处、
> 在活库里 2 处**——因为活库那层是 `UNION ALL`。这与本轮反复用到的「名字对得上不等于
> 同一个东西」同族，此处中招的变体是：**数字对得上不等于量的是同一个面**。
> 凡报「N 处命中」，必须写清量的是哪个面（迁移文件 / 某一层视图 / 整条 viewdef）。

### §9.27.4 改动：migration 815 + Go 镜像体同体

815 把顶层 canonical 视图从 115 列重建为 **118 列**，三列接在 `client_ip` 之后：

- **会话分支**：`t.origin_stage` / `t.token_band` / `t.client_forwarded_for` 直映；
- **v1 分支**：恒走 lateral（`source.*` 内层追加 + `h.*` / `p.*` 两臂选列）——
  中层包装链（577/610/696/700 形态）冻结于更早的列集，从不带这三列，所以
  不存在 fp/raw 那种「基础链已自带」的条件化形态；
- **不 DROP 中层/底层**：三列只出现在顶层。

连带修掉的线上缺陷：`admin/compression_stats.go:212` 的 token 分带聚合读
`token_band FROM request_logs_with_current_month`，而该列从未在契约内 ⇒
**每调必 42703**，错误被 `slog.Warn` 吞掉 ⇒ 仪表盘该格长期静默为空。补完投影
后这条查询正常返回，具名豁免（`viewSourcePhysicalOnlyColumnExemptions`）按其
自身规则（豁免失效必须删）随之删除 —— 该表现在是空的，且**这是正确终态**。

真库实证（815 装上后，同一条 SQL 形态，7 天窗口）：

```
 band          |  cnt
---------------+--------
 (NULL→'')     | 577718      ← 走 v1 分支或镜像未覆盖的历史行
 below         |  40207
 forced        |   5952
 preliminary   |   3383
```

**装 815 之前这条查询 42703、返回空；之后返回真实分带分布。** 这不是「少了一个
警告」而是仪表盘上的一格数据从无到有。

改动清单：

| 文件 | 性质 |
|---|---|
| `sql/migrations/startup/815_request_logs_view_stage_band_cff.sql` | 新增，up |
| `sql/migrations/startup/815_request_logs_view_stage_band_cff.down.sql` | 新增，down |
| `installer/cmd/llm-gw-installer/embeddata/startup/815_*.sql`（×2） | 新增，双树同步副本 |
| `sql/migrations/startup/migration_815_test.go` | 新增，4 道静态门 |
| `db/request_logs_view_schema.go` | 改：投影 / 列序 / DDL 组合 / 回退列序 / 注释 |
| `db/view_schema_v2_contract_test.go` | 改：登记表驱动 + 815 语义断言 + 夹具补 `ts` |
| `db/request_logs_view_padded_columns.go`（+ `_test.go`） | 新增：逐列裁决 SSOT + 门 |
| `admin/view_source_columns_contract.go` | 改：移出 3 列 + 写下「移出≠安全」的推论 |
| `admin/view_source_column_contract_test.go` | 改：豁免表清空 |
| `admin/physical_predicate_on_view_source_test.go` | 新增：物理谓词 × 视图源禁令 |
| `admin/credential_monitor_heatmap_probe_predicate_test.go` | 改：删掉已失实的断言与理由 |
| `admin/request_logs_stop_write_classification_test.go` | 改：补位集改为派生 |
| `admin/v1_direct_padded_column_reader_test.go` | 改：39 → 14 重分类 |

### §9.27.5 补完投影之后**必须**新加的那道禁令

`origin_stage` 进了契约 ⇒ 它被移出 `physicalOnlyRequestLogColumns` ⇒
`TestNoPhysicalOnlyColumnsInViewSourcedSQL` 不再拦它 ⇒ 「视图读方用物理谓词」
从「必然 42703」变成「能跑但漏掉探测排除」。`TestNoPhysicalOnlyColumnsInViewSourcedSQL`
挡不住这个（那一列确实在契约里了），所以新增
`TestNoPhysicalPredicateOnViewSource`：按**谓词正文特征串**
（`origin_stage, 'business') = 'business'`）而不是 Go 标识符判定，因为跨包手抄
一份谓词正是 R50 内联漏过整套测试的同一失效模式；命中物理谓词正文的文件若同时
声明 canonical 视图源即报红，`bg` 包按包粒度豁免（它的 6 处调用逐处核对过）。

`TestViewPredicateHasNoPhysicalArm` 顺带钉住「视图变体谓词不许长出
`origin_stage` 臂」——一旦长上，它就和物理谓词同义了，上面那道门失去判据。

### §9.27.6 门自己抓到的两个真错（都是断言命中，不是崩溃）

1. **回退列序按下标取表达式 ⇒ 表达式贴错列名。**
   815 引入了 `preRateColumnOrder()`（pre-736 链回退 = 113 冻结 + 815 三列）。
   它的列序**不是**完整列序的前缀（credits/client_ip 插在 815 三列之前），
   而 `buildSessionProjectionExprs` 当时按 `projectionExprsV2[i]` 取值 ⇒
   pre-736 回退体渲染出 `NULL::double precision AS origin_stage`：**列名对得上
   而列是错的**，UNION ALL 只按位置匹型所以不报错，只是那一列恒 NULL。
   改成**按列名查** `projectionExprByColumn`，并让 `init` 期自校验两表等长。
   是 §9.27 那道三形态断言（读 `t.<col> AS <col>` 文本）当场变红逼出来的。

2. **Go 镜像体与迁移的 lateral 列序不一致。**
   Go 里 815 三列追加在 fp/raw **之后**，迁移里在**之前**。两者都会生成合法
   SQL、列数相同、查询 200，但 viewdef 逐字不等 ⇒
   `TestRequestLogsViewV2EnsureMatchesMigration` 会红。已把两边统一为
   「class, due, 815三列, fp, raw」。

### §9.27.7 夹具的一个潜伏缺陷（本轮第一次断言碰到它）

第一条读 v1 分支 lateral 的断言（815 三列 passthrough）首跑就红：lateral 的
关联条件是 `h.request_id = v.request_id AND h.ts = v.ts`，而旧夹具 seed
**不写 `ts`** ⇒ `NULL = NULL` 为 NULL ⇒ lateral 恒不命中 ⇒ v1 分支的
`request_class` / `due_at` / `fp` / `raw` 全是 NULL。

这不是我引入的：`request_logs.ts` 在真库是 **NOT NULL**，而此前**没有任何断言
碰过 lateral 那条腿**（`client_ip` 走 `v.client_ip` 绕过了 lateral），
所以夹具的这个不忠实一直藏着。已给 seed 补 `ts`。

**与 §9.22.6 同族**：一条从未被执行的路径，缺陷可以无限期地活着；新写的第一
条断言就是它的挖掘机。

### §9.27.8 变异验证（5 次，全部被门抓住，且都是断言命中）

| 变异 | 被谁抓住 | 证据 |
|---|---|---|
| 815 投影 `trace_events`（「顺手补齐」） | `TestMigration815ExcludesTraceEventsAndID` | 报出并附不补的实测依据 |
| 815 投影 `id`（「session 侧有这列，补上即可」） | 同上 | 报出 + 「NULL 补位形态被改掉」 |
| 视图源文件里 AND 一条本地抄的物理谓词 | `TestNoPhysicalPredicateOnViewSource` | 定位到 `credential_monitor_heatmap.go` 并附语义说明 |
| 会话投影加一条未登记的 NULL 补位列 | `TestEveryPaddedSessionColumnHasAVerdict` | 报出列名 `zz_unregistered_pad` |
| 把物理谓词整体替换掉视图变体 | 未被抓住 —— **构建失败**（`bg` 成了未使用 import） | 见下 |
| **清空裁决表**（模拟「门在空输入上平凡通过」） | `TestPaddedVerdictGatesAreNotVacuous` | 报出「补位列 id/test_col/… 无裁决条目（结构性核验，与主门独立）」 |
| **清空 815 的 `$proj$` 块**（否定式判据的原型退化） | `TestMigration815GatesAreNotVacuous` | 报出「三道门会在空投影上平凡通过」 |

最后一条记在这里是因为它**不算证据**：第一次设计变异时只替换了谓词，导致
`bg` import 变成未使用，测试是**编译失败**而不是断言命中。换成一个能编译的
形态（在视图变体之外额外 AND 一条物理谓词，这恰好也是真实会发生的写法）后
才拿到有效证据。**凡不是靠 `t.Error` 变红的「红」，先确认它是断言命中。**

### §9.27.8b 补一道「门非空转」的结构性断言（变异报告的替代证据）

上表 7 次变异证明的是「**改坏会红**」。它**不**证明「输入为空时也会红」——
而这套门里有相当一部分是**否定式判据**（迁移文件里**不**含某段文本、登记表
**不**含某行）。这类判据在一个被清空 / 被截断的输入上全部平凡通过：绿灯是真的绿，
但什么都没测。所以另加两条结构性断言，它们**不需要重跑任何变异就能复核**：

- `db.TestPaddedVerdictGatesAreNotVacuous`：补位集合非空且**不等于 710 形态的规模**
  （防「退回那 30 列」这个头号退化）、裁决表非空且独立再算一遍覆盖、每条理由有
  40 字符下限、`rejectedProjections` 非空且与裁决表不相交、至少存在一条
  `verdictDifferentThing`（id / client_ip 是本项目仅有的两个「同名不同物」，
  它们若被推翻必须连同实测依据一起改表，不许静默消失）。
- `sql/migrations/startup.TestMigration815GatesAreNotVacuous`：`$proj$` 非空、
  `names` 恰为 118 列、迁移文件 ≥2000 字节（防「被截断导致所有 Contains 判据
  失效」）、`id` 仍是 `NULL::bigint`、down ≥1000 字节且带半剥离拒绝、两个
  installer 副本都在。

两条都已变异验证：清空裁决表 ⇒ 报出逐列缺失；清空 `$proj$` ⇒ 报出
「三道门会在空投影上平凡通过」。两次都是断言命中，文件已逐字节还原。

### §9.27.9 真库往返（最强的一条证据）

`TestRequestLogsViewV2EnsureMatchesMigration`（`TEST_PG_DSN` 门控，**本轮实跑
PASS**）在独立 scratch 库 `llmgw_view_v2_contract_test` 上重放
`733 → 710 → 734 → 738 → 740 → 815`，要求 Go 自愈体与迁移产物
**viewdef 逐字节相同**，并验证：反连接去重、`sys:%` 的 NULL 语义、details
叠加、`client_ip` 透传、815 三列在**会话分支与 v1 分支各一条腿**都有值、
以及 down 链（815 → 740 → 738 → 734）逐级还原。

**815 幂等：从「一次性手跑」升级为可重跑的门。** 此前那条证据是「真库实跑 up → 118 /
down → 115 / up → 118」——它要动活库，验收时谁也不敢重跑，于是这条结论只活在那次
运行的记忆里，属于**不可复核的证据**。现已在同一个 scratch 库内跑 `up → down → up`，
并加三条断言：

- re-up 后 viewdef **逐字节**等于 down 前那一份（不一致 ⇒ 815 依赖了它自己没有建立的
  前置状态，线上表现是「回滚后再前滚，视图少列或多列」，每次都会被当成偶发）；
- re-up 后三列**仍有值**，不只是列名回来。列回来了而值是 NULL，等于把「缺源」伪装成
  「已迁移」——这正是这套视图反复出问题的形状；
- 第二次 down 的 viewdef 与第一次**逐字节相同**，顺带钉住 down 的确定性（同一个 down
  跑两次结果不同 = 它偷偷读了库外状态）。

前两条与第三条各做过一次变异验证：把比较对象换成故意错的字符串 ⇒ 红分别落在
`view_schema_v2_contract_test.go:671` / `:695`，都是 `t.Fatalf` 断言命中、零 panic。

**顺带发现：注释引用的先例与先例的实际行为相反。** 815.down 写着「schema_migrations
行按 append-only 惯例保留（**710/740 惯例**）」，而 740.down 末行恰恰是
`DELETE FROM public.schema_migrations WHERE version = '740'`。数了多数派：
**710 / 734 / 738 / 815 保留，只有 740 删**（4 比 1）⇒ 815 的*行为*是对的，
*引用的先例*是错的，已改写注释。**操作后果不是理论**：`schema_migrations` 是
`PRIMARY KEY(version)`，而 up 文件末尾会 INSERT 自己那一行 ⇒ down 之后**朴素重跑 up
会在该 INSERT 处主键冲突、整笔事务回滚，视图停在 115 列**。响亮地失败可以接受，但
「回滚后再前滚」必须先 `DELETE FROM schema_migrations WHERE version='815'`，而这个
顺序现在被上面那道门钉住了。

**down 的级联面**（真库实测）：`DROP ... CASCADE` 会带走
`v_model_health_dashboard` 与 `v_probe_system_health`。二者由
`db.ensureProbeHealthDashboardViews` 在启动期自愈（740.down 同款取舍），但
**在线回滚不经重启**期间它们是不存在的 —— 已写进 down 头部。

---

## §9.29 补位列逐列裁决：真数是 6，不是 30（决策 2）

§9.21 问「30 列补位里其余 28 列要不要现在逐列裁决」，当时的判断是
「需要逐列做语义裁决，属于产品决策」。**真库把这个问题的前提推翻了**：
需要逐列裁决的不是 28 列，是 **6 列**。

### §9.29.1 §9.21 的 30/28 是从 710 形态数出来的，而现网是 734 形态

`db.request_logs_view_schema.go` 里两套形态并存：

- `projectionExprsV2` = **710** 的 113 列投影，30 处 `NULL::` 占位；
- `buildSessionProjectionExprs(withDetails=true)` = **734** 的生效投影，那 30 处
  里的 **24 处**已被 `d.<col>` 顶掉（733 特征层 `session_turn_details`）。

真库 `pg_get_viewdef` 确认现网是 details-joined 体，三条分支里只有 3 处
`NULL::`（会话分支）+ v1 分支同形 ⇒ **生效补位列 = 6**：

```
id, test_col, test_tab_indent, provider_model, credits_rate_multiplier, client_ip
```

**把这组数字拉成一张可复算的账**（此前 §9.19.3 / §9.20 / §9.21 各自用了不同的
分母 37 / 42 / 34 / 30，导致「6 是怎么来的」无法现场验证）：

| 数 | 含义 | 口径 |
|---:|---|---|
| 45 | `request_logs` 上、视图契约上**没有**的列（2026-10-02 实测） | `information_schema` 差集 |
| 5 | 其中 `session_turns` **已有**同名同语义列 ⇒ 纯投影问题 | §9.19.3 的 A 类 |
| 1 | A 类里最终**被投影**的（`trace_events` 镜像从不写 ⇒ 否决） | §9.27.2 |
| 3 | A 类里 `session_turns` **也没有**的（`upstream_protocol` / `api_key_fingerprint` 等零读列） | 随 v1 退役 |
| 30 | 710 在 session 臂做 NULL 补位的列数 | 710 的 `$proj$` |
| 27 | 其中 734 已换成 `d.<col>`（真库覆盖 99.9996%） | 734 的 details JOIN |
| 3 | 沿自 710、至今仍补位（`id` / `test_col` / `test_tab_indent` / `provider_model` 中除 id 外） | 现行视图 |
| **6** | **生效补位列 = 3（沿自 710）+ 2（738/740 追加）+ 1（815 投影后新增口径不变）** | `db.RequestLogsViewPaddedSessionColumns()` |

恒等式（可现场核）：`34 = 27 + 4`（710 补位 34 列 = 27 被 734 换掉 + 4 仍在），
`6 = 4 + 2`（4 列沿自 710 + 738/740 的 2 列）。**§9.21 的「30 列」用的是 710
的分母，§9.20 的「42 列」用的是差集分母** —— 两者的差就是 27。

其中 `credits_rate_multiplier` / `client_ip` 是 738/740 的追加列，另外 4 列
沿自 710。

（711–814 之间**只有 717 重建过顶层视图**，但它的目的是对齐
`request_logs_hot` 的列类型（R36 遗留 #2，新装 42804 阻断），重建走的是
「捕获现有 viewdef → 原样重建」，不改会话投影。所以这 6 列的补位形态分别由
710/734（4 列）与 738/740（2 列）定下，815 之前无人动过。
**我第一版把这一句写成「711 之后没有任何迁移动过」，是错的** —— 是 grep 时把
`CREATE VIEW` 与 `CREATE OR REPLACE VIEW` 混在一起看了；改正后的口径是
「重建过但不改变会话投影」。）

details 覆盖率（这决定了那 24 列是不是真的「有值」）：

| 腿 | turns | details | 缺 details 的 turn |
|---|---:|---:|---:|
| hot | 1,344 | 1,344 | **0** |
| parent | 1,683,104 | 1,683,098 | 6 |

⇒ **99.9996%**。所以 §9.21 那句「读了 session 臂恒 NULL 的补位列 ⇒ 不可直接
改指视图」，对那 24 列是**错的**。

### §9.29.2 这个错误在代码里已经造成了一处承重缺陷

`admin/request_logs_stop_write_classification_test.go` 里的
`sessionArmNullPaddedColumns`（30 列）被族分类器用来判「行级有 / 谓词级空」，
它的权威源是 **migration 710 的 `$proj$` 块**。而该门拿它与 710 比对 ——
**两侧同源，错误互相抵消，于是门一直绿**。这是「门测的不是它声称测的那个东西」
的教科书形态：它确实在校验一致性。

后果方向是反的：族分类器把「行级有值」判成「谓词级空」，于是会**拒绝正确的
判定**（该文件自己的注释就写过这条后果），并让 105 条读端分类里的一批被按错误
前提登记。

修法：改为从 `db.RequestLogsViewPaddedSessionColumns()` 派生（权威在 db 包，
取**生效投影**），权威源从 710 换成 **815**。两侧现在**不同源**（一侧派生自 Go
的生效投影，一侧解析自迁移文件的 `$proj$`），错误不再互相抵消。旧的 30 列清单
降级为 `legacySessionArmNullPaddedColumns710`，**只为差集报告**保留 ——
删掉它，下一个人就只能靠 git log 考古「为什么 §9.21 说是 39 个」。

新增 `sessionArmDetailsSuppliedColumns`（30 列）单独记账：那批列的风险是
「details 缺行时 NULL」（6 行），与补位列的「恒 NULL」不是同一类，混在一个词里
会让两边的账都看不清。`TestPaddedAndDetailsSuppliedSetsAreDisjointAndComplete`
钉住两表互斥且完备，并在 CI 日志里打出差集。

### §9.29.3 6 列的逐列裁决（登记表 = `db/request_logs_view_padded_columns.go`）

| 列 | 裁决 | 依据（真库） |
|---|---|---|
| `id` | **同名不同物** | 1,515,984 组配对 `r.id = t.id` 命中 **0** |
| `client_ip` | **同名不同物** | 见下 |
| `test_col` | 随 v1 退役 | 全仓无读方；v1 侧 2,162,951 行全非空；session 侧无该列 |
| `test_tab_indent` | 随 v1 退役 | 全仓无读方；v1 侧非空 **0**；session 侧无该列 |
| `provider_model` | 随 v1 退役 | v1 非空 **0**/2,162,951；`session_turn_details.provider_model` 非空 **0**/1,683,061 ⇒ **两侧都没有写方** |
| `credits_rate_multiplier` | 会话族无此事实 | session 族上不存在任何倍率列（唯一含 rate 的是 `rate_limit_status`，是状态不是倍率） |

**`client_ip` 是本轮第二个「同名不同物」，而且 740 当年的理由是错的。**
`session_turns.client_ip` 存在（text，非空 202,014/1,683,104），实测：

| 比较 | 命中 |
|---|---:|
| `session_turns.client_ip` = `session_turns.client_forwarded_for` | **202,014 / 202,014** |
| `session_turns.client_ip` = v1 `request_logs.client_forwarded_for` | **202,014 / 202,014** |
| `session_turns.client_ip` = v1 `request_logs.client_ip` | **0** |

即它是一个**写在 `client_ip` 名下的 forwarded-for 副本**。视图的 `client_ip`
契约是 inet 型的真源对端 IP，直映它等于把转发头当成对端。740 的结论「不能直映」
成立，但它写的理由是「`session_turns.client_ip` 为 text 且**未回填**」——
它已回填 12%，**真理由是语义不同**。理由失实的注释比没有注释更贵：它让后来人
按错误前提去「修复」。

### §9.29.4 顺带重分类：39 → 14 → **1**（两次纠错，第二次是量具的错）

`admin/v1_direct_padded_column_reader_test.go` 的登记表用同一份 30 列口径。改用
生效 6 列后，同一批文件里只剩 **14** 个命中——但**这 14 个里绝大多数是假的**。

**第二次纠错（量具的错）**：该门判「读了某补位列」的条件是
`len(findColumnRefs(body, col)) > 0`，即**整条字面量里任何位置**出现该列名就算，
**不判它绑到哪张表**。于是一条
`FROM request_logs_hot rl JOIN providers p … WHERE p.provider_id = $1`
会因 `provider_id` 出现过而命中——可那一列绑的是 `providers` 表。这与
§9.18 / §9.20.1 反复记录的「列名撞车」是同一个错误族，只是这次发生在**门**里。

改为按**限定符归属**判定（`findColumnRefs` 本来就返回 `qualifier`，
`fromJoinRE` 本来就捕获别名，两者一直被忽略）：

| 形状 | 判定 |
|---|---|
| `<qual>.<col>`，`qual` ∈ 本字面量的 v1 别名集合 | 命中 |
| 裸 `<col>`，且本字面量**只有 1 个关系**且它是 v1 | 命中 |
| 限定到别的关系 | **不命中** |
| 多关系字面量里的裸列 | **不可归属**（不猜；点名 + 具名手验） |

结果：**39 → 14 → 1**。唯一成立的是 `cmd/compression-bench/main.go:205`
`SELECT id, … FROM request_logs`（单关系、裸列、确实读 v1 的 `id`）。

另 2 处「不可归属」已手验并登记为具名豁免（`unattributablePaddedRefExemptions`，
带失效自检）：

- `bg/auto_route_settle_worker.go:id` —— 裸 `id` 绑的是派生表 `s`
  （源 `auto_route_selections_hot`）；该字面量里所有真正读 v1 的列都带 `rl.` 前缀。
  文件里另两处 `WHERE id = …` 属于 `UPDATE auto_route_selections_hot`，整条字面量
  不含 v1 关系。
- `domains/streaming/model_alternatives.go:id` —— 该文件读 v1 的那条字面量
  （`FROM request_logs_hot WHERE … GROUP BY canonical_model`）**不读任何补位列**；
  被点名的裸 `id` 在另一条（`models_canonical` 侧）字面量里。

⇒ **S4 退出判据第 2 条从「39 个」经「14 个」收敛到「1 个」**
（`cmd/compression-bench/main.go`，一个 bench 工具）。而且它卡在 `id` 上一列，
`id` 已证明不可投影 ⇒ 只能改读法。

**这两次纠错的方向值得记**：第一次（30→6）纠正的是**判据的输入**（补位集取自
哪个形态），第二次纠正的是**判据的粒度**（列绑定到哪张表）。两次都让数字变小，
而两次的方向相反的风险都真实存在——所以最终结论必须靠**手验那 1 条 + 逐条手验
2 条不可归属**来收口，而不是靠门自己说 1 就是 1。

### §9.29.5 S4 退出判据（三条，替换 §9.21.5）

1. **写入面**：v1 写路径全部并入门控，停写稳定期 ≥ 一个 hot retention（8h）。
2. **读面**：**1 个**真补位读方改视图读法（`cmd/compression-bench/main.go`，
   §9.29.4；`id` 不可投影，只能改读法）。另 38 个「曾被登记但不受补位阻塞」的
   读方改为按需抽样核对 details 缺行（真库 6 行 parent + 0 行 hot）。
3. **历史面**：`assertTaskInTenant` 依赖的 v1 腿 ⇒ v1-only 历史先回填进 session 族。

（另：`cmd/gateway/dual_read_validator.go` 读 `request_type`，该列现由 details
层供值，停写后两侧仍可比——**这一条从判据里划掉**，依据见 §9.29.1。）

---

## §9.30 决策 3：`raw_model_name` 与 `credential_recovery` 恢复 SQL 的端口范围

### §9.30.1 门控那一半（选项 c）已经在 §9.23 落地

`bg/credential_recovery.go` 的 `lookbackCandidateSQL` 用
`request_logs_hot ∪ request_logs` 取「窗口内是否有成功」作为证据，然后据此
做**特权写**（URSM Recover@30 + 投递探测）。停写后证据源冻结 ⇒ 窗口排空 ⇒
`EXISTS(...)` 恒空 ⇒ 候选集恒空 ⇒ 降级绑定在这条路径上永久失去恢复机会而
**不留痕迹**。

并行线已在 `9b8424fd8`（§9.23）修掉：纯函数 `lookbackComparability`
（`s4_stop_write` 稳定原因键）+ 发查询前短路 + 独立的 `skipped` 通道。
**本轮不需要重做，核对后确认已覆盖。**

### §9.30.2 但 (a)/(b) 的前提被真库推翻了：`raw_model_name` 在**四张表里全空**

§8 决策 1 把「补 `RawModelName` 源头字段」当作端口的**前置条件**。真库实测
（**四张表全部持有该列**，快照 2026-10-02 15:20；行数会随写入增长，重测请以
`information_schema.columns` + `count(*)` 为准）：

| 表 | 行数（快照） | `raw_model_name` 非空 |
|---|---:|---:|
| `request_logs` | 2,163,262 | **0** |
| `request_logs_hot` | 3,528 | **0** |
| `session_turns` | 1,683,184 | **0** |
| `session_turns_hot` | 1,442 | **0** |

⇒ **v1 侧也没有任何取值**（migration 485 加了列，从未有人写）。所以「把 v1 的
做法搬过来」这条路不存在，端口的真正前置是**新增一个有正确来源的字段**，而不是
「补齐一个已有字段」。

> 订正：本审计早一版这张表只列了三张（漏 `request_logs_hot`），小标题也写成
> 「三张表里全空」。**结论不受影响**（四张表非空均为 0），但表的集合本身是结论的
> 一部分——漏掉一张就等于没量全，而「v1 侧也没有值」这句话的说服力正来自四张表
> 全查过。

⇒ **v1 侧也没有这个字段**（migration 485 加了列，从未有人写）。所以「把 v1 的
做法搬过来」这条路不存在，端口的真正前置是**新增一个有正确来源的字段**，而不是
「补齐一个已有字段」。

来源是什么：端口要比的是
`COALESCE(rl.outbound_model, rl.client_model) = pm.raw_model_name`，即**上游名**；
而 §9.12.1 已实测 `session_turns.model` 等于 `client_model`（1138/1138），
拿 `model` 顶替会在发生模型映射的绑定上系统性误判（`glm-5.2 → glm-5-2-260617`）。

**⇒ 裁决：(c) 已落地即本轮终态；(a)/(b) 降级为 S4 灰度前的独立工作项，且其
定义被本节改写** —— 不是「补一个已有列」，而是「在镜像与 v1 写路径上记录
绑定解析出的上游原始名」。回填范围**不是全量历史**，而是切换时刻的一个
lookback 窗口（36h）——这一条把 (a) 的成本量级降了一档。

**顺带一条纪律**：近似实现比不修更危险。若为了「让下架判定看起来在工作」而用
`model` 顶替 `raw_model_name`，它会制造一个**持续产出看似合理结论**的假信号，
比现在的「明确不修」更难拆。

### §9.30.3 三问的最终答复（一句话版）

| 决策 | 答复 |
|---|---|
| 1) 4 个投影 | **补 3 个**（`origin_stage` / `token_band` / `client_forwarded_for`）；`trace_events` 等镜像写入后再补；**`id` 不补**已实测确认（0/1,515,984） |
| 2) 28 列逐列裁决 | **前提被推翻**：需要裁决的是 **6 列**，不是 32 列；已逐列裁决并落成登记表 + 门。顺带把 S4 读面从 39 收敛到 **14** |
| 3) `raw_model_name` + 恢复 SQL 端口 | **(c) 已由 §9.23 落地**；(a)/(b) 前提被推翻（v1 侧该列也全空），端口重定义为 S4 灰度前的独立工作项 |

---

## §9.31 遗留（本轮**没有**解决的）

1. **`trace_events` 要镜像先写。** 写路径改动（`internal/sessionv2mirror` 侧）
   + 历史回填，之后才能投影。**在它被投影之前**，任何读方都必须继续读物理表。
2. **`session_turns.client_ip` 是错名副本**（= `client_forwarded_for`，
   202,014/202,014）。视图继续 NULL 补位是对的，但**底表那个列名本身是个坑**：
   将来有人「顺手把 740 的 client_ip 补上」会直接命中它。已在裁决表登记证据，
   但底表的修法（改名 / 补真源 / 删列）需要单独决策。
3. **14 个 `id` 读方**要逐个改读法（§9.29.4）。本轮只完成了重分类，没动代码。
4. **`raw_model_name` 端口**（§9.30.2）—— 定义已改写，成本已重估，未排期。
5. **`v1-only 历史回填**（S4 判据第 3 条）仍未动。
6. **跨字面量运行时拼接的越列仍无静态门**。§9.27.5 那道门按「文件是否声明视图
   源」判定，判不出「这条拼接出来的 SQL 最终源是视图还是物理表」——那道形状
   仍只能靠真库执行门（`TestCredentialHeatmapSQL_ExecutesOnRealDatabase`）。
7. **`deploy/sql/schemas/baseline/01-schema.sql` 早已落后**（它里面的
   canonical 视图是纯 v1 dump，连 710 的会话体都没有）。本轮未动；它不在任何
   契约门的等式里，但「有人拿它当 fresh-install 真值」的风险仍在。

---

## §9.32 方法学留记（这一轮真正学到的东西）

0. **数字要能现场复算。** 同一批列此前在 §9.19.3 / §9.20 / §9.21 里用过 37 / 42 /
   34 / 30 四个分母，于是「6 是怎么来的」谁也答不上。§9.29.1 补了一张恒等式表
   （`34 = 27 + 4`、`6 = 4 + 2`）。**一个跨越多轮的审计文档，分母必须能被读者
   复算**，否则后面每一轮都在引用一个没人能验证的数。

1. **量具要量对对象。** 本轮我自己的门第一次跑红，原因是
   `paddedSessionColumns()` 量的是 `projectionExprsV2`（710 无 details 形态）
   而不是**生效投影**，于是报出 30 列而不是 6 列。同一轮里，仓库里那道
   §9.22 门犯了同一个错（权威源钉在 710），而且**它一直绿着**——因为它拿
   错误源与错误派生式互相校验。**一道门可以持续有效地校验一个错误的前提。**

2. **「恒 NULL」是个会过期的词。** 同一列在同一张视图上，710 形态下是恒 NULL、
   734 形态下 99.9996% 有值。任何以它为判据的门/分类/清单都必须声明**钉在哪个
   形态上**，否则过期时没有任何信号。

3. **不作为也需要门。** 本轮最贵的两个决定是「不补 `trace_events`」和
   「不补 `id`」，而它们**没有任何编译期或运行期信号**。半年后有人「顺手补齐」
   两个洞同时打开。所以它们各自被 `TestMigration815ExcludesTraceEventsAndID`
   钉住，并用变异验证确认过门承重。

4. **一条从未被执行的路径，缺陷可以无限期活着。** §9.27.7 的夹具 `ts` 缺失
   一直存在，因为此前没有任何断言读 lateral 那条腿。新写的第一条断言就是挖掘机。

5. **变异必须能编译才算证据。** 第一次设计「物理谓词禁令」的变异时整体替换了
   谓词，导致 `bg` import 未使用、测试**编译失败**——那不是断言命中。换成一个
   能编译且同样真实会发生的形态之后才拿到有效证据。

6. **共享文档的并发编辑会让提交越界。** 本轮主审计文档正在被并行会话编辑
   （未提交 168 行、占用 §9.26）。直接追加会让本轮提交把对方的在途改动一并
   带上。处置：单独成文、章节号顺延、头部写明归属与合并条件。

7. **门也会有「列名撞车」。** §9.29.4 的 14 → 1 不是数据变了，是门一直把
   `p.provider_id`（绑 `providers` 表）算成读 v1 的补位列。判归属需要
   **限定符**，而 `findColumnRefs` / `fromJoinRE` 从第一版起就返回了它——
   能力一直都在，判据没用。这与 §9.18 的「机械判据的假阳性会让它自己变成死代码」
   同族：那次是假阳性让门被关掉，这次是假阳性让门一直绿着并把工作清单抬高了 14 倍。
   **假阳性的两种后果都要记：被当成噪声而关掉，或被当成结论而放大工作量。**

8. **不可归属的形状要点名 + 具名手验，不要猜。** 多关系字面量里的裸列无法机械
   判归属。猜命中会重复 §9.29.4 的错误，猜不命中会留下静默失明。第三条路是把它
   记进一张带失效自检的具名豁免表：形状变了豁免自动报红，所以它不会变成
   永不更新的占位。
