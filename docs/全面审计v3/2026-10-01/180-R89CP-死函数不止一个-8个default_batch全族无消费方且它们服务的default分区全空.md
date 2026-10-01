# 180 号 · R89-CP —— 死函数不止一个：8 个 `promote_*_default_batch` 全族无消费方，且它们服务的 `_default` 分区全空

> **日期**：2026-10-01
> **轮次**：R89-CP（第 80 轮，审计第 180 号）
> **类型**：把 139 号的一个死函数**类化 + 交叉验证**（无新增缺陷；一条 P3 扩大化 + 一条运维误导）
> **改动生产代码**：无　**改动数据库**：无（只读 SELECT）
> **上一轮**：179 号（类化 177 号：16 个诊断 label 里只有 2 个进了 `request_logs`）

---

## 〇、起手

台账上一直挂着一个 P3：**遗留死函数 `promote_model_probe_runs_hot_to_partition` 仍存在于库中但永不被调用**（139 号）。

**139 号只点了这一个名字。** 本轮问两个问题：

1. **它是孤例吗？** —— 库里到底有多少个从不被调用的 `promote_*` 函数？
2. **死函数的「危害」到底成不成立？** —— 139 号的理由是「会误导审计者以为该表有归档」，
   那么**如果一张表的分区结构本身就是空的，这个理由还剩多少？**

---

## 一、F1：把 `pg_proc` 里的 `promote_*` 与 `promoteSpecs()` 求差集

**真库现状：29 个 `promote_*` 函数。**
**代码侧 `promoteSpecs()`：21 条，其中 1 条被注释 ⇒ 实际 20 条。**

```
bash: awk '/^func promoteSpecs\(\)/,/^}$/' bg/partition_manager.go \
      | grep -oE '(//)?\s*\{fnName: "[a-z_0-9]+"' \
      | sed -E 's/.*fnName: "//; s/"//'   → 21 条
其中被注释的（// 前缀）：promote_model_probe_runs_hot_to_partition
```

**⇒ 20 个在调 + 9 个不在调。** 那 9 个正是待裁决清单里 139 号点名的那个，加上 8 个 `*_default_batch`。

---

## 二、F2：8 个 `*_default_batch` 的四种检索形态，全部零消费方

| 检索形态 | 命令 | 结果 |
|---|---|---|
| ① Go 代码 | `grep -rn "<fn>" --include=*.go .` | **5 个 = 0**；3 个 = 1，但**全部在 `tests/48h-audit/.../promote_batch_cursor_index_test.go` 的字符串断言里**（`"promote_credit_ledger_default_batch": "P3 (R79)…"`，**是测试注释文本，不是调用**） |
| ② 非 Go 文本 | `--include=*.py/sh/ts/js/json/yaml/yml` | 3 处命中，全在 `deploy/prometheus/rules/partition-health.yml` 与 `scripts/partition/*.sh` —— **只验证存在性，不调用**（见 F4） |
| ③ SQL 侧自动调用 | `pg_trigger` / `pg_rules` / `pg_event_trigger` 中 `LIKE '%default_batch%'` | **各 0 行** ⇒ 无 trigger、无 rule、无事件触发器会调它们 |
| ④ pg_cron | `SELECT … FROM cron.job` | **`ERROR: relation "cron.job" does not exist`** ⇒ 扩展未装，不存在数据库侧定时调用 |

**⇒ 四种形态独立确认：8 个函数零消费方。139 号那条 P3 不是孤例，是一族。**

**⚠️ 它们的来源**：`sql/objects/functions/promote_*_default_batch_interval_integer.sql`（8 个文件）
+ 3 份 baseline schema（各 24 处命中）。
**唯一的创建迁移是 `sql/migrations/startup/336_promote_default_to_partition_functions.sql.bak.skip`** ——
**`.bak.skip` 意味着它在本环境从未执行**（与 `partition_manager.go:36-39` 的 R78 订正注释一致）。

**⇒ 函数是随 baseline schema 装进来的，不是迁移跑出来的。**
**⇒ 这解释了为什么「336 从未执行」与「8 个函数确实存在」两件事同时为真。**

---

## 三、F3：🔴 关键交叉验证 —— **它们服务的 `_default` 分区，全部是 0 行**

139 号那条 P3 的全部危害是「误导审计者以为该表有月度归档」。**那得先有东西可归档。**

**真库 21 张表有 `_default` 分区，逐一 `count(*)`**（⚠️ 分区不在 `public` 下，
`dal` / `orchestrator` / `platform` 三个 schema 各有一张，**不带 schema 前缀会直接报
`relation "audit_logs_default" does not exist`** —— 这个坑我踩了一次）：

| 分区 | 行数 |
|---|---|
| `public.request_logs_default` | **0** |
| `public.request_wal_default` | **0** |
| `public.usage_ledger_default` | **0** |
| `public.routing_decision_log_default` | **0** |
| `public.credential_model_index_default` | **0** |
| `public.request_logs_bodies_default` | **0** |
| `public.credit_ledger_default` | **0** |
| `public.tool_usage_stats_default` | **0** |
| 其余 13 张表的 `_default` | **全部 0** |
| **`public.stats_event_inbox_default`** | 🔴 **387,780** |
| **`public.system_probe_runs_default`** | 🔴 **1,484** |

**⇒ 有 387,780 行的 `stats_event_inbox_default`，恰恰不在那 8 个函数的覆盖范围内。**
**⇒ 8 个函数服务的分区，合计 0 行 —— 它们不仅没人调，**连被服务的数据都不存在**。

### F3-b 追问：那 387,780 行谁来搬？→ **没人搬，而且这是设计**

`stats_event_inbox` 与 `system_probe_runs` 都**只有 `_default` 一个分区，无任何月度分区**
（`pg_get_expr(relpartbound)` = `DEFAULT`，子分区数 = 1）⇒ **结构上就不存在「搬到月度分区」这回事。**

`stats_event_inbox` 的清理路径是**直接 DELETE**：
`bg/partition_manager.go:2039 cleanupOldStatsEventInboxTerminal()`，
条件 `processing_status IN ('processed','dead_letter') AND occurred_at < now() - 7 days`
（`lifecycle.stats_event_inbox_ttl_days` 默认 7，hot-reloadable，有 `< 1 → 7` 下限保护）。

**⇒ 逐日分布恰好落在 TTL 边界上（这是本轮最漂亮的一处自证）**：

| 日期 | 行数 |
|---|---|
| 09-24 | 82,477 |
| 09-25 | 131,269 |
| 09-26 | **142,046** ← 峰值 |
| 09-27 | 11,755 |
| 09-28 | 10,606 |
| 09-29 | 4,664 |
| 09-30 | 3,602 |
| 10-01 | 1,357 |

**日量从 142,046 骤降到 1,357（99%）—— 第一反应是「写入停了」。**
**实际是 TTL DELETE 追上了**：全表 387,780 行里 **`processed` 387,777 / `pending` 4**，
`processed` 的**最老行 = 2026-09-24**，而今天 10-01 ⇒ **正好卡在 7 天 TTL 边界上**
⇒ 09-24 之前写进去的终态行**已被清掉**，剩下的是最近 7 天的量。

**⇒ 逐日递减是「滚动窗口左端被不断切掉」的形状，不是「流量停了」的形状。**
**⇒ 且 `internal/partguard/parents.go:40` 与注释都写明 `stats_event_inbox` 是直接 DELETE 的例外。**

**⚠️ 诚实边界**：那 **4 行 `pending`**（最老 2026-08-19，**已 43 天**）正是注释里点名的
「stuck pending backlog（R27-HC-10，owner decision required）」——
**代码明确声明「active 行永不触碰，包括卡住的 pending 积压」** ⇒ 按 §41 桶②，
**「4 行卡住 43 天」是已知且已声明的，不是本轮新缺陷**。本轮只做登记（`pending` 是唯一的非终态档）。

---

## 四、F4：🔴 一条**运维误导** —— 监控与巡检脚本都指着一个从不执行的函数族

这是本轮**比死函数本身更值得修**的一条。

| 位置 | 内容 | 问题 |
|---|---|---|
| `deploy/prometheus/rules/partition-health.yml:98` | `PartitionPromoteFunctionError` 的 description 写「**promote_\*_default_batch() 函数**在过去 5 分钟内报错」，runbook 常见原因也围绕它 | ⚠️ 表达式本身是 `rate(partition_manager_promote_errors_total[5m]) > 0`，**这个 metric 由 `promoteSpecs()` 的 20 个函数驱动，从不来自 `*_default_batch`** ⇒ **告警文案点名了一个从不执行的函数族** ⇒ 值班的人按 runbook 去查 `*_default_batch` 会**查了个空** |
| `scripts/partition/check-partition-health.sh:478-484` | 断言 `COUNT(*) … LIKE 'promote_%_default_batch'` **必须等于 8**，否则报错并提示「💡 修复：**应用 migration 336 和 339**」 | 🔴 **修复建议是错的** —— 336 在本环境是 `.bak.skip`，**从未执行过**；这 8 个函数是随 **baseline schema** 装进来的。照这条提示去「应用 336」会得到一个已被 `.skip` 明确否决的迁移 |
| `scripts/partition/verify-partition-alignment.sh:203-225` | 逐个验证 8 个函数存在 | 只验存在性，**本身不算错**，但它把「存在」当成「健康」，**与本轮的 0 消费方事实矛盾** |

**⇒ 这正是 §41 桶③「都没说 = 需补文档」的变体，但更糟：不是没写，是**写了一个错的修复指令**。**

**⇒ 同时它也是本轮唯一一个「机制成立 + 今天真的会发生后果」的发现** ——
139 号那条死函数 P3 是「误导审计者」（后果间接），
**而这条是「值班的人会被指向错误的修复路径」（后果直接）**。

**⚠️ 但必须说清后果边界**：巡检脚本目前**只在** `promote_count ≠ 8` 时才报错，
而实际就是 8 ⇒ **今天不会误报**。**风险在「若有人删了这 8 个死函数」时会连锁误报**，
以及**在告警真的响时误导排查方向**。

---

## 五、定级与处置建议（不擅自动手）

| 项 | 定级 | 理由 |
|---|---|---|
| 139 号 P3（`promote_model_probe_runs_hot_to_partition`） | **维持 P3，范围扩大为「9 个」** | 8 个 `*_default_batch` 同族同构，零消费方 |
| 本轮 F4（运维误导） | **新增 P3** | 巡检脚本的修复指令指向一个 `.skip` 迁移；告警文案指向从不执行的函数族 |

**建议修法（三条，全部低成本）：**

1. **删 8 个死函数** ⇒ 同步必须先改 `check-partition-health.sh` 的 `== 8` 断言，
   否则巡检立刻误报（**这就是「先删函数」这件事有隐含顺序依赖的原因**）；
2. **或** 保留函数但把两个脚本的断言从「存在 8 个」改成「**这 8 个是遗留物，不参与健康判定**」——
   **成本更低，且不会因为将来有人清理而误报**；
3. **修 `partition-health.yml:98` 的 description**，把 `promote_*_default_batch()` 改成
   「promote 调度（`bg/partition_manager.go` `promoteSpecs()` 所列 20 个函数）」。

**⚠️ 三条都涉及删除数据库对象或改动运维脚本，属对外可见操作 ⇒ 登记为待裁决，本轮不动手。**

---

## 六、playbook §72 新增

> **§72 把「死代码」和「死代码所服务的对象」分开验 —— 前者只证明「没人调」，后者才决定「危害多大」**

**由来**（R89-CP / 180 号）：139 号以「函数存在但永不被调用」定了 P3，理由是
「会让人误以为该表有月度归档」。**但本轮量到那 8 个 `*_default_batch` 函数服务的
`_default` 分区合计 0 行** ⇒ **「有归档」这个误判连落脚的地面都没有** ⇒ 危害远小于 139 号的表述。

**⇒ 落地三条：**
1. **「N 个死函数」本身不是定级依据** —— 定级要看**它们本该服务的那个对象**：
   对象有数据 ⇒ 死函数是**真缺口**；对象本身是空的/不存在的 ⇒ 死函数是**纯噪音**（P3 或干脆不记）；
2. **⚠️ 差集类结论必须两段做**：先求「声明 vs 消费」差集（本轮：29 vs 20），
   **再对差集里的每一项追一层「它服务的对象是否存在/有数据」** ——
   **只看第一段会把噪音当缺口**；
3. **「没人调」有四种形态**（Go / 非 Go 文本 / SQL 侧 trigger·rule·事件 / pg_cron），
   **本轮形态①还命中了 3 个测试文件里的字符串断言** ——
   **「命中 ≠ 调用」，测试文件里的函数名是断言文本**（§45 的又一次同族印证）。

**⇒ 顺带一条运维向的硬结论：**
**巡检脚本的「修复：应用 migration NNN」这句话必须核对那个迁移是不是 `.skip`。**
**本仓 336 就是 `.bak.skip` 且从未执行** —— **照着做会执行一个被明确否决的迁移。**

**同族**：§45（命中 ≠ 引用）/ §41（注释三桶，这里是「写错的修复指令」桶）/
§12（codegraph 死代码判定不可信）/ §35（阈值来自对象自身）/
§10（查调用方）/ §66（守卫存在 ≠ 可达）/ §71（差集类结论）。
