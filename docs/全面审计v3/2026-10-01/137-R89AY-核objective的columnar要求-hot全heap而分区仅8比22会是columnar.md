# 137 号｜R89-AY：核 objective 明写的「hot+分区（**columnar**）表」—— **hot 侧 100% heap（正确），分区侧只有 8/22 会是 columnar**

- 日期：2026-10-01
- 轮次：R89-AY
- 起因：objective 明写「**所有**的大数据表是以 hot+分区（**columnar**）表来完成，
  更新、删除只能在 hot 表中进行。hot 只保留 8 小时」。本轮逐表核**分区到底是不是 columnar**。
- 结论先行：
  1. **hot 侧 100% 是 heap —— 这是正确且必要的**（objective 自己要求「更新、删除只能在 hot 表中进行」，
     而 columnar 不支持行级 UPDATE/DELETE）。
  2. **分区侧只有 8/22 个 `ensure_*_partition` 会建出 columnar**；
     2 个显式 heap、**12 个完全没有声明访问方法**（⇒ 落 PostgreSQL 默认 heap）。
  3. **数据量最大的三张表的主分区全是 heap**：`request_logs` 2.15M / `session_turns` 1.56M /
     `usage_ledger` 2.07M。
  4. **同一张表相邻月份访问方法不同** ⇒ **跨月查询存在性能断崖**。
  5. **诚实定性：这不是缺陷，是「需求描述与实现不一致」。**
     objective 的「columnar」在 UPDATE-heavy 表上被**有理由地放弃**（代码注释写明理由）。
     **该做的是把边界写清楚，而不是继续声称「所有大数据表都是 columnar」。**

---

## 一、实测：hot 与分区的访问方法全谱

（`pg_class.relam` → `pg_am`，本库 PG 17 + Citus columnar）

### 1.1 hot 表：**全部 heap** ✅

```
candidate_failure_logs_hot   heap     dashboard_access_events_hot  heap
credit_ledger_hot            heap     model_probe_runs_hot        heap
request_logs_bodies_hot      heap     request_logs_hot            heap
request_wal_hot              heap     routing_decision_log_hot    heap
session_turns_hot           heap     tool_usage_stats_hot        heap
usage_ledger_hot             heap
```

⇒ **10/10 是 heap，且这是对的**：objective 要求「**更新、删除只能在 hot 表中进行**」，
而 columnar 存储不支持行级 UPDATE/DELETE ⇒ **hot 必须是 heap，否则 objective 自己就自相矛盾**。

### 1.2 分区：**三种状态混杂**

| 访问方法 | 分区 | 估算行数 |
|---|---|---|
| **columnar** | `request_logs_bodies_2026_09/10/11` | **25,922,234**（全库最大表） |
| **columnar** | `routing_decision_log_2026_07/09/10`、`_archive_2026_08`、`_default` | 932,546（09 月） |
| **columnar** | `model_probe_runs_2026_07/09/10` | 0 |
| **columnar** | `usage_ledger_2026_08` | 0 |
| **columnar** | `request_wal_2026_08` | 0 |
| **heap** | `request_logs_2026_09` | **2,154,225** |
| **heap** | `session_turns_2026_09` | **1,558,025** |
| **heap** | `usage_ledger_2026_09` | **2,065,828** |
| **heap** | `request_wal_2026_09` | 986,502 |
| **heap** | `candidate_failure_logs_2026_09` | 85,735 |
| **heap** | `credit_ledger_*` / `tool_usage_stats_*` / `dashboard_access_events_*` | 0 / -1 |

⇒ **数据量最大的三张 UPDATE-heavy 表，主分区全是 heap**；
**唯一的巨型 columnar 表是 `request_logs_bodies`（正文，25.9M 行）**——
**即「只读大对象用 columnar，频繁更新的元数据用 heap」这套设计确实在跑。**

## 二、`ensure_*_partition` 的声明情况：**8 会是 columnar，14 不会**

| marker | 个数 | 函数 |
|---|---|---|
| **COLUMNAR**（显式 `USING columnar`） | **7** | `handoff_logs`、`model_probe_runs`、`next_month_archive`、`next_month_cmi_archive`、`next_month_routing_archive`、**`request_logs_bodies`**、`supplier_errors` |
| **HEAP**（显式注释） | **2** | `candidate_failure_logs`、`usage_ledger`（`RAISE NOTICE '… created % as heap'`） |
| **UNKNOWN**（**完全没声明**） | **13** | `auto_route_selections`、`cache_metrics`、`credential_model_index`、`credit_ledger`、`dashboard_events`、`next_month_request_wal`、`request_logs`、`request_wal`、`routing_decision_log`、`session_module_executions`、`session_turn_details`、`tool_usage_stats`、`usage_facts_daily` |

**UNKNOWN 的判读**：DDL 里既无 `USING columnar` 也无 `USING heap` ⇒ **落 PostgreSQL 默认 = `heap`**
（本库实测 `pg_settings` 中**不存在** `default_access_method` 这个 Citus GUC ⇒ 无自定义默认）。

⇒ **未来的月份分区里，只有 8/22 是 columnar，其余 14 个是 heap。**
⇒ **objective 里「所有大数据表…（columnar）」这句话，在本仓**不成立**。**

## 三、一个操作性后果：**跨月性能断崖**

**同一张表，相邻月份的访问方法不同**：

| 表 | 早期分区 | 当前月分区 |
|---|---|---|
| `usage_ledger` | `2026_08` = **columnar**（0 行） | `2026_09` = **heap**（2,065,828 行） |
| `request_wal` | `2026_08` = **columnar**（0 行） | `2026_09` = **heap**（986,502 行） |

⇒ **任何跨 8/9 月的查询都会同时扫 columnar 与 heap**，
而 columnar 表**只对分析型全表扫描友好**、不支持点查与索引下推
⇒ **月度边界处的查询计划会出现断崖**，且**表越大、断崖越明显**（2M 行的 heap vs 空 columnar）。

⚠️ **这不是「配错了」，而是历史遗留**：
早期月份在 columnar 期建成、当前月份在 heap 期建成，**两套策略没有统一**。

## 四、诚实的定性与建议

### 定性：**需求描述与实现不一致**（不是缺陷）

objective 写「所有大数据表…（columnar）」，但代码里对 UPDATE-heavy 表**有明确的放弃理由**：

> `// *_default stays heap; the matching month partition (after migration) is also heap`
> `// (columnar cannot hold UPDATE-heavy data)`

⇒ **这个取舍是合理的**：columnar 不支持行级 UPDATE/DELETE，
而 `request_logs` / `session_turns` / `usage_ledger` 都需要终态回填
（token/成本/延迟的 UPDATE）——**改成 columnar 会直接让回填写不进去。**

### 建议（**登记为待裁决，不擅自动手**）

1. **改文档而不是改代码**：把「所有大数据表是 hot+columnar 分区」
   改成**按表族分列的事实**（只读大对象 columnar / 高频更新元数据 heap）。
   **现在的表述会让运维按错误的前提做容量与性能规划。**
2. **给 13 个 `UNKNOWN` 的 `ensure_*_partition` 补上显式 `USING heap|columnar`**——
   **「靠数据库默认」是本条 12 个函数的问题所在**：
   换库、换 PG 版本、换 Citus 配置都可能让它们**静默翻转**，
   而这正是本会话反复命中的「**默认值即契约**」问题。
3. **统一月度边界策略**：要么全 heap，要么全 columnar；
   现状（08 columnar / 09 heap）让**每次跨月查询都要付出混合代价**，
   且随时间推移**只会累积更多混合分区**。
4. **明确 `request_logs_bodies` 是 columnar 这一点**——
   它是全库最大的表（25.9M 行），**是「columnar 用在哪里」的正确样板**，
   值得在文档里作为正面案例写出来，而不是笼统一句「都用 columnar」。

## 五、playbook §34（本轮新增）

**「X 表都应该具备 Y 属性」这类需求描述，必须逐表实测属性，而不是采信描述。**

- 本轮实测推翻了「所有大数据表都是 columnar」：**hot 侧 100% heap（正确），
  分区侧只有 8/22 会是 columnar，数据量最大的三张表全是 heap。**
- **且「未声明」与「显式声明 heap」必须分开计数**：
  本轮 22 个函数里 **2 个显式 HEAP、13 个完全未声明**——
  **「靠默认」不是一种设计选择，它是「设计缺失」的一种伪装**。
  把它算进「设计为 heap」会掩盖真正的风险：**换环境即可能静默翻转**。

⇒ **落地纪律**：核对「全库是否都有属性 X」时，
① 逐表实测；② **把「未声明/靠默认」单列一档**；
③ 若发现混用，**报告要给出「混用会导致什么」的具体后果**（本例：跨月性能断崖），
而不是只列一张状态表。

**同族**：§22（零导入是相对量）、§31（先数再定性）、§28（终态缺失的歧义）。
**共同点：全都是「**需求/注释里的整齐描述，比实际状态整齐**」——
而整齐的描述最容易被直接采信，直到有人去逐个数。**
