# LLM Gateway 数据库版本与环境对比

## 导出基线

本目录中的拆分 SQL 以 `184` 节点上的 `llm_gateway` 数据库为基线整理。

- 节点: `__INTERNAL_PUBLIC_IP__`
- 数据库: `llm_gateway`
- PostgreSQL: `15.3 (Debian 15.3-1.pgdg120+1)`

## 扩展情况

### 184
- `btree_gist`
- `citus_columnar`
- `pg_trgm`
- `pgcrypto`
- `plpgsql`

### 71
- `btree_gist`
- `citus_columnar`
- `pg_trgm`
- `pgcrypto`
- `plpgsql`

### 本地 llm_gateway
- `btree_gist`
- `citus`
- `citus_columnar`
- `pg_trgm`
- `pgcrypto`
- `plpgsql`

## 重要说明

1. 184/71 真实使用的是 `citus_columnar`，不是独立名为 `columnar` 的扩展。
2. 本地环境比 184/71 多一个 `citus` 扩展，这属于环境差异，不代表 184 导出错误。
3. `request_logs_archive` 的历史分区使用列式存储；当前活跃 `request_logs` 分区仍是 heap。

## request_logs 分区对比

### 184
- `request_logs_2026_07`
- `request_logs_2026_08`
- `request_logs_default`
- `request_logs_archive_2026_06`
- `request_logs_archive_2026_07`

### 71
- `request_logs_2026_06`
- `request_logs_2026_07`
- `request_logs_2026_08`
- `request_logs_default`
- 无已挂载的 `request_logs_archive_YYYY_MM` 子分区

### 本地 llm_gateway
- `request_logs_2026_06`
- `request_logs_2026_07`
- `request_logs_2026_08`
- `request_logs_default`
- `request_logs_archive_2026_07`

## 初始化数据范围

保留：
- `tenants`: 仅 `default`
- `users`: 仅 `admin`
- `applications`: 仅 `admin` / `applicant`
- `providers`: 仅标准 provider 配置，不含 credentials，不含自定义业务 provider
- `work_type_config` / `work_type_model_route`

不保留：
- `api_keys`
- `credentials`
- `request_logs*` 数据
- `billing_orders`
- `credit_ledger*` / `usage_ledger*` 数据
- 详细审计、运营、业务明细数据

## 执行前提

建议环境：
- PostgreSQL 15.x
- 已安装 `citus_columnar`
- 若本地需要完全复刻当前容器行为，可同时安装 `citus`
