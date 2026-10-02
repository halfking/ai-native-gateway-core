# D07 — 热+分区存储

> 域知识库：[docs/audit/playbook/domains/D07-hot-columnar.md](../../../docs/audit/playbook/domains/D07-hot-columnar.md)
> R73 改动面：request_logs archive cadence 与 migration 754 调用边界。
> 状态：**S-01 已由「环境未提供」改为真库实测并完成**（2026-09-29，PostgreSQL 17.10）。
> 实测推翻了此前三轮关于该链路「已接线可用」的结论——详见 §8。

## 1. 审计要点

- 核对 hot 保留、月分区扫描、归档表 RLS/租户字段、幂等与定时器相位。
- `archiveOldRequestLogs` 使用 30m Go context；migration 754 为单长事务、分批 INSERT、无 executable DELETE/COMMIT。
- **已覆盖（原「未覆盖」三项）**：真实 role timeout → 调用方 `SET LOCAL statement_timeout='30min'` 已实测触发；
  历史数据量 → 真库 2,125,857 行月分区已灌数复刻；执行计划 → 已 EXPLAIN 实测并据此修复。

## 2. 业务测试

- [x] B-01：archive cadence 多启动相位定向测试通过

## 3. 数据测试

- [x] D-01：migration 754 function/installer/embed/caller 形状核对（**形状全对、内容全错——见 §8**）
- [x] D-02（2026-09-29 新增）：归档投影列必须存在于基线 DDL 的 request_logs 列集合（跨源交叉校验）
- [x] D-03（2026-09-29 新增）：canonical 与 delivery 两份 754 必须字节一致

## 4. 压力测试

- [x] S-01：兼容 PG EXPLAIN / 大分区实测 —— **发现并修复两个 P1**，见 §8 与 reports/latest.md

## 5. 安全测试

- [x] SF-01：源分区直查、独立归档表、无删除的静态守卫通过

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D07-hot-columnar/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
# ① 归档接线（R73 五条）：每日一次闸门、分钟不敏感、闸门真被调用、
#    statement_timeout 钉在事务内、由小时清理循环驱动。
go test -race -timeout 120s ./bg -run 'TestShouldRunRequestLogsArchive_|TestArchiveOldRequestLogs_' -count=1
# ②a 只读目录门（列名交叉校验 / 双副本字节一致 / promote_* 批游标首列索引 /
#     死函数引用不存在的表）。只读，DSN 指向被审计库本身。数据门不需要该 DSN
#     也能跑（前三道），后两道需要。
D07_S01_PG_URL=postgres://reader@127.0.0.1:5432/llm_gateway?sslmode=disable \
  go test -timeout 120s ./tests/48h-audit/D07-hot-columnar/data/... -count=1
# ②b 归档 SQL 真跑 + 计划形状（2026-09-29 新增，见 §8）。会 CREATE/DROP 一次性
#     scratch 库，故**必须**用维护 DSN（路径为 /postgres、角色有 CREATEDB），
#     且刻意不读 ②a 的 D07_S01_PG_URL——一个变量服务两种角色，会让只读门在
#     误配时悄悄变成写门。无库自动 skip。
D07_S01_ADMIN_URL=postgres://admin:pass@127.0.0.1:5432/postgres?sslmode=disable \
  go test -timeout 300s ./tests/48h-audit/D07-hot-columnar/stress/... -count=1
```

**为什么拆成两个 DSN**：②a 是只读目录审计，②b 要建/删自己的库。用同一个变量时，
把只读 DSN 配给 ②b 会让 ②b 尝试在真库上 CREATE DATABASE；把 ②b 的 DSN 配给 ②a
则让一个只读门挂在一条预期会写的连接上。两者都不是能靠「配的人小心」解决的问题，
所以在代码里互不读取。

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`bg/partition_manager.go`；`installer/cmd/llm-gw-installer/embeddata/startup/754_archive_request_logs_default.sql`
- R79（2026-09-29）：`sql/migrations/startup/756_request_logs_id_index.sql`（新增，含 down）

## 8. 2026-09-29 S-01 实测结论（本计划项此前的「环境未提供」标注已过期）

计划项原标注「环境未提供」，实测时本机 PostgreSQL 17.10 已健康运行 34 小时，
标注属过期。改为实做后，**两项此前登记为「已通过」的结论被推翻**：

| # | 级别 | 发现 | 处置 |
|---|---|---|---|
| P1-a | 致命 | 754 的 `archive_request_logs_default()` **自落地起从未成功执行过一次**。candidates CTE 投影的 11 列里有 2 个在 request_logs 上不存在（真名 `gw_session_id` / `upstream_status_code`），首跑即 SQLSTATE 42703。因函数体是 `format()+EXECUTE` 动态 SQL，CREATE 阶段不校验列，六轮审计 + D-01 形状核对 + SF-01 静态守卫全部放行 | ✅ 已修（canonical + delivery 双副本字节同步）；新增 data 跨源列名门 + stress 真跑门，均过变异检验 |
| P1-b | 致命 | 754 的批游标 `WHERE id > :last ORDER BY id LIMIT 1000` 在 request_logs 上**没有任何可用索引**（无主键，唯一索引仅 `(request_id, ts)`），每批次退化为全分区并行顺序扫描。EXPLAIN 实测：取 1000 行读 **531,262** 缓冲块（≈4.2 GB）；成本 O(rows²/1000)，2.1M 行 = 2126 批 ≈ 8.9 TB 缓冲读 | ✅ 新增迁移 756 建 `request_logs(id)` 索引；新增 stress 计划形状门（自带「建索引前必为 Seq Scan」对照） |

**实测数字**（本机 PG 17.10，2,125,857 行 / 6070 MB 月分区，无其他活动会话）：

| 场景 | 结果 |
|---|---|
| 冷归档（无 id 索引） | **30:00.028 被 statement_timeout 击杀，整笔 ROLLBACK，0 行归档** |
| 冷归档（建索引后） | **25.96s，2,125,857 行全部归档** |
| 热重跑（幂等） | **8.18s，rows_archived=0，归档表仍 2,125,857 行（无重复）** |
| 单批计划（索引前） | Parallel Seq Scan + top-N Sort，531,262 buffers |
| 单批计划（索引后） | Index Scan，230 buffers，1.294 ms |
| 建索引耗时 | 2.33s（2.1M 行分区） |
| 归档表体积 | 497 MB / 源 6070 MB ≈ **8.2%**（754 头注称「≈5% 级」，实测略高，不影响结论） |

**两条必须一起上线的耦合**：只修 P1-a 不修 P1-b，链路会从「毫秒级失败」退化成
「每晚烧 30 CPU 分钟、到点被击杀、整笔回滚、次日重来」的永久活锁——即 R72 对 753
首扫的诊断在生产规模上的复刻，而 R73 的 30min 抬升并不能兜住它。

**顺带登记（未修，owner 裁决）**：归档月表 `request_logs_archive_YYYY_MM` 是 754 建的
**独立 heap 表**，未 ATTACH 到父表 `request_logs_archive`。该父表是 `PARTITION BY
RANGE (ts)` 但**分区数为 0**（唯一会 ATTACH 的旧函数 `archive_request_logs(date)`
已被迁移 331 移除，且 331 本身未进 installer startup 通道）。后果：任何未来读方写
`SELECT ... FROM request_logs_archive` 会静默得到 0 行而不是报错。现状「无任何读方」
属实，但这是个静默陷阱。

## 8.1 R80：F-12 archive ledger 的推迟前提失效 —— 补测增长曲线（2026-09-29）

F-12（§18）把「无 ledger 的全量重扫」定为 Major 并**有意推迟**，理由原文是：

> 「彻底解法是加一张 archive ledger 记录 `(partition, max_id)`，已归档分区直接跳过。
> 该 ledger **本轮仍未实施**——2026-09-29 S-01 实测确认归档链路本身此前从未跑通过一次，
> 在它跑通并量出真实成本之前谈 ledger 优化是空中楼阁。」

**该理由现已失效**：S-01 已经真跑通（25.96s / 2,125,857 行），迁移 756 又补上了
批游标首列索引。推迟所依赖的「链路没跑通」不再成立，于是「是否值得上 ledger」
必须重新用数据回答，不能再沿用。

本轮用独立 SCRATCH 库 `gw_ledger_probe_*`（与 `llm_gateway` 零关联，验证后即 DROP）
按 754 的真实依赖建 fixture：**直接安装仓内那一份 754 文件**（非手抄版），
构造 6 → 12 个过期月分区、每分区 30 万行、含 756 建的 `(id)` 索引。

| 场景 | 结果 |
|---|---|
| 冷跑（6 分区 / 1.8M 行） | ✅ 6 个分区全部归档，`rows_archived` 合计 1,800,000 |
| 热重跑（6 分区 / 1.8M 行） | 5.78s ~ 7.87s，**`rows_archived = 0`** |
| 扩容到 12 分区 / 3.6M 行后的首次跑 | 32.0s（其中含 1.8M 行真实新归档） |
| 热重跑（12 分区 / 3.6M 行） | **10.44s**，`rows_archived = 0` |
| 函数体 `DELETE` 计数（剥注释） | 0 —— F-11「摘要抽取不删源」不变量实测仍成立 |
| 三次归档后源表行数 | 3,600,000 未变 —— 再次实证 F-11 |

**这条实测把 F-12 的定级从「Major」改判，并推翻了一处我的表述**：

1. **增长是线性的，不是二次的。** 过期数据量翻倍（1.8M → 3.6M），热重跑
   5.78s → 10.44s（≈1.8×）。这与 756 补索引后的预期一致：批游标走
   `Index Scan`（S-01：230 buffers / 1.294ms），代价是 O(总过期行数)，
   一次线性扫，而非 §16 那类 O(rows²) 活锁。**F-12 原文「随时间单调增长、
   不会自行收敛」这句是对的，但当时的语气暗示它会失控——实测不支持「失控」**。
2. **绝对量级很小。** 按实测摄入速率 2.1M 行/月（8.18s/2.1M 行）外推：
   1 年历史 ≈ 98s、3 年 ≈ 294s、5 年 ≈ 493s，**均在 30min 预算内**
   （现由 R73 的 `SET LOCAL statement_timeout='30min'` 兜底），且每日只跑一次。
   要撞上 30min 需约 15 年历史 —— 与服务生命周期不符。
3. **结论：不建 ledger 迁移。** 代价（新表 + 每次归档的 upsert + 跨实例一致性
   推理 + 一条新的 fail-closed 门）与收益（每天省几秒 CPU）不成比例，且 ledger
   本身会引入「记录已归档但源分区被重写」这类新的不一致面。**每日闸门已是
   足够且更简单的缓解。**

> 这是本轮第三次同型的自我修正（§16 F-6「非缺陷」错、§19「四列缺失」误报、
> 本节 F-12 定级过重）。共性：**把「代价未测量」直接读成「代价失控」**。
> F-12 当时的结论方向（需要缓解）没错，但**定级与措辞缺一次实测支撑**，
> 于是「Major」这个数字是被猜出来的。推迟它本身是合理的工程判断，
> 但推迟理由写成了「链路没跑通」，那是个**会随 S-01 完成而自动失效的理由**——
> 一个失效的推迟理由比没有理由更糟：它会让下一轮误以为该任务还「没到时候」。

**因此本轮不新增 757 ledger 迁移。** F-12 的状态由「Major 待 ledger」改为
「已缓解 + 实测确认不需 ledger」，触发重估的条件写明：若 (a) 摄入速率较实测
2.1M 行/月上升 10 倍以上，或 (b) 归档频率由每日改为每小时，或 (c) 实测单次
热重跑逼近 30min 预算，再重开 ledger。三个条件任一命中即应重估，而不是继续
按本轮结论推迟。

## 9. R79 续：promote_* 全族普查（S-01 方法的推广）

S-01 只修了 `request_logs` 一族。本轮把「批游标列必须有首列索引」这条推到 hot→partition
的 `promote_*` 全族（真库 29 个函数 / 27 个批游标 / 18 个活函数），结果：

- **Gate A（活函数缺索引）**：18 个活函数里 16 个索引齐全，2 个没有 ——
  `candidate_failure_logs_hot(ts)`（无任何 ts 首列索引）与 `auto_route_selections_hot(ts)`
  （仅有 promote 谓词推不出的部分索引）。**定级 P2 潜在，不是 P1**：按实测摄入速率
  （24 行/h 与 359 行/h），8h 窗口只有 ~190 / ~2,900 行，**都不到一个 5000 行的批次**，
  二次方代价今天根本没被支付。
- **Gate B（死函数引用不存在的表）**：3 个 `promote_*_default_batch` 零调用方，
  且其父表根本没有 DEFAULT 分区。**定级 P3 文档债**（上了膛的枪：接线即 42P01）。
  登记而不删——删 schema 对象是迁移决策。

**一条被证伪的假设**：初见 `candidate_failure_logs_hot` 有 8h47m 的行未超窗被 promote，
判「8h 不变式已破」；追日志后确认上一轮 promote 跑于 23:04:50、截止线 15:04:50，
当时最老行 15:05:32 **尚未到期**，按小时周期的正常滞留上界是 8h+1h。
**不变式没破，是我在验证前就开始归因。**

两道门均**过变异检验**：白名单塞假条目 → 自收缩检查红；Gate A 索引查找改成 `WHERE false`
→ 报出全部 16 个活函数（证明不是象征性抽查）。两道门都 **fail-closed**：
活函数解析不出批游标即红，而不是跳过——这条设计在写门当天就顶出了抽取器只覆盖 7/26 的坑。

详见 reports/latest.md「R79 续」。

## 10. R79 续二：全库批游标普查（把专项门升成全库门）

`promote_*` 是专项普查，本轮改为扫**整个 `pg_proc`**：结果非 promote 族只有 **4 个**批游标函数，
**无新增 P1**。四个分别是：

| 函数 | 游标列 | 性质 | 结论 |
|---|---|---|---|
| `archive_request_logs_default` | `id` | 活，`FROM %I` 动态源表 | R79 已修（迁移 756 建 `(id)` 索引） |
| `archive_request_wal` | `created_at` | **死函数** | `request_wal` 无 created_at 首列索引，但**正确修法是删函数** |
| `ensure_request_logs_partition` | `ctid` | 活 | 系统列不可索引 + `EXIT WHEN drained = 0` 是合法策略，**非缺陷** |
| `repair_request_logs_detached_partitions` | `ctid` | 仅测试引用 | 同上 |

**`archive_request_wal` 为什么是「死函数」而不是「缺索引」**：迁移 331 已声明要删它，
但 331 不在 installer startup 通道，本机 `schema_migrations` 只到 V359，**它至今仍活在真库里**。
补 `(created_at)` 索引等于给一个没人调的函数优化执行计划；正确动作是执行 331 的意图。
登记为 P3 深化，**不替 owner 删 schema 对象**。

**ctid 那一类值得单列**：ctid 是系统列，`CREATE INDEX` 建不了。「缺首列索引」这条规则对它
判红是**规则用错了对象**。门因此把它归入第三类「不可索引」，**只登记不判红并写明理由**——
不写明就等于把它悄悄算进「已覆盖」，那和把部分索引算成有索引是同一类自欺。

**泛化门与专项门的范围必须互斥**：第一版泛化门没排除 `promote_*`，结果它用更粗的可达性判据
把专项门已精确管理的 6 项债原样重报一遍，**这道门会永久红在别人管理的债上**。改为显式
排除并在注释写明「谁负责什么」。共用解析辅助函数只保留一份实现（两份各自演化必然分叉）。

详见 reports/latest.md「R79 续二」。

## 11. R79 续三：非游标查询面 + DEFAULT 分区普查（1 个 P2 + 三条证伪）

前几批的靶子都是「批游标」。本轮换靶子，查查询面上的另外两种风险。

**新 P2｜`stats_event_inbox` 名义分区、实际零分区**：声明 `PARTITION BY RANGE (occurred_at)`
却只有 DEFAULT 一个子分区，**1,419,612 行 / 1298 MB** 全在里面（2026-08-19 → 09-29，41 天）。
`bg/partition_manager.go` 的 `ensureSpecs()` 无任何条目引用它；全仓**零处**
`DELETE/TRUNCATE stats_event_inbox`——消费者只 `markProcessed`，`replaySQL` 还刻意保留
已处理行，是一本只增不减的重放账本。定 P2 不定 P1：声明查询有部分索引兜底，今天不慢；
真实代价是无界增长（实测 10–26K 行/天，尖峰 150K）。

**三条被证伪的假设**（这轮一半价值在「不是」上）：
1. `session_turns` 的索引全带 `ON ONLY`，若 PG 不递归则 6.2GB 热分区缺 `request_id` 索引
   → 实测 5 个分区逐个查 `pg_index`，**每个都有** `*_request_id_idx`。证伪。
2. 计划里 `Seq Scan on request_logs_2026_07/08` 疑似缺索引 → 实测这两个分区
   **本来就是 0 行**，顺序扫是对的。本地空表假象。证伪。
3. 「DEFAULT 装了 1168 MB，分区裁剪全废」→ EXPLAIN 三次复跑否掉：窗口被显式兄弟分区
   **完整覆盖**时 PG 17.10 会裁掉 DEFAULT（关掉 `enable_partition_pruning` 即复现为 Append）。
   剩下的真实结论窄得多，**P3 文档债**：`partition_manager.go:1258` 那句
   「partition pruning 对 WHERE 范围查询仅扫命中分区」对 2026-09-26 之前的数据不成立。

**这道门抓到了我自己手工普查漏掉的那一张**：手工 census 带了
`AND (子分区数) > 1` 的过滤，而 `stats_event_inbox` 恰好只有一个子分区（DEFAULT 自己），
于是被整条抹掉——**恰恰因为它退化，它才不会被那个条件选中**。
写成门后同一条查询没有该过滤，立刻报出 2 张。
教训：**普查脚本里的过滤条件会同时充当「筛选」和「掩盖」。**

详见 reports/latest.md「R79 续三」。

## 12. R79 续四：P1 —— 分页查询的 LIMIT 其实没有约束工作量（已登记，未修）

R79 在存储函数侧抓到「批游标列无索引 → O(rows²)」。本轮在**查询面**抓到同型误解：

```sql
SELECT ... FROM <view> WHERE <ts 范围> ORDER BY ts DESC LIMIT <page_size>
```

**受控对照**（同一天窗口、同一条 `ORDER BY ts DESC LIMIT 10`，只改 FROM 来源）：

| FROM 来源 | 执行时间 | 计划形状 |
|---|---|---|
| 直查 `request_logs` | 0.288 ms | Index Scan，LIMIT 下推 |
| 内层嵌套视图（含 LATERAL，**不含**两个反连接） | 0.120 ms | `Merge Append` + `Limit loops=10`，下推 |
| 完整视图 `request_logs_with_current_month` | **12,390 ms** | `Append(actual rows=404794)`，**下推失效** |

第二、三行**只差两个相关反连接**（视图体末尾对 `session_turns_hot` / `session_turns`
按 `request_id` 的 `NOT EXISTS`）。加上去之后规划器必须先判定每行能否通过反连接，
无法对 UNION ALL 各分支预截断，只能把 404,794 行全部物化、两轮索引探测、再排序。

**直接证据**：`LIMIT 10` 与 `LIMIT 1000` 的 Append 行数**完全相同**（均 404794）；
缓冲区读 **9,216,949 block**（≈70 GB 逻辑读）换回 10 行。
**所以不是深翻页问题**——第 1 页和第 500 页一样贵。

命中面：`admin/logs.go` ctx 预算 30s（`:470`），默认窗口就是一天（`:474-475`），
即**一天窗口已经 12.4s**；`page` 无上限（`:478-481` 只夹下界），
窗口靠 R37 的 366 天上限兜着。

**定 P1 但不由本轮修**：视图被 `admin/logs.go`、`bg/stats_minute_rollup.go`、
`domains/routeincident/store.go`、`db/probe_views_unified.go`、`maas/usage.go` 共用，
且有自愈重建链 + 迁移 575/577/680/696/700/717 + 视图 113/115 列冻结契约测试。
**改视图是迁移 + schema 契约决策。**

**门自己红过三次，每次都是门有缺陷**：
① 按字符串字面量判分页——`admin/logs.go` 用 `fmt.Sprintf` 拼装，FROM 源与 ORDER BY
分处不同字面量，导致对全树最被分页的查询报「已无分页引用」；
② 只认纯标识符——SQL 源都带别名（`"... AS r"`），真实用法几乎全漏，
改为抽字面量内标识符 token 再与目录视图名求交（**用目录当词表**）；
③ **门把自己的源码当成了消费方**——门文件里每个 allowlist 视图名都是字符串字面量、
注释里还有 ORDER BY+LIMIT，删掉一条登记会让交集归零而误触发真空守卫。
**这是最坏的门的失效形态：它因为一个与被审代码无关的理由保持绿色。**
修法：门必须排除自己的目录。

三条修正后基线 PASS（4 条登记），撤掉 P1 登记会**指名红**而非误触发真空守卫。
另分诊出 `v_task_model_ranking`（真阳性候选，未测）、`session_turns_with_current_month`
（混合：一处经 CTE 间接 LIMIT 20）、`v_routable_credential_models`（假阳性，仅测试/错误文案）。
**假阳性也写进登记并注明理由**，让分诊结果留在代码里可查。

详见 reports/latest.md「R79 续四」。

## 13. R79 续五：P1 —— 有测试断言其文本的 SQL，从未被执行过一次

上一轮两条「未测」候选，本轮去测，测出一段**执行即报错**的查询：
`domains/sessionsummary/message_source_v2.go` 的 `v2SessionBodiesBaseQuery`
引用 `t.origin_actor`，而 `session_turns_with_current_month` 的 65 列定值投影里
**没有这一列**（基表 `session_turns` 上有，attnum 99；`db/db.go:2990` 只保证
`request_logs*` 表；全仓无任何 SQL 把它投进 turns 视图）。

**唯一相关的测试只断言文本包含某个 JOIN**，查询能否执行无人看。
源码常量原文实跑：`ERROR: column t.origin_actor does not exist`。

**两条设置路径都中招 → P1**：
`main_pipeline.go:1416` 在 `sessions_v2_compression_read`（默认 true）下
`SetMessageSource(NewPerTurnDigestSource(pool))`，而
`NewPerTurnDigestSource = gated{digest: perTurnDigestSource, fallback: v2SessionBodiesSource}`
——开关开走 digest（同样缺该列）、开关关（默认）走 fallback（同样缺该列）。
唯一可用配置是 `sessions_v2_compression_read=false` 退回 V1。

**第二例 P1 候选**：`domains/routeincident` 8 处查 `diagnostic_runs.route_key` /
`routing_audit_log.reason`；基线 `01-schema.sql:7955` 与真库**双缺**，
全仓无任何迁移添加；`route_key` 只存在于 390 的 `routing_audit_log`（另一张表），疑串表。

**新门 `TestData_GoSQLConstants_PrepareAgainstRealDB`**：抽取仓内 160 条无占位符的
完整 DML 常量逐条 PREPARE（只规划不执行，守住主库只读约束）。
错误按 SQLSTATE 分层：`42703` 判红，`42P01/42704/3F000/42501` 只记录，其他 fail-closed。
结果：133 成功 / 9 已定性 / 9 backlog / 9 本机无法验证。

**门自己红了四次，全是「判错对象」家族**：
① 抽取器只判「含 SELECT」，把 SQL 片段与 DDL 常量也收进来（48 条假语法错）；
② 没排除 SQLite 目录——换方言问错服务器；
③ **登记键 `file::name` 不唯一**：`action_infra.go` 有 6 个同名 `sql` 常量，
自收缩检查命中其一后 `delete()` 抹掉登记，**把另外 5 个失败项的理由一起带走**，
它们随即红在一个与自身 SQL 无关的原因上；
④ 同上。修法：键加行号 + 内容哈希，且只报告、绝不在遍历中改注册表。

**backlog 用棘轮而非永久红**：9 条未分诊项登记不判红（长期红的门会被人习惯性忽略），
配三条断言：新增未登记项→红、登记项开始能规划→红、`len(backlog)` 与常量不符→红。

详见 reports/latest.md「R79 续五」。

## 14. R79 续六：backlog 清空（9 条逐条定性），又挖出 2 个 P1

上一轮 9 条无法规划的 SQL，本轮逐条查证：

| 条目 | 定性 |
|---|---|
| `offerListSQLColumns`（`__mo_modality__`） | **假阳性**：模板占位符，启动探测 `model_offers.provider_modality` 后替换（`:76`/`:120`/`:138`） |
| `internalPersonDaySQL` | 抽取器判错对象：Go 字符串拼接体，正则只抓到第一段反引号 |
| `selectExecutionCols` / `pgSourceColumns` | 抽取器判错对象：无 FROM 的列清单片段 |
| `modelcatalog::insertManualCredentialModelSQL` | **PREPARE 推断限制**（`42P08`，$8 只在 CTE 内被引用） |
| `internal/reasoncap::q` | **真缺陷 → P1-4**：`model_aliases` 无 `alias` 列，应为 `raw_name` |
| `toolexecution::q / qHot / q` | **真缺陷 → P1-3** |

**P1-3｜`tool_usage_stats` 列名漂移**：基线 `01-schema.sql` 与真库 `pg_attribute`
**两个独立 SSOT 逐列一致**（`tool_id`/`usage_date`，无 `tool_name`/`date`），
全仓无任何改名迁移；而 Go 直接 `INSERT INTO tool_usage_stats_hot (… tool_name, date …)
ON CONFLICT (tool_name, date)`。活接线 `cmd/gateway/tool_execution_integration.go:40`。

**P1-4｜`model_aliases.alias`**：基线该表只有 `raw_name`；`internal/reasoncap/pgsource.go:59`
写的是 `ma.alias = $1`。

**一次自我纠正**：此前我曾把 P1-4 误读成 modelcatalog 的问题——用 `paste` 把「键」行与
下一行错误消息配对时**错位了一行**。正确做法是让门逐条打印 `键：…` 与消息。

**分类器新增 `42P08` 一档**（PREPARE 无参数类型的推断限制，非语句缺陷）。

**自收缩检查补上缺失的一侧**：原检查只覆盖「登记项现在能规划」，不覆盖
「抽取器不再采集它」——补过滤后 4 条登记项悄无声息离开语料而门一直绿。
现断言：**每条登记键都必须对应本轮语料里的一个候选**。

backlog 清空（`expectedUntriagedBacklog = 0`），机制保留。
语料 155 条：131 规划成功 / 14 已定性 / 10 本机无法验证。

详见 reports/latest.md「R79 续六」。

## 15. R79 续七：「P1 候选」量成 P1 —— 8 处缺列的可达性核实

缺列是事实，但**会不会真断**上一轮没量。本轮量完，结论 **P1**。

**缺列事实三方一致**：基线 `01-schema.sql:7955` 无 `route_key`；真库 `pg_attribute` 无；
全仓仅有的两处 `ALTER TABLE diagnostic_runs`（445 / 391）都只加 `created_at/updated_at`。
`route_key` 全仓只出现于 **390 的 `routing_audit_log`**（另一张表，疑串表）。

**可达性链**（这是定 P1 的依据，不是缺列本身）：
`cmd/gateway/main.go:3539/3545` `NewStore` + `NewObserver` →
`telemetryClient.AddOnRequestLogPersisted(observer.AsHook())`（**每条落库请求日志**）→
`Observer.Transition` → `writeAudit` → `persistRunInTx` → `INSERT … route_key` → 42703。
admin 侧 `NewRouteIncidentsHandler` 无条件接线，`DiagnosticRunsList` / `AuditLogListByRun` 同样失败。

**一条加重因素**：`observer.go` 的重试循环注释按瞬时冲突设计（「Transient: lock
conflict, transient deadlock… the next persisted row will catch up」），
但 `42703` 是**永久性**错误，每次重试与每条后续请求都必然同样失败。
`maxRetries: 4` ⇒ **每条请求日志触发 5 次注定失败的查询**，退避后只记 warning。
**重试机制对永久性 SQL 错误零收益，只是把热路径的失败成本放大 5 倍。**

登记表已从「P1 候选」升为「P1」并写入完整可达性链；变异（撤登记→指名红）仍通过。

详见 reports/latest.md「R79 续七」。

## 16. R79 续八：非 hot 分区族普查（session_bodies）—— 一条差点写成 P2 的 P3

前面几轮都查 `request_logs` 一族。本轮查全库最大的表 `session_bodies`（8.68 GB / 5 分区），
查询面走 `session_bodies_unified`。

**缺 `(session_id, ts)` 索引**：该族 `session_id` 打头的索引只有
`(session_id, turn_no [DESC])` 三条。`WHERE session_id=$1 ORDER BY ts LIMIT 20`
因此只能 `Bitmap Heap Scan` 出该会话的**全部** body 再外排（实测落盘溢出），
53,851 body 的会话：**835.853 ms / Buffers hit=653,557 read=13,616（≈5.2 GB）+ temp 溢出**。

**但分布一量，定级从 P2 掉到 P3**：
- 817,986 个会话，avg **2.1** / p50 1 / p90 1 / **p99 2** / max 53,851
- 拆来源：真实会话 817,579 个、avg **1.22**、>100 body 的仅 222 个；
  `sys:probe:*` 探针会话 445 个、avg **1,693**、>100 body 的 350 个

真实会话 p99 是 2 个 body，读 2 行拿 20 条毫无压力。成本集中在探针子系统，
而探针会话是否真的走摘要路径本轮没查。**定 P3（潜在）**，修法是补 `(session_id, ts)`。

**与 P1-1 的耦合**：`SessionMetadataCloseHook.OnSessionClosed` 每次会话关闭都调
这条查询，它现在因 `origin_actor` 直接报错。**只修 P1-1 会把这条休眠的昂贵路径
立刻激活**（探针类会话每次 close 付 836 ms）——修 P1-1 时应一并评估补索引。

**可迁移判定**：**定级之前先量分布，不是量峰值。** 触发审计的是最大值，
但只有中位数能告诉你这是不是常态。

详见 reports/latest.md「R79 续八」。

## 17. R79 续九：四个非 hot 族普查（1 个 P2 + 1 个 P3 + 1 条证伪）

| 族 | 体积 | 行数 | ts 打头索引 |
|---|---|---|---|
| `usage_ledger` | 1001 MB | 2,038,536 | ✅ |
| `sessions` | 476 MB | 760,808 | ❌ |
| `request_wal` | 375 MB | 975,156 | ❌ |
| `routing_decision_log` | 275 MB | 921,463 | ✅ |

四族分区结构都健康（DEFAULT 空、边界正确、数据在当月分区），但**仍是月分区**
（`usage_facts` 已按 R68 改日分区）——月内 ts 范围查询不能裁剪。

**P2｜`request_wal` 完全没有保留期机制**（三条独立证据）：
1. 唯一该清理的 `archive_request_wal` 是死函数（R79 续二 已登记，331 未应用）；
2. `drop_old_state_partitions` 函数体**根本没提 request_wal**；Go 侧零 DELETE/DROP/TRUNCATE；
3. **`lifecycle.request_wal_ttl_days` 全仓只命中它自己的声明，零消费方**——
   一个可热更新、界面可见、描述明确、默认 1 天的平台设置，**改了没效果且不报错**。

代价实测：`request_wal_2026_09` = 975,156 行 / 375 MB / 26 天；日峰值 151,875，
近期 8K–12K。**若 1 天 TTL 生效应只留约 1 万行 / 4 MB，实际是声明意图的约 80–100 倍。**
且旧月分区也不被 drop（`_2026_06/07/08` 至今仍在）。

**修法被 columnar 挡住（最实用的一条）**：直觉修法是补 DELETE，但
`EXPLAIN DELETE FROM request_wal` 直接 `ERROR: UPDATE and CTID scans not supported for
ColumnarScan`——因为 `request_wal_2026_08` 是 citus columnar（am 359239）而 09 是 heap。
逐分区验证：09 可执行（Seq Scan），08 报错。
**修复必须先处理 columnar 分区**（转回 heap，或改走 `DETACH + DROP TABLE`）。

**P3｜`sessions` 保留期删除全分区 Seq Scan**：`bg/lite_retention_worker.go:131`
`DELETE FROM sessions WHERE updated_at < ?` → 5 个分区全 Seq Scan（09 估 865,385 行）。
`idx_sessions_status (status, updated_at DESC)` 用不上——首列 status 未被谓词约束。

**一条被证伪**：我怀疑每月 1 日调度的 `archive_routing_decision_log` 会因 columnar 失败。
**证伪**——它末尾是 `ALTER TABLE … DETACH PARTITION` + `DROP TABLE`，**不走 DELETE**，
因此 columnar 安全，且该族被正常清理。
**限制作用于操作类型，不是表**：「同样是 columnar 表」不等于「同样受同一限制」。

详见 reports/latest.md「R79 续九」。

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D07-hot-columnar/）。
知识库入口：docs/audit/playbook/domains/D07-hot-columnar.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§8
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
