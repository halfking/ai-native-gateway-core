# request_state_transitions 索引审计（2026-10-02，生产 252）

> 承接 `docs/audit/2026-10-02-ursm-node-snapshot-min-capacity.md` 的派生项。
> 该表 3.4 GB，其中 **3.17 GB 是索引、heap 仅 266 MB（12:1）**，是全库索引占比最高的表。

## 1. 体量

| 指标 | 值 |
|---|---|
| 总大小 | 3.4 GB（heap 266 MB + 索引 3.17 GB） |
| 行数 | 928,190 |
| 时间跨度 | 2026-09-25 14:19 ~ 2026-10-02 14:45（8 天） |
| 日增 | 4 万 ~ 18 万行（10-02 仅 41,693，负载波动大） |
| 保留策略 | 7 天（`domains/requestjourney/retention.go`，1h tick） |
| 状态 | 已接近 7 天稳态，非异常堆积 |

## 2. 根因：tenant_id 无选择性

```
tenant_id=default  927,992 行（99.98%）
tenant_id=chenb        324 行（0.02%）
```

**全表 11 个索引中有 9 个以 `tenant_id` 开头。** 在 99.98% 集中在单一值的前提下，
这些索引的第二列才具备实际选择性，首列 `tenant_id` 不贡献任何收敛。

## 3. 索引使用情况（统计量归零于 2026-09-23 06:56，9 天真实计数）

| 索引 | 大小 | idx_scan | 说明 |
|---|---|---|---|
| `idx_state_transitions_journey_recent` | 553 MB | **0** | EXPLAIN 显示 planner 会选它（见 §4） |
| `idx_state_transitions_journey_model_recent` | 520 MB | **0** | **EXPLAIN 显示 planner 拒绝它**（见 §4） |
| `uq_state_transitions_tenant_request_seq` | 472 MB | **2,234,400** | 唯一约束 + 主查询路径，**在用** |
| `idx_state_transitions_tenant_request` | 472 MB | **0** | 与上一个索引首两列重复（`tenant_id,request_id`） |
| `idx_state_transitions_journey_node_recent` | 413 MB | **0** | 6 列复合，含 `resolved_model` |
| `idx_state_transitions_request` | 411 MB | **0** | `(request_id, created_at DESC)` |
| `idx_state_transitions_request_attempt` | 141 MB | **0** | |
| `request_state_transitions_pkey` | 94 MB | **0** | `(id)` identity 主键 |
| `idx_state_transitions_created` | 93 MB | 952 | **在用**（retention worker 的 DELETE） |
| `idx_state_transitions_journey_retry_at` | 1.4 MB | **0** | 极小，无优化价值 |
| `uq_state_transitions_legacy_request_seq` | 88 kB | **0** | legacy 行唯一约束（`event_type IS NULL`） |

**零扫描索引合计约 2.61 GB**，是全库最大的单点索引浪费。

## 4. 生产查询形状的 EXPLAIN 验证（EXPLAIN ANALYZE，真实 tenant）

### Q2 — `repository.go:136` model 级聚合（无时间窗）

```sql
SELECT model, count(*) FROM request_state_transitions
 WHERE tenant_id = 'default' AND event_type IS NOT NULL
   AND COALESCE(NULLIF(to_model,''),NULLIF(model,''),resolved_model) IS NOT NULL
 GROUP BY model LIMIT 20;
```
```
->  Gather Merge (Workers Launched: 2)
      ->  Sort (actual rows=159 loops=3)
            ->  Partial HashAggregate
                  ->  Parallel Seq Scan on request_state_transitions
                        Filter: (event_type IS NOT NULL AND tenant_id = 'default' AND ...)
                        Rows Removed by Filter: 30478
                        Buffers: shared hit=34107
```

**planner 拒绝了为这条查询专门建的 `idx_state_transitions_journey_model_recent`（520 MB），
转而对全表做并行 seq scan，实读 34,107 个 buffer（约 267 MB）。**
这不是"索引没建好"，是索引因首列无选择性而对 planner 无价值——它每天既消耗
520 MB 空间，又在每次规划时增加候选成本。

### Q1 — `repository.go:111` journey 按 request_id 取序列（2.2M 次调用）

```
->  Index Scan using uq_state_transitions_tenant_request_seq
      Index Cond: (tenant_id = 'default' AND request_id = ...)
      Buffers: shared hit=12
```

**这条 2.2M 次的主路径只用到唯一约束索引，12 个 buffer 命中，代价可忽略。**
即：绝大多数流量根本不碰那些大索引。

## 5. 生产可达性核实（19:20，pg_stat_statements 21 天窗口）

§5 初版把"删索引前需确认代码路径可达性"列为前置条件。核实用
`pg_stat_statements`（窗口 2026-09-11 ~ 2026-10-02，**21 天**，比
`idx_scan` 的 9 天硬）完成 —— 该表 21 天内的全部语句：

| 类型 | 调用数 | 说明 |
|---|---|---|
| INSERT | **5,100,572** | writer 主写链 |
| DELETE | 1,577 | retention（`WHERE created_at < NOW() - interval`），均值 1,978 ms，累计删 5,213,593 行 |
| **SELECT** | **26** | **只有一条形状**，均值 22.32 ms，共 259 行 |
| CREATE INDEX/TABLE/ALTER | 477 + 13 | 启动 EnsureSchema 反复执行 DDL |

那条唯一的 SELECT 全文：
```sql
SELECT tenant_id, gateway_instance_id, request_id, seq, event_type, stage,
       requested_model, resolved_model, model, provider_id, provider, ...
FROM request_state_transitions
WHERE tenant_id = $1 AND request_id = $2 AND event_type IS NOT NULL
ORDER BY seq
```
即 `repository.go:111`，**由保留的 `uq_state_transitions_tenant_request_seq` 独占服务**
（该索引 idx_scan 2,234,400 = INSERT 的 ON CONFLICT + 这 26 次查询）。
`attempt_facts.go` / `repository.go:136,189,242` 的查询形状
**21 天内一次都没有执行过**。

补充核实：
- `pg_constraint WHERE confrelid='request_state_transitions'` → **0 行**（无任何外键引用）
- `pg_depend` 依赖视图/规则 → **0 个**
- 全部 11 条索引 DDL 已留档 `/tmp/rst_index_ddl.txt`

## 5b. 已执行（19:22）

删除 6 个零扫描普通索引，**回收 2,510 MB**（表 3.4 GB → **928 MB**，11 索引 → 5）：

| 已删 | 大小 |
|---|---|
| `idx_state_transitions_journey_recent` | 553 MB |
| `idx_state_transitions_journey_model_recent` | 520 MB |
| `idx_state_transitions_tenant_request` | 472 MB |
| `idx_state_transitions_journey_node_recent` | 413 MB |
| `idx_state_transitions_request` | 411 MB |
| `idx_state_transitions_request_attempt` | 141 MB |

保留 5 个：`uq_state_transitions_tenant_request_seq`（唯一约束 + 唯一真实查询）、
`request_state_transitions_pkey`（94 MB，见下）、`idx_state_transitions_created`
（retention 在用，EXPLAIN 确认 DELETE 仍走它）、`uq_state_transitions_legacy_request_seq`
（legacy 行 ON CONFLICT）、`idx_state_transitions_journey_retry_at`（1.4 MB）。

**`request_state_transitions_pkey`（94 MB）未删**：它需要
`ALTER TABLE ... DROP CONSTRAINT` 而非 `DROP INDEX`，属结构性变更，
不在本次授权范围内。虽然零外键引用 + 零扫描的证据同样成立，仍单列待定。

### 删后验证（真实生产形状）

```
Index Scan using uq_state_transitions_tenant_request_seq
  Index Cond: (tenant_id = 'default' AND request_id = ...)
  Buffers: shared hit=10 read=1
Planning Time: 4.204 ms     ← 删前 11 索引时为 5.570 ms（−24%）
```
稳态实测 5 次：**0.034 / 0.049 / 0.092 / 0.167 / 3.638 ms**（首次含冷缓存）。
retention DELETE 计划不变（仍 `Index Scan using idx_state_transitions_created`）。
写入链路正常（近 5 分钟 153 行，latest 19:24:29），锁等待 0。

### ⚠️ 一次假警报：基线量具选错了形状

删后第一次复测报出 **Execution Time 33,855 ms**，比删前基线（0.280 ms）劣化 12 万倍。
**根因是基线本身有问题**：基线查询为了合成 `request_id`，自己加了一个
`ORDER BY occurred_at DESC LIMIT 1` 子查询，而该子查询恰好依赖被删的
`idx_state_transitions_journey_recent`。而真实生产查询的 `request_id` 是
**绑定参数、根本没有子查询**。

换回真实形状后耗时 0.034~0.092 ms，**与删前无实质差异**。

教训：判断"删索引是否安全"的量具必须是**生产实际执行的形状**，
不能自己拼一个"看起来像"的查询来造基线——否则会同时产生
假警报（本例：33.8 秒）和假安全感（本例：0.280 ms 里有一半是靠被删索引拿到的）。
判据来源应是 `pg_stat_statements` 的实际查询文本，不是代码里的函数名。

### 唯一保留待定项

`request_state_transitions_pkey`（94 MB，`id BIGSERIAL PRIMARY KEY`）：
零外键引用、21 天零扫描，证据与已删的 6 个同样成立，但删除需
`ALTER TABLE ... DROP CONSTRAINT`（结构性变更，非 `DROP INDEX`），
未在本次授权范围内。表若将来需要按 `id` 定位或被外键引用，该约束不可逆，
故单列待定而非一并删除。

### 回滚

11 条原始 DDL 留档于 252 的 `/tmp/rst_index_ddl.txt`，逐条 `CREATE INDEX` 即可重建。


## 6. 结构性根因（比逐个删索引更值得修）

`request_state_transitions` 与 `ursm_node_snapshot_min` 是同一类问题的两个实例：
**表按「多租户」设计索引，但实际是单租户负载（default 占 99.98%）。**
建议后续在索引设计上把 `tenant_id` 从所有索引首列移除（RLS/租户隔离已在
`app.bypass_rls` 事务内删除路径中单独处理，不依赖索引首列），或按
`(occurred_at DESC)` 等真实收敛列重建。

## 7. 本次审计未做的事（初版记录，19:22 后已失效，保留以留痕）

> 以下三条是**审计初版**的未做项。§5 用 pg_stat_statements 21 天窗口完成了
> 生产可达性核实，§5b 已据此执行删除 6 个索引。**「全程只读」与
> 「未确认可达性」两条已完成，「月度批处理」一条仍为残余风险。**

- ~~全程只读，未对生产库做任何 DDL~~ → §5b 已执行 `DROP INDEX` × 6。
- ~~未逐个确认索引对应代码路径的生产可达性~~ → §5 已用 pg_stat_statements
  21 天窗口完成：21 天内该表只有一条 SELECT 形状被执行 26 次。
- **仍然未做**：未验证 21 天窗口是否覆盖了所有业务周期（如月度批处理、
  季度结算等低频任务）。若存在此类任务命中已删索引，会退化为
  `idx_state_transitions_created` 上的扫描或顺序扫描。窗口 21 天对
  "日常读路径"的覆盖是充分的，对"月度任务"不是——这一条残余风险
  需要按业务日历另行确认，本审计无法从数据库侧证伪。

