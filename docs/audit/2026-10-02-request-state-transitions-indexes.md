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

## 5. 处置建议（分级，均需拍板）

| 级别 | 对象 | 潜在回收 | 前置条件 |
|---|---|---|---|
| **高置信** | `idx_state_transitions_journey_model_recent` (520 MB) | 520 MB | 已用生产形状 EXPLAIN 证明 planner 拒绝它。仍需确认 `repository.go:136` 的调用方是否在管理端 UI 中暴露 |
| **高置信** | `idx_state_transitions_tenant_request` (472 MB) | 472 MB | 首两列 `(tenant_id, request_id)` 被 `uq_state_transitions_tenant_request_seq` 完全覆盖（后者多了 `seq` 且是唯一约束），无独立价值 |
| **中置信** | `idx_state_transitions_request` (411 MB)、`idx_state_transitions_request_attempt` (141 MB)、`request_state_transitions_pkey` (94 MB) | 646 MB | 需先确认对应代码路径（`repository.go` 的 `WHERE request_id = $2` 无 tenant 限定形状）是否在生产执行 |
| **待定** | `idx_state_transitions_journey_recent` (553 MB)、`idx_state_transitions_journey_node_recent` (413 MB) | 966 MB | 两者服务于 `attempt_facts.go` / `repository.go:189,242` 等形状。**idx_scan=0 只说明这些代码路径在近 9 天未被执行，不等于形状无效**；需逐个确认调用方可达性后再决定 |

**明确不建议动的**：`uq_state_transitions_tenant_request_seq`（2.2M 次，在用）、
`idx_state_transitions_created`（retention 在用）、`uq_state_transitions_legacy_request_seq`
（唯一约束，删了会破坏 ON CONFLICT 契约）。

## 6. 结构性根因（比逐个删索引更值得修）

`request_state_transitions` 与 `ursm_node_snapshot_min` 是同一类问题的两个实例：
**表按「多租户」设计索引，但实际是单租户负载（default 占 99.98%）。**
建议后续在索引设计上把 `tenant_id` 从所有索引首列移除（RLS/租户隔离已在
`app.bypass_rls` 事务内删除路径中单独处理，不依赖索引首列），或按
`(occurred_at DESC)` 等真实收敛列重建。

## 7. 本次审计未做的事

- 全程只读，未对生产库做任何 DDL。
- 未逐个确认 §5「中置信」「待定」两档索引对应代码路径的**生产可达性**——
  这是删索引前的必要前置，本次未完成，因此不建议现在动它们。
- 未验证 `idx_scan=0` 的 9 天窗口是否覆盖了所有业务周期（如月度批处理），
  若存在低频周期任务仍可能命中这些索引。
