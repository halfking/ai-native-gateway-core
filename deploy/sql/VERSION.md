# LLM Gateway 数据库版本与初始化前提

## 目标

本目录下的 SQL 假定用于单机服务器上的单个目标数据库初始化，不依赖多节点操作说明。

## 数据库版本

建议环境：
- PostgreSQL `15.x`
- 推荐版本：`15.3` 或兼容版本

## 扩展要求

必需扩展：
- `pg_trgm`
- `citus_columnar`

可选扩展：
- `citus`

说明：
1. 历史归档分区使用的是 `citus_columnar`。
2. 如果运行环境同时安装了 `citus`，不影响本目录 SQL 的执行。
3. 若目标环境未启用列式存储能力，则 `request_logs_archive` 的 columnar 分区语句需要同步调整。

## request_logs / archive 说明

- `request_logs`：当前活跃请求日志，按月分区，使用 heap。
- `request_logs_archive`：历史归档日志，按月分区，使用 `citus_columnar`。
- `request_logs_default`：默认分区，用于兜底接收未命中月份分区的数据。

## 初始化数据范围

保留：
- `tenants`: 仅 `default`
- `users`: 仅 `admin`
- `applications`: 仅 `admin` / `applicant`
- `providers`: 标准 provider 配置，不含 credentials
- `work_type_config` / `work_type_model_route`

不保留：
- `api_keys`
- `credentials`
- `request_logs*` 数据
- `request_wal*` 数据
- `billing_orders`
- `credit_ledger*` / `usage_ledger*` 数据
- 详细审计、运营、业务明细数据

## 分区管理建议

月度分区应由初始化脚本、运维脚本或数据库定时任务按实际时间窗口创建。

示例：

```sql
CREATE TABLE request_logs_2026_09
    PARTITION OF request_logs
    FOR VALUES FROM ('2026-09-01 00:00:00+00') TO ('2026-10-01 00:00:00+00');

CREATE TABLE request_logs_archive_2026_09
    PARTITION OF request_logs_archive
    FOR VALUES FROM ('2026-09-01 00:00:00+00') TO ('2026-10-01 00:00:00+00')
    USING columnar;
```

## 执行前检查清单

- [ ] PostgreSQL 15.x 已安装
- [ ] `pg_trgm` 已安装
- [ ] `citus_columnar` 已安装
- [ ] 目标数据库已创建
- [ ] 初始化执行顺序按 README 中的顺序进行
- [ ] 如使用列式归档，目标环境支持 `USING columnar`
