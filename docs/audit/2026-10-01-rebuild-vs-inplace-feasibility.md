# 重建新库 vs 原地修 —— 可行性裁决（2026-10-01，含同日订正）

任务来源：用户提问「根据 `/tmp/252-session-bodies-columnar-failure.md`，我们能否重建一个新的
gateway 数据库，复制数据，然后做全库切换，是否可行？」

裁决一句话：**不可取——重建解决不了触发这次提问的那个 OOM；原地转 columnar 也不能做，
它会让会话正文读路径慢约 1,600 倍。这张表应当保持 heap。**

> **本文件同日订正**：初版曾推荐「单表原地转 columnar（省 14 GB、20 分钟、零停机）」。
> 该推荐**已撤回**——执行前的索引/查询形态核查证明它会打断生产热路径读。
> 撤回依据见 §五，量级数据见 §六。§三/§四（OOM 定因与重建评估）不受影响。

本轮全部结论来自 252 生产库只读观测 + `ops_probe` scratch 沙箱 A/B。
沙箱已 `DROP SCHEMA … CASCADE`（11 个对象 / 1.56 GB），
生产表 `session_bodies_2026_09` 核验后仍为 `am=heap / 7 索引 / 20 GB`，锁等待 0。

---

## 一、先说结论

| 方案 | 判定 | 一句话理由 |
|---|---|---|
| **A. 重建新库 + 复制 + 全库切换** | ❌ 不可取 | 新库同构 ⇒ 必然复现同一个 OOM；净效果 = 花 58 GB 磁盘 + 数小时 + 停机，换来一个**已经拥有**的状态（该表本来就是 heap） |
| **B. 单表原地转 columnar**（初版推荐） | ❌ **撤回** | 省 14 GB 的代价是 7 个索引全部失效 + 每次会话链加载全扫该分区，实测慢 **1,600 倍** |
| **C. TOAST 换 lz4** | ❌ 无效 | 实测 pglz 39 MB vs lz4 39 MB，**完全相同**；该表上 lz4 是空操作 |
| **D. 保持 heap** | ✅ **采纳** | 唯一零风险选项。20 GB 换的是"不用赌读路径" |

---

## 二、三道闸门的实测数字（重建方案）

### 闸门 1 · 空间：装得下，但只剩 9% 余量

| 项 | 实测 |
|---|---|
| 根盘 | `/dev/vda3` 197 G，已用 116 G，**可用 73 G** |
| `llm_gateway` | **58 GB** |
| ├ `ursm_node_snapshot_min` | 21 GB（heap 17 GB + 2 索引 4 GB） |
| └ `session_bodies_2026_09` | 20 GB（heap 仅 326 MB + **TOAST 20 GB**） |

58 GB 落得进 73 GB，但新旧并存期**余量约 15 GB / 8%**，无容错空间。

### 闸门 2 · 时间：数小时量级

```
COPY (SELECT * FROM session_bodies_2026_09 ORDER BY id LIMIT 50000) TO '/dev/null';
COPY 50000
Time: 263780.692 ms (04:23.781)      -- wait_event = IO/DataFileRead
```

≈ **4.9 MB/s 有效吞吐**（load 5.0 / 4 核、28.9% iowait、14 G 内存可用 6 G、swap 已用 2.2 G）。
单这张表（798,149 行）≈ **70 分钟**；全库 58 GB 另需写盘 + 建索引 + 21 GB 的
`ursm_node_snapshot_min`，现实 **数小时**，全程压在在线生产机上。

### 闸门 3 · 切换窗口

58 GB 做不到零停机。停写追平 = 生产停机数小时；
逻辑复制 = Citus 13.3 + 48 张 columnar 关系 + 26 张白名单父表的对象级兼容面要逐个验，
这部分本身就是数天量级且无先例。

---

## 三、OOM 精确定因（这一节成立，且仍然重要）

原报告写「瓶颈在**这张表的读取本身**」「**2000 行抽样也失败**」。两条都不成立。

| 原报告结论 | 实测 |
|---|---|
| 纯 SELECT 读 2,000 行 > 4 分钟 | 5 万行全字段 detoast + 序列化仅 263.8 s ⇒ 2,000 行 ≈ **10.6 s**，差 23 倍 |
| 2,000 行抽样转换也失败 | 5,000 行抽样、**默认参数**，转换**成功**：105 MB → 31 MB，**5.6 s** |
| 定因为"表结构问题"但未给机制 | 机制如下，已复现 |

> 注：`SELECT count(*) FROM (SELECT * … LIMIT 2000)` 会是 1.162 ms，但那是 count(\*)
> 不 detoast，**不能**用作反驳证据。真正可比的是上面的 COPY 实测。

### 真机制

`alter_table_set_access_method(…,'columnar')` 会把**一个 chunk group 的所有行序列化成单个
StringInfo 缓冲区**再压缩。默认 chunk group = 10,000 行。PostgreSQL 单次分配硬顶
`MaxAllocSize = 1,073,741,823` 字节——**任何** chunk group 的序列化体积逼近它就必然 OOM，
与 `maintenance_work_mem` / `work_mem` / 宿主机内存**全都无关**。

这解释了原报告里最硬的那条证据：两次 OOM 的 buffer 都是 **1,073,387,452，一个字节不差**。
那是「数据相同 → 序列化长度相同 → 撞同一个硬顶」，不是参数问题。

### A/B 复现（同一份数据，唯一变量 = chunk group 行数）

| 臂 | 数据 | `columnar.chunk_group_row_limit` | 结果 |
|---|---|---|---|
| A | 10,000 行 × 200 KB = **2 GB 序列化** | 10000（默认） | ❌ `out of memory`，buffer 1,073,707,360（距硬顶 34,463 B） |
| B | **同一份数据，逐字节相同** | **1000** | ✅ 6.76 s，27 MB → 184 kB，读回 10,000 行 avg 200,009 B 无损 |

补充：`columnar.chunk_group_row_limit` 有效范围 **1000 .. 100000**
⇒ 该旋钮救不了**平均行宽 > 1 MB** 的表（1.5 MB 合成行在 1000 下仍 OOM）。本表 avg 59 KB。

### 为什么全表撞得到、抽样撞不到

生产行宽分布（15,747 行样本，5 个 jsonb 列 `octet_length(concat_ws(…))`）：

| avg | p50 | p90 | p99 | max |
|---|---|---|---|---|
| 59,192 B | **51 B** | 132,611 B | 1,168,535 B | 3,091,854 B |

极度长尾（p50/p99 差 2 万倍）。10,000 行 chunk group 均值 ≈ 590 MB，最大那个冲过 1 GB；
5,000 行样本只有 295 MB ⇒ **抽样在结构上不可能复现全表的 OOM**。
原报告「2000 行也失败」那行记的其实是 `statement_timeout` 超时，**不是 OOM**——
两件事被并进一张表，于是「抽样能复现」成了假的确定性。

---

## 四、订正：原报告的"已固化成果"与生产实际不符

原报告 §七称「`columnar_insert_only_parents()` 白名单已含 **26 张父表** ⇒
未来新建的 session_bodies 分区会自动是 columnar」。**生产实测不成立**：

```

> ### ⚠️ 为什么"26 张表"这个数字必须当真——它是一次 4 小时生产事故的肇因
>
> 2026-10-01 **03:0x → 06:44**，252 发生写入链整体冻结事故
> （`docs/audit/2026-10-01-252-sql-log-audit-round17.md` §〇/§一/§二）：
>
> - 库内 `columnar_insert_only_parents()` 当时被手改成 **21 族大名单**，
>   **其中明确含 `session_bodies`**；
> - `enforce_columnar_trigger`（`ddl_command_end` 事件触发器）据此在**每次任何
>   `CREATE TABLE` 之后**把清单内所有父表的 heap 分区批量转列存——
>   ON CONFLICT / UPDATE 族（不可列存）被无差别转换；
> - 伤亡：sessions upsert 报错 4,284、stats_event_inbox 4,011、
>   session_turns UPDATE 2,976、usage_facts 165；deadlock 36、aborted-tx 29；
> - **05:31:27 → 07:05 写入链整体冻结**，session turn/bodies/outbox 事务整体回滚，
>   **该窗数据不可恢复**（未进 DB 即回滚，无 outbox 可重放）；
> - 根修 06:44：名单恢复正典单族 + 直改回退 13 表 + 换挂回退 2 表 + 潜伏面回退 4 表。
>
> **那份失败报告的时间戳是 2026-10-01 05:00-05:45，正落在事故窗内**，
> 早于 06:44 的根修——它把肇因配置记成了「已固化的成果」。
> **任何人照着它去"恢复 26 张表的白名单"，就会重启这场事故。**
>
> 这是本轮最该被记住的一条：**转述的"成果"必须逐条对生产核验，
> 事故窗内产出的报告尤其如此**——它记录的是事故进行中的状态。

### 列存治理层的三个真缺陷（全部经真库实跑确认/证伪）

| `session_bodies` 分区 | 存储 | 大小 |
|---|---|---|
| 2026_07 / 2026_08 / 2026_10 / 2026_11 / default | heap | 56-57 kB |
| **2026_09** | heap | **20.0 GB** |

⇒ **未来 `session_bodies` 分区仍会是 heap、仍带索引，当前没有定时炸弹。**
好消息。顺带查出列存治理层的三个真缺陷（全部经**真库实跑**确认/证伪）：

| # | 缺陷 | 证据 | 处置 |
|---|---|---|---|
| G1 | `columnar_healthcheck()` 的 `should_be_heap` 只有 6 个 `request_*`/`usage_*` 族，**R17 回退的 20 个族落到 `expected='unknown'`、`compliant=NULL`** | 252 实跑：修复前 754 行里 739 行 unknown | ✅ 已修（commit `958661f79`） |
| G2 | 同函数把**分区化索引的子项**（`relkind='i'`）当表分区报 | 252 实跑：`pg_inherits` 中 641 条 relkind='i'、113 条 'r'；修复后 754 → **113 行**，全为真表 | ✅ 已修 |
| G3 | `auto_rotate_to_columnar()` **一行都返回不了** | `relname`/`amname` 是 catalog `name`，`RETURNS TABLE` 列是 `text` ⇒ `RETURN QUERY` 首行即抛 `structure of query does not match function result type … in column 2`，**`dry_run := true` 同样炸** | ✅ 已修（修后实跑返回 12 行） |

**G1 是最重要的一条**：2026-10-01 事故后，白名单收敛了，但**检测复发的那道门对肇事的 20 个族是瞎的**——
它们再被转成列存时，healthcheck 会报 `unknown` 而不是不合规。修后 `compliant` 非空的分区
从 15 个升到 **99 个**，`session_bodies_2026_09` 现在报 `expected='heap', compliant=true`。

**一处误判的撤回**：本文件初版写「`columnar_heal()` 的 `DECLARE` 段缺 4 个变量，
**一跑就会失败**」。**这条是错的。** `RETURNS TABLE` 的列本身就是隐式 OUT 参数，
不需要 `DECLARE`；我用一个同形的沙箱函数实跑验证，函数正常返回两行。
错因是**只读了源码就下结论、没有实跑**。修正后 `columnar_heal()` 判定为**正常**。

### 未纳管残差：不归类，留作策略决策

修后仍有 14 个分区 / 5 个族为 `unknown`：`request_logs_bodies`（3 个分区**确实都是列存**）、
`routing_decision_log_archive`（default 分区列存、其余 heap）、`candidate_failure_logs`、
`mock_probe_history`、`credential_model_index_archive`。

**我没有替它们归类**，因为两条路都是错的：
- 放进 `should_be_columnar` = 改 `columnar_insert_only_parents()`，那个有双侧契约测试钉住、
  且 R17 刚把它收敛回单族——**扩它正是引爆事故的动作**；
- 放进 `should_be_heap` = 把它们现有且**有意为之**的列存分区标成不合规。

这是**第三种形态**（按策略逐分区列存，不是 insert-only 强制），已写进函数注释说明
`unknown 是合法第三态`，留作存储策略决策。

### 顺带发现：三方一致性门有覆盖漏洞

`sql/migrations/startup/baseline_ensure_functions_contract_test.go` 的三方一致性门
**只盖 `ensure_*`**，列存治理函数可以在三份基线间自由漂移。
新增 `baseline_columnar_governance_contract_test.go` 覆盖 `columnar_*` /
`auto_rotate_to_columnar` / `enforce_columnar_*` 三方逐字一致，并加三条**语义**属性断言
（比逐字比对更强：逐字比对在"三份一起漂"时照样绿，那正是 R17 的形态）。
四个变异全部验证转红：抽掉 `session_bodies` / 去掉 relkind 过滤 / 回退 `::text` / 只漂移一份基线。

- `enforce_columnar_trigger`（`ddl_command_end`，**已启用**）按
  `columnar_insert_only_parents()` 迭代，目前只覆盖 `routing_decision_log`。
  ⇒ 哪天有人把 `session_bodies` 加回白名单，§五/§六 的读路径问题会**对新分区自动复现**。

---

## 五、为什么初版推荐被撤回：columnar 会废掉全部查询索引

Citus columnar **不支持可用的二级索引**。索引对象在目录里、**唯一约束仍然强制**，
但**查询规划器永远不会用它**。实测（同一张表、同一批索引、同一个查询）：

| 表 | 计划 |
|---|---|
| heap | `Index Scan using …_tenant_id_session_id_turn_no_idx` |
| columnar | `Custom Scan (ColumnarScan)` + `Filter`（全扫） |

- 唯一约束有效性单独验过：columnar 表上重复 `(tenant, session, turn)` 仍被
  `duplicate key value violates unique constraint` 拦截 ⇒ **数据完整性无损**；
- 但**读路径**从索引查找退化为过滤扫描。

`session_bodies_2026_09` 的 7 个索引（约 448 MB）全部服务单行定位：

| 索引 | 大小 | 列 |
|---|---|---|
| `…_tenant_id_request_id_partition_date_key` (UNIQUE) | 100 MB | tenant_id, request_id, partition_date |
| `…_tenant_id_session_id_turn_no_partiti_key` (UNIQUE) | 86 MB | tenant_id, session_id, turn_no, partition_date |
| `…_request_id_idx` | 85 MB | request_id |
| `…_tenant_id_session_id_turn_no_idx` | 81 MB | tenant_id, session_id, turn_no |
| `…_session_id_turn_no_idx` | 70 MB | session_id, turn_no |
| `…_pkey` | 26 MB | id, partition_date |
| `…_tenant_id_session_id_partition_date_idx` (UNIQUE) | 8 kB | tenant_id, session_id, partition_date |

### 这些查询真的在跑

| 调用点 | 查询形态 |
|---|---|
| `domains/session/v2/turn_reader.go:100` `LoadChain` | `WHERE b.tenant_id=$1 AND b.session_id=$2 ORDER BY b.turn_no ASC` |
| `domains/session/v2/turn_reader.go:48` `LoadLatestOutbound` | `WHERE tenant_id=$1 AND session_id=$2 … ORDER BY ts DESC LIMIT 1` |
| `domains/sessionsummary/message_source_v2.go:85` | `WHERE b.session_id = $1` |
| `admin/session_detail_v2.go` / `session_turns_v2.go` / `unified_detail.go` 等 | `LEFT JOIN session_bodies_unified b ON …` |

而且 `session_bodies_unified` = `session_bodies_hot UNION ALL session_bodies`（父表全部分区），
**这些查询都没有 `partition_date` 谓词 ⇒ 不做分区裁剪** ⇒ 每次都会扫到 2026_09。

---

## 六、读路径代价实测（撤回的直接依据）

同一批 46,000 行真实数据建成孪生表，只差一个"转不转 columnar"：

| 臂 | 体积 | 索引 |
|---|---|---|
| `heap_s` | **1,099 MB** | 5 个（含 2 UNIQUE） |
| `col_s` | **285 MB** | 全部失效（-74%） |

同一查询（`TurnReader.LoadChain` 的真实形态，命中 2,483 轮）：

| 臂 | 计划 | 延迟 | 扫描行数 |
|---|---|---|---|
| **HEAP（现状）** | `Index Scan` | **1.943 ms** | 2,483 |
| **COLUMNAR** | `ColumnarScan` + `Sort` | **3,111.649 ms** | 46,000（`Rows Removed by Filter: 43,517`） |

**慢 1,600 倍。** 按本次 285 MB / 3.11 s ≈ 92 MB/s 的压缩扫描速率，
外推到生产 798,149 行（≈ 4.9 GB 压缩）⇒ **单次查询约 50 秒**，
且发生在每次会话上下文加载的路径上。**省 14 GB 换 50 秒/次，不可取。**

> 原报告 §六 第 2 条其实已经写对了：「这张表的价值在于**单行取正文**，
> 而 columnar 的优势在**大批量扫描聚合**——两者方向相反」。初版低估了这一条。

---

## 七、其它被排除的路径

| 尝试 | 结果 |
|---|---|
| 调 `maintenance_work_mem` 512MB→2GB | buffer 字节数**完全不变**（1,073,387,452）⇒ 非根因 |
| 调 `work_mem` 256MB | 同上 |
| 小样本转换 | 5,000 / 46,000 行均**成功**（105→31 MB / 1,197→324 MB） |
| `ALTER TABLE … SET (toast_compression=lz4)` | `unrecognized parameter`——本构建不支持表级选项 |
| 逐列 `ALTER COLUMN … SET COMPRESSION lz4` + `VACUUM FULL` | `attcompression='l'` 已生效，但**体积 88 MB → 88 MB，零变化** |
| pglz vs lz4 决定性对照（同一批 2,000 行） | **39 MB vs 39 MB，完全相同** ⇒ lz4 在本表是空操作 |

**关于审计文档里的"lz4 9.53×"**：那是 V1 `request_logs_bodies` 的数据形态，
且是 raw-vs-stored 比值，不是 lz4-vs-pglz 比值。**不能迁移到 `session_bodies`。**
本表两种算法都只有约 2.2×。

---

## 八、执行层面的一个坑

```
SELECT rolname, rolconfig FROM pg_roles WHERE rolname='llm_gateway';
  --> {statement_timeout=30s, idle_in_transaction_session_timeout=60s}
```

生产角色带 **30s 语句超时**护栏。任何维护脚本（转换、VACUUM FULL、批量 DDL）
必须显式 `SET statement_timeout` / 以高权限角色执行，否则会被静默打断。
本轮多次实验正是因此被取消过一次。

---

## 九、顺带修正的容量判断

`ursm_node_snapshot_min` **没有** 25-50 GB 危机（订正 `2026-10-01-bodies-columnar-followup.md` §六）：
实测 2026-09-06 23:56 → 2026-10-01 17:44 = **24.7 天**、42.9M 行、heap 17 GB；
近 3 日 2.77M / 2.93M / 2.40M 行/天 ≈ **1.07 GB/天**；
`>30d` 行数当前为 **0**，与 `URSM_SNAPSHOT_RETENTION_DAYS` 默认 30 天
（`domains/ursm/v2/persist/retention.go`）一致，首个清理点 2026-10-06 23:56。
⇒ 30 天稳态 ≈ **25 GB**（当前 21 GB），取该报告区间的**下限**。

`session_bodies` 父表**仍无 TTL**（R78 订正复核成立），2026_09 的 20 GB 不会自退役。
**但既然转 columnar 不可取、lz4 无效，这 20 GB 就是一个静态持有量**——
不增长即可，靠 `columnar_insert_only_parents()` 保持不在白名单里维持现状。
稳态库容 ≈ 62 GB，当前 73 G 可用**够用**。

---

## 十、自审计

- A1 反驳"2000 行读 4 分钟"时，我先用的 `count(*) … LIMIT 2000 = 1.162 ms` 是**无效证据**
  （count(\*) 不 detoast）。换成真 detoast 的 COPY 实测才敢下结论。
- A2 第一次 B 臂设 `columnar.chunk_group_row_limit = 200` 被拒（范围 1000..100000），
  `SET` 未生效、仍按 10000 跑 ⇒ B 臂也 OOM。**差点把"旋钮无效"写进结论**——
  是 `SHOW` 回显 10000 暴露的。改用 1000 重跑才拿到有效 A/B。
- A3 合成表行宽一开始选错：1000 行 × 1.5 MB 在**最小** chunk 值下仍必然 OOM，无法跨越。
  换成 10,000 行 × 200 KB 才做出真正跨过 1 GB 硬顶的对照组。
- **A4（本文件最重要的一条）**：初版在**没有核查索引/查询形态**的情况下就给出了
  "推荐转 columnar"。执行前补查才发现 columnar 不提供可用索引、
  而 `session_bodies_unified` 不做分区裁剪——**建议本身是危险的**。
  凡是对"单行定位"为主的表谈列存，必须先验 `EXPLAIN` 而非只验体积。
- A5 唯一约束有效性我单独验了（未默认 columnar=无约束）——结论是**仍强制**，
  所以撤回理由只限读路径，不涉及数据完整性。
- A6 第一版 lz4 试验用了 `toast_compression` 表级选项（不被支持），
  且中途被 30s 角色超时打断一次；改逐列语法 + 独立 pglz/lz4 对照后才得出"零收益"结论。
- A7 `columnar_insert_only_parents()` 的"26 张表"来自原报告转述，
  实测只有 1 张——**报告的"已固化成果"需要逐条对生产核验后才能引用**。
  本轮进一步查明：那个数字对应的是 **2026-10-01 03:0x-06:44 生产事故的肇因配置**，
  而原报告写于事故窗内（05:00-05:45），早于 06:44 根修。
- **A8（本轮第二次犯同类错）**：判定「`columnar_heal()` 缺变量、一跑就失败」时，
  我**只读了源码**就写进报告。同形沙箱函数一跑就证明——`RETURNS TABLE` 的列
  本身就是隐式 OUT 参数，不需要 `DECLARE`，函数**正常**。
  上一条 A4 是"没验就推荐"，这一条是"没验就判废"，**两个方向的错都出在同一处**：
  静态阅读替代了实跑。**凡能造沙箱验证的结论，静态判断只能当假设。**
- **A9**：`git checkout -- <file>` 被我当成"还原我的改动"用了一次，
  结果把**我自己尚未提交的修复**一起回退到 HEAD（因为它们只在工作区，没进 index）。
  正确做法是改动一开始就落到独立 worktree 分支。
  连带：`tools/sync-columnar-governance-baselines.py` 的 D 锚点原写成新串的子串，
  重跑会重复插入注释而计数检查仍报 1 —— **幂等锚点必须跨过插入点**。
  两处都被当场抓住（一次是门复跑仍红，一次是脚本重跑后文件哈希未变校验），
  但代价是绕了一圈。**批量改 schema 的脚本要自带"重跑必须无副作用"的性质。**
