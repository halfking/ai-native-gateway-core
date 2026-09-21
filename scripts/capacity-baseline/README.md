# 容量/保留策略基线（capacity & retention baseline）

为路由权威迁移（URSM v2 shadow/canary）等重路由变更提供前置容量事实。
核心质量基线报告（`docs/perf-2026-09-05-core-quality-baseline.md`）的下一步第 4 项明确：
**容量/保留策略基线完成前，不开始 URSM v2 shadow/canary 迁移**。

## 组成

| 文件 | 作用 |
|---|---|
| `01-table-sizes.sql` | 表/分区/物化视图尺寸全景（Top 40，含行数估计与分区数） |
| `02-partition-coverage.sql` | 已知分区家族（request_logs* / instance_heartbeats）的分区边界与尺寸 → 验证保留窗口真实前移 |
| `03-growth-rate.sql` | 增长速率：archive 月度分区尺寸序列（主）+ 非分区大表写入统计（辅） |
| `run.sh` | 依次执行上述查询，报告存 `reports/capacity-<ts>.txt`（只读查询） |

## 运行

```bash
DB_HOST=... DB_PORT=5432 DB_USER=... DB_PASSWORD=... DB_NAME=... \
  scripts/capacity-baseline/run.sh
```

生产库执行请避开业务高峰；全部为只读目录查询，无锁风险（`pg_class`/`pg_inherits`/`pg_stat`）。

## 基线文档要求

每次采集后整理为 `docs/perf/capacity-retention-baseline-<日期>.md`，至少包含：

1. **全景**：总库尺寸、Top 10 关系及占比；
2. **保留覆盖**：request_logs 家族热/归档分区边界，最早与最晚分区，热窗口是否随时间前移；
3. **增长斜率**：archive 家族最近 3 个月分区尺寸（bytes/行数），推算月增量与 6/12 个月预测；
4. **保留参数快照**：`HOT_CRON_DISABLED / HOT_CRON_RUN_AT / HOT_CRON_RETENTION_HOURS /
   HOT_CRON_BATCH_SIZE / HOT_CRON_MAX_RETRIES / HOT_CRON_BACKOFF_SECONDS`
   （定义见 `admin/data_lifecycle_cron_env.go`）在目标环境的实际值；
5. **风险判定**：按预测容量给出的明确结论（可迁移 / 需先扩容或先压缩保留窗口）。

## 与 URSM v2 的关系

URSM v2 shadow/canary 会引入额外的路由状态写入与读取路径。基线文档第 5 项结论是迁移的
前置门禁：容量余量不足时先处理存储，再进入 `docs/06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md` 流程。
