# ursm_node_snapshot_min 容量审计（2026-10-02，生产 252）

> 结论先行：**`ursm_node_snapshot_min` 单表 22 GB，占 `llm_gateway` 库 39 GB 的 56%，
> 是全库第一大消费者，且在运行时没有任何读方。** 本表是 URSM v2 节点状态快照
> （`domains/ursm/v2/persist/writer.go`），本次审计为存储优化 v2 目标重新定位靶点时发现。

## 1. 体量与增长

| 指标 | 实测值 |
|---|---|
| 总大小 | **22 GB**（heap 19 GB + pkey 3.58 GB + ts_idx 360 MB） |
| 行数 | 45,639,234 |
| 时间跨度 | 2026-09-06 23:56:50 ~ 2026-10-02 14:32（**25.6 天，从未清理过**） |
| 日增行数 | 均值 1.78M；峰值 3.3M（2026-10-01） |
| 日增体积 | **约 860 MB/天** |
| 当前保留策略 | 30 天（`DefaultSnapshotRetentionConfig`，`URSM_SNAPSHOT_RETENTION_DAYS` 未设置） |

日行数（近 10 天）：10-01 3,309,211 / 09-30 2,934,834 / 09-29 2,766,736 / 09-28 2,862,080 /
09-27 2,640,128 / 09-26 2,587,062 / 09-25 2,142,353 / 09-24 875,766 / 09-23 633,507。
**09-24 起写入量翻了 3~5 倍**，容量基线报告（`docs/perf/capacity-retention-baseline-2026-09-05.md`
§5 记「约 148 MB/天」）已失效——实际是它的 5.8 倍。

**稳态推算**：30 天保留 × 860 MB/天 ≈ **26 GB 长期占用**，是当前库体积的 2/3。

## 2. 写入语义：状态表被当成时序表写

```go
// domains/ursm/v2/persist/writer.go:247
INSERT INTO ursm_node_snapshot_min (...) VALUES (...)
ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING
```

- Writer 的 flush 间隔是 `time.Minute`（`writer.go:27`），但**实测同组合平均间隔 24.8 秒**，
  1 天 3,188,069 行 / 1,008 个 (credential_id, raw_model_name) 组合 = **每组合每天 3,163 个快照**。
  写入侧存在多个实例并发 flush（生产两台网关 172.16.2.209 / .241 各每分钟全量落盘 1,008 组合）。
- 表内基数极低：**2 个 tenant、1 个 provider、1 个 canonical_name、62 个 credential、452 个 raw_model**。
  即：同一份节点状态在 25.6 天里被重复写入了约 **4,570 万次**，行间差异仅 `updated_at_ms` / 延迟等少数字段。

## 3. 读方审计：**运行时零读方**

全仓检索（`FROM ursm_node_snapshot_min`，排除 docs/CHANGELOG 与编译产物）命中仅 3 处：

| 位置 | 性质 |
|---|---|
| `domains/ursm/v2/persist/writer.go:247` | INSERT（写） |
| `domains/ursm/v2/persist/retention.go:206,209` | DELETE（保留清理） |
| `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql:496` | 一次性运维脚本（09-20 已执行） |

**没有任何 SELECT 读方**：无管理端 API、无 dashboard、无运维 worker、无回放/对账路径。
`retention.go` 注释所述用途「快照仅用于 URSM v2 cutover 前后的状态审计/比对」目前只体现为
人工 SQL 取证，不构成运行时依赖。

## 4. 为什么不能列存（与其他表不同的硬边界）

`ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING` 依赖 4 列
唯一约束，**Citus columnar 不支持唯一索引** → 与 `session_bodies` 同一条硬边界，
本表不能转 columnar。要压缩只能走「改 schema / 改保留策略」而非「换存储引擎」。

补充：pkey 含 `tenant_id` / `raw_model_name` 两个 text 列，45.6M 行撑出 **3.58 GB 索引**
（约 82 字节/条），是全库第二大的单索引。

## 5. 体积构成：payload 独占约 91%

最近 1 天按列合计（3,188,069 行）：

| 列 | 1 天体积 |
|---|---|
| **payload (jsonb)** | **895 MB** |
| raw_model_name | 65 MB |
| tenant_id | 18 MB |
| canonical_name | 3.0 MB |
| health_status | 3.0 MB |

`payload` 平均 295 字节/行，键频次（1 天抽样 17,345 行）：

| 键 | 出现率 | 是否与已有列重复 |
|---|---|---|
| `available` / `generation` / `source_priority` | 100% | **是**，行内已有同名列 |
| `updated_at_ms` / `disabled` | 79~81% | 否 |
| `fail_streak` | 65% | **是** |
| `last_probe_latency_ms` / `last_probe_at_ms` | 42% | 否 |
| `last_direct_ok` / `last_attempt_ms` | 34% | 否 |
| `failure_count` / `success_count` / `disable_count` | 30~32% | 否 |
| `last_err` | 24% | 否 |
| `last_ok_ms` | 19% | 否 |
| `manual_reason` / `manual_hold` / `manual_at_ms` / `manual_actor` | 17% | 否 |
| `cool_until_ms` | 16% | 否（`cool_until` 列存在但类型/语义不同） |

两个结构性问题：
1. **4 个键（`available`/`generation`/`source_priority`/`fail_streak`，合计 100%+65%+100%+100% 覆盖）
   是行内 typed 列的纯重复**，且全部以 JSON **字符串**形式重复一遍（`"generation":"43"`）。
2. 其余 ~15 个键是标量，却全部以 jsonb 字符串存储（`"last_ok_ms":"1790121538946"`），
   13 字节时间戳要付 ~20 字节的引号加 jsonb 开销。

## 6. 优化选项（按价值/风险排序，均需拍板后再动）

| 方案 | 稳态体积 | 收益 | 风险 | 性质 |
|---|---|---|---|---|
| **A. 保留期 30d → 7d** | 26 GB → **~6 GB** | 省 ~20 GB；**立即可回收存量 39M 过期行** | 丢失 >7 天的状态审计取证 | **单环境变量**，需重启两台网关；无代码改动、无读方受影响 |
| B. 保留期 → 3d | ~2.6 GB | 省 ~23 GB | 取证窗口更短 | 同 A |
| C. payload 键拆 typed 列 + 去掉 4 个重复键 | payload 895 MB/天 → ~250 MB/天 | **省 ~19 GB/月**，并降低长期稳态 | 需改 writer + migration + 回放兼容 | 代码改动，走正常提交流程 |
| D. 改写为「状态变化才写」（消除 25 秒重复） | 取决于状态变更频率 | 潜在最大 | 改变表语义（时序 → 状态变更日志），读方为零故风险低但需重新定义用途 | 需设计评审 |
| E. 转 columnar | — | — | **不可行**：`ON CONFLICT` 唯一约束硬边界（§4） | 排除 |

**推荐次序**：先 A（立即回收 20 GB、零代码、可回切），观察 7 天后再评估 C。
D 是真正的根治方向但需要先回答「这张表到底要回答什么问题」——零读方的事实
意味着它目前不回答任何问题。

## 7. 派生风险：首次保留清理将在 2026-10-06 发生

表最早行 09-06 23:56，30 天保留 → **首次实际删除在 2026-10-06**。届时需核对：
- `SnapshotRetentionWorker` 的吞吐能否跟上写入速率（每小时 1 次 tick，单轮 10 分钟墙钟，
  每批 5000 行）。按峰值 3.3M 行/天 = 137K 行/小时，需 27.5 批在 600 秒内完成
  （约 21.8 秒/批预算）。**未验证过真实吞吐**——`CleanupOnce` 只在 `deleted > 0` 时打日志，
  在首次到期前无任何日志可观测，这也是本次无法从日志侧确认 worker 健康的原因。
- 删除 3900 万行会产生大量 WAL 与索引膨胀，需预留磁盘并安排 `VACUUM FULL`
  （约需 22 GB 临时空间；当前可用 90 GB，勉强够但需在低峰做）。

建议在 10-06 之前把保留期调到 7d（方案 A），这样存量过期行会被立即清理，
避免 10-06 的集中删除高峰。

## 8. 本次审计未做的事

- 未对生产库做任何变更（本次全部为只读查询）。
- 未验证 `SnapshotRetentionWorker` 的真实删除吞吐（需要先有到期行，或临时缩短保留期观察）。
- 未审计 `request_state_transitions`（3.4 GB，其中 **3.17 GB 是索引**、heap 仅 266 MB，
  索引/堆比 12:1）——疑似同类索引过度问题，值得下一轮单独审计。
