# S3 波 4（第三分片）：bodies 腿逐点评估第一片 —— 压缩会话页的停写盲区

> 轮次：2026-10-14 凌晨。本轮目标仍是会话目标本体：完成 request_log 读写向
> session_turns 的转换、停掉 request_logs 相关写入、把查询与 API 切到会话族，
> 并**确认数据在更改前后一致**。本分片从 `TestV1BodiesReadersAreAssessed`
> （删除 `request_logs_bodies` 的唯一前置，27 文件 / 39 调用点）切入，
> 先做「存储可用性复核」再做「逐点评估」。

---

## 0. 先回答目标原话里的那一句：「确认数据的存储可用」

本机 host-first 本地网关（`~/kaixuan/llm-gateway-go-v2`，2.5.8.2620，:8782）
真库实测，翻转点 `storage.request_logs_write_enabled=false` 于
**2026-10-13 07:01:04**：

| 面 | 行数 | max(ts) | 判读 |
|---|---:|---|---|
| `request_logs_hot` | **0** | — | 热表**已排空**（promote 搬空）⇒ 任何直读 hot 的面读到的是 0 行，不是「变少」 |
| `request_logs` | 35,506 | 10-13 07:01:04 | 冻结在翻转点，**与开关翻转时刻逐秒吻合** |
| `request_logs_bodies_hot` | **0** | — | 同上 |
| `session_turns_hot` | 2,314 | 10-14 02:01:44（= 取数时刻） | 持续承接新流量 |
| `session_turns` | 17,967 | 10-13 18:00:09 | 已 promote 的历史 |
| `session_bodies` | 17,967 | 10-13 18:00:09 | **与 session_turns 等行数** |

派生核对（真库 SQL，非代码推断）：

- `request_logs_with_current_month` 总 44,079 = v1 臂 35,506 + 会话臂 8,573；
- **`dup_request_ids = 0`** ⇒ canonical 视图两臂不重计（反连接在停写后仍成立）；
- `bodies_missing_for_turns = 0` ⇒ **每一个 session_turns 都有对应 session_bodies**
  （LEFT JOIN 计数），正文覆盖不是「大部分」，是全量；
- `storage.session_bodies_native_read = true`、`storage.request_logs_write_enabled = false`。

⇒ **存储面可用性成立**：写入侧门生效且冻结点干净，会话族承接全部新流量，
视图双臂零重计，正文覆盖 100%。后续改造可以放心把「读源切到会话族」当作
既定方向，而不是在赌数据没落全。

⚠ 同时坐实一件对本轮很关键的事：**`request_logs_hot` 是 0 行**。
于是「直读 hot 的读面」不再是「新旧对比」问题，而是**当下就是空的**。
本轮找到的缺陷正是这一类形态的变体（见 §2）。

---

## 1. 逐点评估的第一步：把总体拆成两类

`TestV1BodiesReadersAreAssessed` 的总体（27 文件 / 39 调用点）**不是**
「27 个都没评估的文件」。`scanV1BodiesReaders` 的末尾有一段（§9.230/§9.232）：
把 `indirectRequestLogsReaders` 里 `ResolvesTo` 落在 v1 bodies 的文件，
**以及它们的全部切换层消费点**，各按 1 处并入总体。

⇒ 真正还有 **v1 bodies 字面量**的是 **14 文件 / 25 处**（本轮修复后；修复前 26 处）；另外 **13 文件 / 13 处**
是**已经改走切换层**（`db.SessionBodiesSourceSQL()`）的消费点——它们源码里一个
bodies 关系名字面量都没有，正是靠那段并入逻辑才没有从退役证据里消失。

拆开之后本轮才看得见真正的量：**25 处里有 17 处在 `admin/`（生产 API）**，
`cmd/` 6 处（离线工具），`bg/` 与 `db/` 各 1 处。

---

## 2. 本轮修的缺陷：压缩会话页在停写后整页归零（不是"少一个字段"）

### 2.1 现象与量级

`GET /api/admin/compression/sessions`。真库实测（24h 窗）：

| 口径 | 命中 |
|---|---:|
| `request_logs_bodies_with_current_month` 中 `outbound_body IS NOT NULL` 的行 | 7,076（**全部停写前**） |
| `session_bodies_hot ∪ session_bodies` 中同条件 | **20,326**（全部停写后） |
| `request_logs_with_current_month` 总行 | 44,121 |
| 命中**旧** whereClause 的行 | **811（1.8%）** |

⇒ 停写后的会话在这个页面上 **100% 不可见**。这不是字段降级，是整页只剩历史。

### 2.2 根因不在 LATERAL 里 —— 而分类表把它判错过一次

原语句：

```sql
SELECT COUNT(DISTINCT rl.gw_session_id)
  FROM request_logs_with_current_month rl
  LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
 WHERE rl.ts >= $1 AND rl.ts <= $2
   AND rb.outbound_body IS NOT NULL      -- ← 真凶，在顶层 whereClause
   AND rl.gw_session_id IS NOT NULL AND ($3 OR rl.success)
```

`LEFT JOIN` 的 `IS NOT NULL` 落在 `WHERE` 里等价于内连接，所以它同时是**过滤器**。
bodies 腿只指向 v1 ⇒ 停写后 `rb` 对新会话恒 NULL ⇒ 整行被滤掉。

★ 而 `requestLogsStopWriteClassification` 里这条的 Note 写的是
「**列表行仍返回**（compression_strategy 在 session 臂有真值），但 LATERAL 的
`:147` 谓词停写后恒假」——**它只看了 LATERAL，漏掉了顶层那条**。
分类表与真实行为不符时**没有任何门会报错**；这是本轮第二个（也是更贵的）发现。
本轮已把该条改判 `unaffected_by_stop_write`，并在
`bodiesUnaffectedJustification` 里具名登记了论证与量化。

### 2.3 修法（三处，缺一不可）

1. **顶层过滤器改跨族 EXISTS**：`compressionOutboundBodyPredicate`
   （v1 腿 ∪ `db.SessionFamilyBodiesSourceSQL()` 腿）。
   **为什么用 EXISTS 而不是把两条 bodies 源 UNION 起来 JOIN**：双写期同一个
   `request_id` 在两个家族里**同时存在**——本地实测 **11,711 个双生
   request_id**。UNION ALL 后按 request_id 连接是一对多，会直接虚增主查询里的
   `COUNT(*) AS request_count`。EXISTS 只回答「有没有」，不改变行基数。
2. **LATERAL 改双源**：`compressionLatestOrigMsgsLATERAL`。语义**逐字保持**
   「最新一轮」而不是改成 `MAX` 聚合（改聚合会把停写前会话的返回值一并改掉）。
   两族各按自己的键走：v1 腿保留 `ts` 等值（v1 侧实测 35,506/35,506 全等），
   会话腿按 `request_id` 单键 JOIN `session_bodies`（§9.231 列合同**不投影
   `ts`**，等值 JOIN 是 99.85% 不命中的静默行数变化）。
   会话腿直接查 `session_bodies` 的 `session_id` 而不是 turns 家族：前者有
   `idx_session_bodies_session (session_id, turn_no DESC)` 供索引，
   LATERAL 每行一次探测；走 turns 家族会退化成每行全族扫描。
3. **分页下推到 LATERAL 之前**：见 §3。

### 2.4 「前后一致」的正面回答（本轮的核心验收）

| 口径 | 停写前 12,467 会话 | 停写后 7,412 会话 |
|---|---:|---:|
| 旧 v1 腿命中 | 811（6.5%） | **0（0.0%）** |
| 会话腿命中 | 11,646（93.4%） | 7,412（**100%**） |
| 并集命中 | 12,457 | 7,412 |

- 并集在两个方向上都**不低于**原腿（只增不减）；
- 在旧形态**已有取值**的 811 个会话上，并集给出**逐个同值**：
  `same_value 811 / different_value 0 / 因并集变 NULL 的 0`。

⇒ 这是「只修漏、不改数」的证明，而不是「新旧看起来差不多」。

残余的 `msg_reduction` 仍会为 NULL，原因是
`outbound_msg_count` / `outbound_token_est` 的**列填充率**：
710 视图实测停写前 **11.40%**、停写后 **28.34%**（停写后反而更高）。
所以它与 v1 无关，是列填充率问题，本轮只登记不处理。

---

## 3. 顺带挖出的性能陷阱：LIMIT 排在 LATERAL 之后

把谓词改跨族之后，同一窗口耗时从 ~0.3s 涨到 **~9.9s**——贴着本端点的
`context.WithTimeout(…, 10s)`。原因是结构：

```sql
FROM ( …GROUP BY gw_session_id… ) s
LEFT JOIN LATERAL (…) latest ON true
ORDER BY s.last_ts DESC
LIMIT $n        -- ← 排在 LATERAL 之后
```

LATERAL 是 `ON true` 的一对一连接，不改变基数与排序，于是它要对
**全部分组会话**求值才轮到 LIMIT 生效：停写前过滤后只剩千级会话、看起来够快，
停写后候选从 1,125 涨到 9,255，就顶不住了。

**等价性是实测的，不是推理的**（同一固定窗口 2026-10-13 全天、200 行页）：

| 形态 | 耗时 | 结果集 |
|---|---:|---|
| LIMIT 后置（原结构 + 新谓词） | 10.444s | 200 行 |
| LIMIT 前置（现结构） | **0.318s** | 200 行 |
| `diff` | — | **完全一致** |

⇒ 33× 提速，且逐行相同。旧形态在 24h 窗上会直接越过端点超时（返回 500/空），
所以这一条不是"优化"，是**可用性修复**。

---

## 4. 判据与变异验证

判据：`admin/s3_wave4_compression_sessions_test.go`，**同层驱动**——
断言直接打 `compressionOutboundBodyPredicate` /
`compressionLatestOrigMsgsLATERAL` / `compressionSessionsCountSQL` /
`compressionSessionsMainSQL` 这四段**就是发给 PostgreSQL 的文本**。
为做到同层，两条 SQL 的组合从 handler 里提到了包级函数。

| 判据 | 钉的是什么 |
|---|---|
| `…PredicateServesBothFamilies` | 两个 EXISTS 分支（**count==2**，不是 Contains）+ v1 腿在场 + 会话腿是 `SessionFamilyBodiesSourceSQL()` 双腿形态 + 旧 `rb.` 过滤形态不得回归 |
| `…SQLHasNoBodiesJoin` | LATERAL 之外不得再有 bodies JOIN（双生会虚增 `request_count`）；谓词必须被两条 SQL 内嵌 |
| `…PaginationPrecedesLateral` | LIMIT/OFFSET 必须落在分页子查询体内、且 LATERAL 之后不得再有 LIMIT（**位置**判据） |
| `…LateralKeepsBothFamiliesWithoutTSEquality` | v1 腿的 ts 等值条件不得被改动；会话腿不得出现任何 **ts 比较式**；不得出现 `MAX(` 聚合 |

### 4.1 变异验证：**第一轮有两条是假绿，这是本轮最值钱的记录**

| 变异 | 第一版判据 | 加固后 |
|---|---|---|
| M1 会话族腿替换成 v1 腿 | ✅ 红 | ✅ 红 |
| M2 会话腿加 ts 等值（**换别名** `session_turns.ts = sb.ts`） | ❌ **假绿** | ✅ 红 |
| M3 分页被 SQL 注释掉（`-- LIMIT $`） | ❌ **假绿** | ✅ 红 |
| M4 顶层恢复 `rb.outbound_body` 过滤形态 | （变异无效，没还原旧形态） | ✅ 红 |
| M5 分页子查询被删 | — | ✅ 红 |

两条假绿的成因，都值得单独记：

1. **M2：钉的是「某一条字面量」，不是「性质」。** 第一版钉
   `sb.ts = rl3.ts` 这一个别名拼法，换成 `session_turns.ts = sb.ts` 就完全绕过
   —— 而那正是要拦的东西。⇒ 改钉 **ts 比较式的正则**（`\bsb\.ts\s*(=|<>|…)`），
   与别名拼法无关。**判据要钉性质，不能钉拼写。**
2. **M3：注释是位置判据的伪装色。** 把源码里的 `LIMIT $` 改成 `-- LIMIT $`，
   拼出的 SQL **字面上仍然含有** `"LIMIT $4 OFFSET $5"`，任何基于 `strings.Index`
   的判据全绿——而 PostgreSQL 根本不执行它。⇒ 位置判据必须作用在
   `stripSQLComments()` 之后的文本上（本包已有引号感知的实现，复用它）。

这两条与 AGENTS.md 判据纪律第 3、7 条同族，但给出了新的两个具体形态：
**性质 vs 拼写**、**注释 vs 位置**。还原后 5 条全绿。

### 4.2 一处判据自身写错的记录

`…SQLHasNoBodiesJoin` 第一版对 mainSQL 也报红——因为 LATERAL 里**本来就该**
有一条 v1 腿（服务停写前历史）。是判据的锚点选错了，不是实现错了；
处置是把反向断言限定在 `LEFT JOIN LATERAL ( … ) latest ON true` 之外。

---

## 5. 剩余 25 处 bodies 读点的逐点结论（本轮盘点，下一片执行）

分三类。**「已迁」= 源码里已无 bodies 字面量，经 `db.SessionBodiesSourceSQL()`
取源，开关（`storage.session_bodies_native_read`）开时取会话族。**

### A. 已迁（13 文件 / 13 处）—— 退役时只需删 v1 臂，不需改调用点

`admin/auto_title_generator.go` · `admin/compression_stats.go` ·
`admin/logs_summary.go` · `admin/memora_handlers.go` ·
`admin/no_topic_session.go` · `admin/session_compare.go` ·
`admin/session_export.go` · `admin/session_sanitize_matches.go` ·
`admin/session_title.go` · `bg/passive_probe_listener.go`（:183 走
`dbpkg.SessionBodiesSourceSQL()`）· `domains/hooks/goal/history_store.go` ·
`domains/sessionforensics/export.go` · `domains/sessionsummary/summarizer.go` ·
`domains/sessionsummary/system_prompt_prefix.go`

### B. 保留 v1 历史腿 + 已有会话兜底（6 处）—— 退役时删 v1 腿即可

- `admin/body_resolver.go`（2，ts 等值 hot + 母表双腿 + S4 交接时加的
  `session_bodies` 按 `request_id` 兜底腿）
- `admin/logs.go`（4，`fetchRequestBodies` / `fetchRequestOutboundBody` 各两腿，
  两腿 miss 后按 `request_id` 兜底 `session_bodies`）
- `admin/unified_detail.go`（2，`loadOutboundBody` 同款）

⚠ 共性：兜底腿是**纯增量**（旧请求 v1 腿已命中）。退役时若把 v1 腿**删掉**
而不是把会话腿**提为主路**，历史会话会从「有正文」变成「查得到会话、没正文」。

### C. 仍需改指会话族（本片未做，按优先级）—— **每一项都附实测依据**

| 站点 | 停写后真实表现（本地库实测） | 处置方向 |
|---|---|---|
| `admin/data_lifecycle.go`（2） | 7 天增长趋势的 `compressed` 计数只读 v1 ⇒ 停写后新行 `requests` 正常、`compressed=0`，存储增长面板**低报压缩率** | 趋势 CTE 改跨族（`COUNT(DISTINCT request_id)` 双腿聚合，天然不放大） |
| `admin/data_lifecycle_blobs.go`（2） | blob 体量排行与**清理预览**只读 v1 ⇒ 新正文字节数报 0、预览少算可释放字节 | 需要 bodies 双源 + **按 request_id 去重**（双生 11,711）才能给字节数；该处注释自称 30s 预算敏感，属性能裁决读面 |
| `admin/session_bodies_batch.go`（2） | 会话正文批量读（export / compare / summary 走它），ts 等值那条实测 99.85% 不等 ⇒ 本就近乎恒空 | 改指会话族单键源；**注意**该文件的 `= ANY` → `unnest` 性能裁决（实测 17s → 1.8s），改写不许退化 |
| `admin/quality_correlations.go`（1） | `LEFT JOIN request_logs_bodies rb` **投影里一个 rb 列都没用**（纯死 JOIN）；真失效源是 `FROM request_logs` 裸母表 + `is_auto_request=TRUE`（auto/探针流量按设计不进会话族） | 死 JOIN 可直接删；真问题归探针面族（波 2 登记不迁，主拍板） |
| `cmd/scenario_driver/main.go`（2） | 断言的是 `request_logs_bodies_hot` / `request_logs_hot` 的**增量** —— 它验证的正是被停写关掉的那条链路 ⇒ 停写后 delta 恒 0 | 退役或改指会话族；保持原样等于留一个永远红的工具 |
| `cmd/compression-bench/main.go`（1） | ts 等值 JOIN bodies 压缩基准 ⇒ 停写后只覆盖历史 | 改指会话族或明确标注「仅历史」 |

### D. v1-only 是工作本身（3 处）—— 不改，只登记

- `cmd/tools/backfill_session_bodies/main.go`（1）：**读 v1 就是它的职责**
  （从 v1 回填 `session_bodies`）。退役前它是必需工具，退役后失去意义 ⇒
  与 S6 同步退役。
- `cmd/tools/validate_sessions_v2/loader.go`（2）：离线对拍校验器
  （`LoadV1Turns` vs `LoadV2Turns` 逐轮判定镜像漂移），已在
  `bodiesUnaffectedJustification` 具名登记。
- `db/request_logs_view_schema.go`（1）：**切换层自身的 v1 臂**
  （`SessionBodiesSourceSQL()` 在开关关闭时的返回值）。它不是读方，
  是退役时最后要摘的那一臂。

---

## 6. 本轮不动的三件事（沿用既有登记，不是遗漏）

1. **探针面族**（model_probe / credential_selfcheck / today_success_probe 等
   十余个域）：观测对象按设计不进会话族，读视图救不回观测对象 ⇒ 主拍板项。
2. **`trace_events`**：不在 118 列契约内，镜像从不写、视图刻意不投影 ⇒ 结构性缺口。
3. **生产 154/245/252 翻转 `storage.request_logs_write_enabled`**：需 owner 授权；
   前置是生产库跑 7 天 dual-read-drift 零漂移 + 先应用 661 迁移。

---

## 7. 门状态

- `go build ./admin/` / `go vet ./admin/` rc=0；改动文件 `gofmt -l` 零输出
  （仓库里另有两处**存量**未格式化文件 `request_logs_read_inventory_test.go` /
  `request_logs_retirement_exposure_test.go`，本轮未触碰，按 AGENTS.md 不做全量格式化）。
- `admin` 包全量：唯一红 = `TestV1BodiesReadersAreAssessed`（**故意红的负向
  闸门**，总体由 27 文件/39 处降到 **27 文件/38 处**）。
  分类表两道门、静默档计数门、读库存门、本片 4 条形状判据全绿。
- ★ **一处由本轮改动触发、随即被门抓住的连带修正**：给
  `compression_sessions.go` 加上 `dbpkg.SessionFamilyBodiesSourceSQL()` 拼接点后，
  `TestV1ArmLiteralBlindSpotIsPinnedAndTiered` 判红——因为该文件的盲区口径是
  「读 v1 臂视图 + **关系名是字面量** ⇒ 拼接点工具看不见」，一旦有了拼接点它就
  不再满足盲区定义。处置照 `body_resolver.go` / `unified_detail.go` 的先例：
  自 `v1ArmLiteralBlindSpot` 移出、迁入 `v1ReaderIndirectPopulation`，
  **分档域不变**（仍是 Tier ② 对外 API）。变化的只是「工具能不能看见」。

  ★ 这一条顺带说明：**「加拼接点」这类正确改动会让退役工具覆盖面发生迁移**，
  而迁移方向是「变得可见」（好方向）。门红在这里是**在干活**，不是回归。

---

## 8. 判据纪律增量（第 9、10 条）

**第 9 条：判据要钉性质，不是钉拼写。** ts 等值那条判据第一版钉的是
`sb.ts = rl3.ts` 这一个别名拼法，换成 `session_turns.ts = sb.ts` 就绕过——
而两者的**性质完全相同**（都是拿 session_bodies 侧的 ts 去比 turns 侧的 ts）。
⇒ 凡是要禁某一类写法，判据必须匹配该写法的**语法结构**（比较式），
不能匹配某一个具体别名组合。

**第 10 条：SQL 注释是位置判据的伪装色。** 把 `LIMIT $` 改成 `-- LIMIT $`，
拼出的字符串仍然含有 `"LIMIT $4 OFFSET $5"`，`strings.Index` 判据全绿，
而数据库根本不执行。⇒ 凡是对**位置**下断言的判据，必须作用在
`stripSQLComments()` 之后的文本上；「文本里出现了」与「会被执行」是两件事。

（与既有第 3 条「看不见 ≠ 没拦」、第 7 条「同构腿要 count==N」同族，
但这两条给出了此前没记录过的具体伪装形态。）

---

## 9. 回滚

无 DDL、无 settings 变更、无写路径改动。回滚 = revert 本提交；
回滚后该端点恢复"只显示停写前历史"的行为（不是崩溃，是盲）。