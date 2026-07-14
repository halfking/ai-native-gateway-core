# 2026-07-14 — 数据库存储清理（48 GB → 9.3 GB）

> 适用数据库：252 pg17 (llm_gateway)
> 涉及部署：154 llm-gateway-go

## 问题

252 pg17 数据库增长至 48 GB。主要膨胀源：

| 表 | 大小 | 占比 |
|---|---|---|
| `model_probe_runs_2026_07` | **37 GB** | 77% |
| `columnar_internal.chunk + stripe + chunk_group` | ~4.4 GB | 9% |
| `request_logs_bodies_2026_07` | **3.4 GB** | 7% |
| `request_logs_hot` | 3.4 GB | 7% |

## 根因分析

### model_probe_runs 37 GB — 10s 探测间隔 + 无噪声过滤

- **存储量**：~28 亿行（13 天内）。每天约 2.15 亿行，每秒约 2,500 次写入。
- **探测间隔**：`ProbeInterval = 10s`（见 `model_probe.go:55-68`）。每 10 秒对所有
  682 个 (credential, model) 探针对触发一次 cycle。
- **每 cycle 写入**：`model_probe_backoff_v2` 中 healthy 探针 10s tick + noise
  无过滤 → 每条 watchdog 探测都 INSERT 一行。
- **2026-07-13 修复**（已发布）：
  1. `ProbeInterval` = 10s → 5min（30 倍降低）
  2. 噪声跳过过滤器（`model_probe.go:762`）：当 `stateChange=="unchanged" && status=="ok"`
     且无 http_status/error 时跳过 INSERT。~80% 的行被跳过。
- **现状**：hot 表 ~5,000 行/小时，promote 到月度分区。

### request_logs_bodies 3.4 GB — 月度分区无自动清理

- **存储内容**：请求/响应 JSONB payload（`request_body` ~170KB/行、`outbound_body` ~51KB/行、
  `response_body` ~902B/行）。
- **hot retention**：24h（2026-07-13 从 7d 下调）。超过 24h 的 body 被 promote 到月度分区。
- **月度分区无 TTL**：`request_logs_bodies_YYYY_MM` 只增不删。按 3.4 GB/月，年增长 ~40 GB。

## 清理操作

### 1. TRUNCATE model_probe_runs_2026_07（38 GB → 14 MB）

```sql
TRUNCATE model_probe_runs_2026_07;
```

保留分区结构（后续 promote 正常写入），释放 38 GB 列存空间。

### 2. TRUNCATE request_logs_bodies_2026_07（3.4 GB → 24 kB）

```sql
TRUNCATE request_logs_bodies_2026_07;
```

### 3. 设置调整

```sql
-- model_probe_runs: 90d → 14d
INSERT INTO settings_kv VALUES ('lifecycle.model_probe_runs_ttl_days', '14', 'integer', 'platform', 'lifecycle', NOW());
INSERT INTO settings_kv VALUES ('probe.partition_retention_days', '14', 'integer', 'platform', 'lifecycle', NOW());

-- request_logs_bodies: 新增 7 天月度分区 TTL
INSERT INTO settings_kv VALUES ('lifecycle.request_logs_bodies_ttl_days', '7', 'integer', 'platform', 'lifecycle', NOW());
```

### 4. 新增 SQL 函数

```sql
CREATE FUNCTION drop_old_request_logs_bodies_partitions(p_retention_days int)
RETURNS TABLE(dropped_partition text, rows_dropped bigint) LANGUAGE plpgsql AS $$
-- 遍历 request_logs_bodies 的所有月度分区
-- DROP 掉 month_end < cutoff_date 的分区
-- 返回被 drop 的分区名
$$;
```

### 5. 代码变更（154 deploy）

- `bg/partition_manager.go::archiveOldPartitionsIfNeeded` — 每次 tick 调用
  `dropOldRequestLogsBodiesPartitions`
- `bg/partition_manager.go` — 新增 `dropOldRequestLogsBodiesPartitions` 方法
- `settings/spec_lifecycle.go` — 注册 `lifecycle.request_logs_bodies_ttl_days` setting

## 结果

| 指标 | 清理前 | 清理后 |
|---|---|---|
| DB 总大小 | 48 GB | **9.3 GB** |
| model_probe_runs_2026_07 | 38 GB | 14 MB |
| request_logs_bodies_2026_07 | 3.4 GB | 24 kB |
| model_probe_runs retention | 90 天 | **14 天** |
| request_logs_bodies retention | 无限制 | **7 天** |

## 后续防复发

1. `lifecycle.model_probe_runs_ttl_days` = 14 + `probe.partition_retention_days` = 14：
   `drop_old_model_probe_runs_partitions()` 自动 DROP 超期的月度分区。每天检查一次。
2. `lifecycle.request_logs_bodies_ttl_days` = 7：
   `drop_old_request_logs_bodies_partitions()` 自动 DROP 超期的月度分区。每天检查一次。
3. ProbeInterval 5min + 噪声跳过过滤器：增量从 ~215 M 行/天降至 ~5,000 行/天。
4. 若需进一步降低 model_probe_runs 存储，可将 `lifecycle.model_probe_runs_ttl_days`
   调到 7（最小值 1）。

## 回滚

1. DROPPED SQL 函数：`DROP FUNCTION drop_old_request_logs_bodies_partitions(int);`
2. 设置回退：`DELETE FROM settings_kv WHERE key IN ('lifecycle.model_probe_runs_ttl_days', 'probe.partition_retention_days', 'lifecycle.request_logs_bodies_ttl_days')`
3. 代码回退：`git revert <commit-hash>; scp llm-gateway-go.bak-* llm-gateway-go; systemctl restart llm-gateway-go`
4. 数据恢复：无法恢复已 TRUNCATE 的数据（设计如此——不需要的数据不保留）。
