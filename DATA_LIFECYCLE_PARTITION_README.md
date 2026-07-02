# 数据生命周期管理 - 分区表列存储归档功能

## 快速开始

在 https://llmgateway.internal.example.com/admin/data-lifecycle 中新增了分区表列存储归档管理功能，支持将历史数据自动迁移到高压缩比的 columnar 存储。

### 核心功能

1. **查看分区状态** - 查看所有分区表及其可归档分区
2. **手动归档** - 单次归档指定月份的分区
3. **批量归档** - 一次归档多个月份
4. **试运行模式** - 安全预览归档操作

### 支持的表

- `request_logs` → `request_logs_archive` (已存在)
- `request_wal` → `request_wal_archive` (新增)

### API 示例

```bash
# 1. 查看可归档的分区
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions

# 2. 试运行归档（推荐）
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":true}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive

# 3. 执行归档
curl -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"table_name":"request_logs","archive_month":"2026-04","dry_run":false}' \
  https://llmgateway.internal.example.com/api/admin/data-lifecycle/partitions/archive
```

## 文件变更

### 新增文件

```
admin/
├── data_lifecycle_partition.go      # 核心功能实现 (~530 行)
└── data_lifecycle_partition_test.go # 单元测试 (~150 行)

db/migrations/
├── 305_partition_archive_functions.sql      # 创建 request_wal 归档支持
└── 305_partition_archive_functions.down.sql # 回滚脚本

docs/
├── data-lifecycle-partition-archive.md              # 功能文档
└── data-lifecycle-partition-implementation-summary.md # 实现总结
```

### 修改文件

```
admin/handler.go  # 添加 3 个 API 路由（第 351-356 行）
```

## 部署清单

- [ ] 应用数据库迁移 305
- [ ] 验证归档函数已创建
- [ ] 重启服务
- [ ] 验证 API 可访问
- [ ] 试运行归档测试
- [ ] 配置监控告警（可选）

## 详细文档

- **功能使用指南**: [data-lifecycle-partition-archive.md](./data-lifecycle-partition-archive.md)
- **实现总结**: [data-lifecycle-partition-implementation-summary.md](./data-lifecycle-partition-implementation-summary.md)
- **原始需求**: [data-lifecycle-management.md](./data-lifecycle-management.md)

## 测试状态

✓ 所有单元测试通过  
✓ 编译成功  
✓ API 路由已注册  
✓ 数据库迁移已创建  

## 预期效果

- **存储压缩**: 15-40x 压缩比（columnar 存储）
- **示例**: 4GB 分区归档后约 100-200MB
- **自动化**: PartitionManager 每月自动归档 2 个月前的数据

## 权限要求

- **查询分区**: platform_ops 或 super_admin
- **执行归档**: super_admin（高风险操作）

## 联系支持

遇到问题请查看故障排查指南：[data-lifecycle-partition-archive.md#故障排查](./data-lifecycle-partition-archive.md#故障排查)

---

## 列存储持久化机制（Phase 23，2026-07-02）

> 目的：将"列存储"从一次性手工转换提升为**长效不变量**——新建分区时自动走 columnar，存量分区被夜间巡检自动修复，启动时报告漂移。

### 兼容性矩阵（这一份是事实之源）

| 父表 | 访问方式 | 留 heap 还是走 columnar | 理由 |
|---|---|---|---|
| `routing_decision_log` | INSERT-only | **columnar** | Go 端只 INSERT，无 UPDATE/DELETE |
| `credential_model_index` | INSERT-only | **columnar** | 同上（`credential_success_rate.go` 是 TRUNCATE，不是行级 DELETE） |
| `request_logs` | UPDATE-heavy + 大 JSONB | **heap**（暂留） | 体积由多 MB 的 `request_body` 撑起；需要 body-table 拆分后才能转 |
| `request_wal` | UPDATE-heavy | **heap** | `request_logger.go` 多次 UPDATE |
| `usage_ledger` | UPDATE-heavy | **heap** | `client.go` 多次 UPDATE（补 cost/tokens/latency） |
| `request_logs_archive` / `request_wal_archive` | 归档后只读 | 见 migration 318b | `request_logs_archive_*` 留 heap 防止 columnar 1 GB 序列化 buffer 溢出 |

依据来源：`bg/partition_manager.go` 和 `domains/hooks/observability/telemetry/*.go` 的实际行为审计（2026-07-02）。

### 装机内容（已部署到 184）

#### SQL 层

```
deploy/sql/phase-23-columnar-invariant/
├── 00-prereqs.sql                — 扩展检查
├── 01-rewrite-ensure-functions.sql — 四个 ensure_<t>_partition() 函数按上表分类
├── 02-event-trigger.sql          — CREATE TABLE / ALTER TABLE 之后的 dd_command_end 触发器
├── 03-healthcheck-and-heal.sql   — columnar_healthcheck() / columnar_heal() / columnar_drift_report()
├── 99-verify.sql                 — 最终报表
└── README.md
```

安装：

```bash
bash scripts/phase-23-apply.sh 184
```

幂等。可重复执行。

#### Go 层

- `bg/columnar_invariant_check.go`：启动时调用 `columnar_drift_report()`，输出每张父表的 compliant / noncompliant 计数
- `cmd/gateway/main.go`：启动后立即调用上述检查，**不阻塞启动**
- `bg/partition_manager.go::ensureSpecs()`：注册 `ensure_request_logs_bodies_partition()`（migration 328a 引入的兄弟表）

#### 巡检层

`scripts/columnar-daily-cron.sh` 每晚执行：

1. 调用 `columnar_drift_report()`，输出每张父表的状态
2. 调用 `columnar_heal()`，把所有 INSERT-only 父表的堆分区自动转 columnar
3. 调用 `backfill_request_logs_bodies()`，把 `request_logs` 的 body 列仍在源表的行搬到 `request_logs_bodies`（每个 CALL 一个事务，内存有界）

crontab 条目参考：

```
15 3 * * *  /opt/scripts/columnar-daily-cron.sh >> /var/log/columnar-daily.log 2>&1
```

### 验证查询

```sql
-- 列存储不变量：每张父表一行
SELECT * FROM columnar_drift_report();

-- 详细：所有分区的合规状态
SELECT parent_name, partition_name, storage, expected, compliant,
       pg_size_pretty(total_size_bytes)
FROM columnar_healthcheck()
WHERE NOT compliant;

-- 一次性修复
SELECT * FROM columnar_heal();
```

### 反向兼容

- **关闭列存储不变量**：在 184 上执行 `DROP EVENT TRIGGER enforce_columnar_trigger;`，然后用 `CREATE OR REPLACE FUNCTION` 把那几个 `ensure_*_partition()` 改回 heap。
- **回滚列存储 to heap**：`ALTER TABLE <part> SET ACCESS METHOD heap;` 不需要停机。
- Phase 23 与 phase-22 不冲突，可重复运行。

---

## Body-表拆分（request_logs → + request_logs_bodies，migration 328a/b）

> 目的：把 `request_logs` 表里体积占 99 % 的三个 JSONB 列（`request_body`、`outbound_body`、`response_body`）拆到独立的 columnar 兄弟表 `request_logs_bodies`，让 metadata-only 的 `request_logs_*` 分区最终走 columnar。

### 当前状态（已落到 184）

- 新表 `public.request_logs_bodies` 已建，按 `RANGE(ts)` 分区，所有分区为 **columnar**
- `request_logs_bodies_2026_06` 268 MB（columnar，对比之前在 heap 里 1.2 GB+）
- 历史数据**已回填**：6 775 / 6 775 行
- 运行时仍保留 `request_logs` 上的 body 列（双写将随 Go PR 启动）

### 三阶段迁移

#### Phase 1 ✅（migration 328a，2026-07-02 已落地 184）

```sql
CREATE TABLE request_logs_bodies (...);
-- ensure / backfill 工具全部就位
SELECT ensure_request_logs_bodies_partition(now());
CALL backfill_request_logs_bodies(200);   -- 反复 CALL 直到 rows_pending_backfill=0
```

文件：`db/migrations/328a_request_logs_bodies_table.sql`，`.down.sql` 已写。

#### Phase 2（Go PR，待合入）

修改 `domains/hooks/observability/telemetry/client.go::PersistRequestLog`：
- 单 INSERT 改为 2 个 INSERT（同一事务）：一个写 `request_logs`（不带 body 列），一个写 `request_logs_bodies`
- 修改 `admin/logs.go` 读取：从 `request_logs_bodies` 取 body 列（按 `(request_id, ts)` 索引）
- 修改 `admin/data_lifecycle_blobs.go` 与 `admin/data_lifecycle_attachments.go`：cleanup 同步删 body 表中的行

设计文档：[docs/COLUMNAR_BODY_SPLIT_PLAN.md](./docs/COLUMNAR_BODY_SPLIT_PLAN.md)

#### Phase 3（migration 328b，待合入）

```sql
ALTER TABLE request_logs DROP COLUMN request_body;
ALTER TABLE request_logs DROP COLUMN outbound_body;
ALTER TABLE request_logs DROP COLUMN response_body;
DROP INDEX IF EXISTS idx_request_logs_session_outbound;  -- 引用已删除列
SELECT * FROM columnar_heal();   -- 此时 metadata-only 分区已可转 columnar
```

预期落点：
- `request_logs_default` 1.2 GB → ~ 60 MB（≈ 95 % 压缩）
- `request_logs_archive_2026_06` 2.5 GB → ~ 120 MB
- `request_logs_bodies_*` 268 MB / 11 MB（已是 columnar）

### 设计要点

- **走 columnar 的限制**：`request_logs_archive_*` 即使没有 UPDATE 也不能转——`request_body` 单行可达数 MB，会撞 columnar 1 GB 序列化 buffer。328b 之后 metadata-only 就完全 OK。
- **双写一致性**：Phase 2 必须保持单事务（任意一句失败 → 两句都失败），不允许双写丢失。
- **回退**：两阶段都可回退。Phase 2 的代码变更可以分批 stage-deploy，row-level 调整不需要停机。

---

## 自动归属 & 自我修复（综合说明）

整个列存储机制的几个层次（一句话总结）：

1. **DDL 即时修复**：`ddl_command_end` 事件触发器 → INSERT-only 父表的新分区秒变 columnar
2. **夜间修复**：`columnar-daily-cron.sh` 跑 `columnar_heal()`，把漏网的存量分区补转
3. **启动诊断**：gateway boot 调 `columnar_drift_report()`，把当前不变量摘要写到 slog
4. **手动修复**：`SELECT * FROM columnar_heal();` 任意时间执行

**总方针：本套机制把"列存储是否生效"变成只看一张表的实时事实**，而不是人来记忆、脚本兜底的一次性动作。

