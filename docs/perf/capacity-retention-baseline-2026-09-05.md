# 容量/保留策略基线（2026-09-05 本地开发库实采）

> 采集环境：本地开发库（docker 容器 `llm-gateway-pg`，PG 用户/库 `llm_gateway`），darwin/arm64。
> 采集工具：`scripts/capacity-baseline/run.sh`（01 全景 / 02 分区覆盖 / 03 增长速率 + 本轮补充时间跨度查询）。
> 原始输出：`scripts/capacity-baseline/reports/capacity-20260905-065455.txt`（已 gitignore，不入仓）。
> **注意：本基线来自开发库，写入模式与生产不同（生产写入更连续、无本地重启/回填噪声）。**
> 生产/目标环境实采需 DB 凭据，超出本轮范围；下文"风险判定"对生产推断均按偏保守口径。

本文档是 `docs/perf-2026-09-05-core-quality-baseline.md` 建议下一步第 4 项的交付物，
其第 5 节风险判定是 **URSM v2 shadow/canary 迁移的前置门禁**。

## 1. 全景

总库尺寸：**18 GB**。Top 10 关系合计约 15.1 GB（≈84%）：

| # | 关系 | 总尺寸 | 堆 | 索引 | 行数（精确 COUNT） | 占库 | 说明 |
|---|---|---:|---:|---:|---:|---:|---|
| 1 | `ursm_node_snapshot_min` | 6655 MB | 5353 MB | 1301 MB | 13,720,919 | 36% | URSM v2 快照落盘，**无清理** |
| 2 | `request_stage_events` | 2833 MB | 1742 MB | 1091 MB | 6,961,444 | 15% | trace 舞台事件，**无清理** |
| 3 | `request_state_transitions` | 2808 MB | 323 MB | 2485 MB | 905,043 | 15% | 有 7 天保留；索引/堆 = 7.7:1 |
| 4 | `request_logs_bodies_hot` | 863 MB | 17 MB | 13 MB | 7,655 | 4.7% | 堆+索引仅 30MB，TOAST ≈ 833 MB |
| 5 | `session_bodies_2026_09` | 822 MB | 15 MB | 14 MB | 29,840 | 4.5% | TOAST 为主（请求/响应体） |
| 6 | `analysis_events` | 591 MB | 372 MB | 219 MB | 723,084 | 3.2% | 未发现清理入口 |
| 7 | `stats_event_inbox_default` | 437 MB | 278 MB | 159 MB | 455,379 | 2.4% | inbox default 分区 |
| 8 | `usage_facts_default` | 406 MB | 215 MB | 191 MB | 455,390 | 2.2% | usage default 分区 |
| 9 | `request_context_attrs` | 354 MB | 229 MB | 125 MB | 109,192 | 1.9% | |
| 10 | `session_summaries` | 314 MB | 163 MB | 148 MB | 324,851 | 1.7% | |

前三大关系（URSM 快照 + stage events + state transitions）合计 12.3 GB，**占库 68%**。

其他观察：

- `stats_event_inbox_default` / `usage_facts_default` 均带 `_default` 分区且各 ~440MB——
  与 `request_logs` 同模式：月度分区未建，数据堆积 default 分区（见 §2）。
- `instance_heartbeats_old_backup`（70 MB）为遗留备份表，可清理。
- `request_logs_bodies_hot` 与 `session_bodies_*` 的体积几乎全在 TOAST，
  说明请求/响应体存储是 bodies 家族的体积主体，行数不能代表体积。

## 2. 保留覆盖（分区家族边界）

| 家族 | 分区现状 | 最早/最晚数据 | 结论 |
|---|---|---|---|
| `request_logs` | **仅 DEFAULT 分区**，无月度分区 | 2026-09-03 15:28 → 09-04 22:54（192 MB / 76,789 行） | ❌ 数据全部堆积 default，不随月滚动 |
| `request_logs_hot`（普通表） | 非分区 | 2026-09-04 22:55 → 09-05 06:56（7,736 行） | ✅ 热窗口恰为 ~8 小时，02:00 cron 清理生效，**热窗口随时间前移** |
| `request_logs_bodies` | 月度 2026_08（空）/ 2026_09（304 MB）/ 2026_10（空） | 09 月分区起 2026-09-03 | ✅ 月度滚动结构存在 |
| `request_logs_archive` / `_bodies_archive` | **不存在**（查询 0 行） | — | ❌ 归档家族未建，"归档"依赖脚本导出而非库内分区 |
| `instance_heartbeats` | 2026_07 / 08 / 09 三分区均 24 kB（空） | — | ⚠️ 分区结构健康但 dev 无数据；写入落在遗留表 `_old_backup`（70 MB） |
| `session_bodies` | `session_bodies_2026_09`（822 MB / 29,840 行）+ hot | — | ✅ 月度分区 |

要点：**hot 层保留（8 小时）真实生效**；但 `request_logs` 主表与
`stats_event_inbox` / `usage_facts` 的 default 分区无月度滚动，
长期增长只能靠 `manage-request-logs.sh` 一类的脚本式 DELETE，库内无分区级回收。

## 3. 增长斜率

开发库无 `request_logs_archive` 月度分区序列可作主依据（03 号查询 B 视角 0 行），
且 `pg_stat_user_tables` 计数器近期被重置（`n_tup_ins` 与实际行数差 3 个数量级，A 视角不可用）。
本轮改用**表内时间戳跨度 + 精确 COUNT** 推算：

| 关系 | 时间跨度 | 精确行数 | 平均速率 | 体积速率 | 保留机制 |
|---|---|---:|---:|---:|---|
| `ursm_node_snapshot_min` | 45 天（07-22 → 09-05） | 13,720,919 | 30.5 万行/天 | **≈148 MB/天** | **无** |
| `request_stage_events` | 47 天（07-20 → 09-05） | 6,961,444 | 14.8 万行/天 | ≈60 MB/天 | **无** |
| `request_state_transitions` | 7 天（08-29 → 09-05） | 905,043 | 12.9 万行/天 | ≈400 MB/天（写入） | ✅ 7 天 RetentionWorker → 稳态 ~2.8 GB 封顶 |
| `request_logs_bodies_2026_09` | 2 天 | 76,788 | — | ~150 MB/天 | 月度滚动 |

`ursm_node_snapshot_min` 写入极不均匀：8/22–8/28 峰值约 100 万行/天
（persist writer 每 60 秒 INSERT 全量 `(tenant, credential, raw_model)` 快照，ON CONFLICT DO NOTHING；
峰值期 ≈700 tuples/分钟），9/1–9/5 降至 0–12 万行/天（当前 env `URSM_V2_MODE=shadow`
且未开 `URSM_V2_SHADOW_DOUBLE_WRITE` → **persist writer 当前是禁用状态**，近几日写入为历史残留周期）。
风险判定按全期平均 148 MB/天 的保守口径。

### 6 / 12 个月预测（按当前 dev 口径外推，仅作量级参考）

| 关系 | 月增量 | 6 个月 | 12 个月 |
|---|---:|---:|---:|
| `ursm_node_snapshot_min`（无保留） | +4.5 GB | **+27 GB** | **+54 GB** |
| `request_stage_events`（无保留） | +1.8 GB | +11 GB | +22 GB |
| `request_state_transitions`（7 天保留） | 稳态 ~2.8 GB 封顶 | 2.8 GB | 2.8 GB |
| bodies 家族（TOAST 为主，月度滚动+脚本清理） | +4–5 GB/月毛增 | 视归档策略 | 视归档策略 |

生产口径下 URSM persist 若常开（canary/authoritative 必然常开），
`ursm_node_snapshot_min` 增速不低于 dev 全期平均——上表第一行即主导项。

## 4. 保留参数快照

`admin/data_lifecycle_cron_env.go` 定义的参数在**本地环境**（容器 `llm-gateway-local-8782`）的实测值：

| 参数 | 本地实际值 | 说明 |
|---|---|---|
| `HOT_CRON_DISABLED` | 未设置 | 默认启用 |
| `HOT_CRON_RUN_AT` | 未设置 | 默认 02:00 |
| `HOT_CRON_RETENTION_HOURS` | 未设置 | 默认 8 —— 与 §2 实测热窗口精确吻合 |
| `HOT_CRON_BATCH_SIZE` | 未设置 | 默认 500 |
| `HOT_CRON_MAX_RETRIES` | 未设置 | 默认 3 |
| `HOT_CRON_BACKOFF_SECONDS` | 未设置 | 默认 30 |
| `URSM_V2_MODE` | **shadow** | |
| `URSM_V2_SHADOW_DOUBLE_WRITE` | 未设置 | → persist writer 禁用（shadow 不落盘） |

生产/目标环境的同名参数 **待实采**（需 DB/环境凭据，超出本轮范围）。
上表证明了采集口径可用：热窗口默认值与库内数据边界一致。

## 5. 风险判定（URSM v2 门禁结论）

**结论：有条件通过 —— shadow 模式可继续；进入 canary/authoritative 之前必须先为
`ursm_node_snapshot_min` 建立保留/归档机制，并补齐 default 分区滚动。**

按必要性排序：

1. **`ursm_node_snapshot_min` 无界增长（硬阻塞）**：占库 36%，45 天 6.6 GB，
   全仓库唯一入口 `domains/ursm/v2/persist/writer.go` 只有 INSERT，无任何 DELETE/分区化。
   canary/authoritative 下 persist writer 常开，按 148 MB/天 外推 6 个月 +27 GB。
   要求：改月度分区（对齐 `request_logs_bodies` 模式）或加保留 cron（快照审计价值窗口建议 ≤30 天），
   两者任一落地并验证后，此门禁解除。
2. **`request_stage_events` 无保留（同批处理）**：+60 MB/天 无清理，6 个月 +11 GB。
   需明确保留窗口（建议对齐 requestjourney 的 7 天保留或走分区化）。
3. **`request_logs` 主表 / `stats_event_inbox` / `usage_facts` default 分区无月度滚动（中期风险）**：
   结构上已分区但未建月度子分区，数据全落 default，无法以 DROP PARTITION 回收空间。
   建议在建 649 号迁移同类的分区预建 cron 时一并补齐。
4. **`request_state_transitions` 索引膨胀（观察项）**：索引 2485 MB / 堆 323 MB（7.7:1），
   保留 worker 只删行不回收空间；若稳态长期运行需定期 `REINDEX CONCURRENTLY` 或评估索引冗余。
5. **小项**：`instance_heartbeats_old_backup`（70 MB）与 `request_logs` default 中 9/3 以前
   无数据的原因（疑似 dev 库部分重建）留待生产实采核对。

门禁与 `scripts/capacity-baseline/README.md` 的约定一致：第 1 项未解除前，
URSM v2 停留在 shadow（不落盘），不进入
`docs/06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md` 流程。

### 5.1 门禁项 1/2 整改交付（2026-09-05 同日）

第 1、2 项的解除路径已在本轮代码交付（保留 cron 方案，未动 schema）：

- `domains/ursm/v2/persist/retention.go` —— `SnapshotRetentionWorker`：
  1h tick 分批删除 `snapshot_ts` 早于保留期的快照行，每批独立事务（批 5000、
  单轮墙钟上限 10 分钟），`URSM_SNAPSHOT_RETENTION_DAYS` 默认 30、显式 0/负禁用；
  接线独立于 URSM v2 mode / persist writer 启用状态（shadow 未开 double-write 时
  历史残留同样回收）。main.go 在 persist writer 块后接线，shutdown 区 Stop。
- `internal/trace/stage_events_retention.go` —— `StageEventsRetentionWorker`：
  同型实现，按 `created_at` 清 `request_stage_events`，`STAGE_EVENTS_RETENTION_DAYS`
  默认 7（对齐 `request_state_transitions` 的 journey 保留）。main.go 在
  journeyRetentionWorker 旁接线。
- 验证：两包单测 + race 通过；批量 DELETE 在开发库回滚事务内实测——
  ursm 批删走 `ursm_node_snapshot_min_ts_idx` + PK（5000 行 / 196 ms），
  stage events 批删走 PK 回表、子查询 seq scan 找满 LIMIT 即停（5000 行 / 10 ms）；
  按 196 ms/批推算 13.7M 存量约 10 分钟净删除时间，受窗口上限分摊到数个 tick。
- 遗留与说明：worker 于下次部署重启后生效（本地容器仍跑旧镜像）；保留 cron 删除
  不回收磁盘空间，稳态体积取决于 autovacuum，生产若需立即收缩再评估 pg_repack 或
  后续分区化；default 分区滚动（第 3 项）不在本批范围。

## 附：本轮补充查询（供生产实采复用）

```sql
-- 表时间跨度 + 精确行数（增长斜率；pg_stat 计数器可能被重置，不可依赖）
SELECT COUNT(*), MIN(snapshot_ts), MAX(snapshot_ts) FROM ursm_node_snapshot_min;
SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM request_stage_events;
SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM request_state_transitions;
-- 近 N 天逐日写入（识别回填/峰值模式）
SELECT date_trunc('day', snapshot_ts)::date d, COUNT(*)
FROM ursm_node_snapshot_min
WHERE snapshot_ts >= NOW() - INTERVAL '14 days' GROUP BY 1 ORDER BY 1 DESC;
```
